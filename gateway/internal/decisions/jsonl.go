package decisions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// jsonlLog is an append-only JSON-lines file replayed into memory: the
// last line for a key wins. Outcomes and mirror results are written after
// their request's record, which is itself append-only, so they live next
// to it rather than in it.
type jsonlLog[T any] struct {
	path string
	key  func(T) string
	// gone reports an entry that deletes its key (an outcome set to null).
	gone func(T) bool

	once sync.Once
	mu   sync.RWMutex
	m    map[string]T
}

func newJSONLLog[T any](path string, key func(T) string) *jsonlLog[T] {
	return &jsonlLog[T]{path: path, key: key}
}

func (l *jsonlLog[T]) load() {
	l.once.Do(func() {
		l.m = map[string]T{}
		f, err := os.Open(l.path)
		if err != nil {
			return
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			var v T
			if json.Unmarshal(sc.Bytes(), &v) == nil {
				l.put(v)
			}
		}
	})
}

func (l *jsonlLog[T]) put(v T) {
	k := l.key(v)
	if l.gone != nil && l.gone(v) {
		delete(l.m, k)
		return
	}
	l.m[k] = v
}

// Append writes v and applies it.
func (l *jsonlLog[T]) Append(v T) error {
	l.load()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	l.put(v)
	return nil
}

// Get returns the entry for key.
func (l *jsonlLog[T]) Get(key string) (T, bool) {
	l.load()
	l.mu.RLock()
	defer l.mu.RUnlock()
	v, ok := l.m[key]
	return v, ok
}

// Each calls fn for every live entry.
func (l *jsonlLog[T]) Each(fn func(T)) {
	l.load()
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, v := range l.m {
		fn(v)
	}
}
