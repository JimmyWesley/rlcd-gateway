package setup

// Editing an agent's JSON settings file (Claude Code, OpenCode).
//
// Only the keys we set change; everything else in the file is kept (the file
// is re-encoded, so formatting and key order may change). What each key held
// before is kept in the gateway's own state, config.Dir()/agents/<agent>.json,
// never in the user's file, so undo can put a previous value back instead of
// just deleting the key. If the file is exactly as setup left it, undo
// restores the backup byte for byte.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
)

type keyState struct {
	Path     []string        `json:"path"`
	Existed  bool            `json:"existed"`
	Previous json.RawMessage `json:"previous,omitempty"`
}

type editState struct {
	File        string     `json:"file"`
	FileExisted bool       `json:"file_existed"`
	Backup      string     `json:"backup,omitempty"`
	Written     string     `json:"written_sha256"`
	Keys        []keyState `json:"keys"`
	// Created lists objects setup had to create, removed again on undo if empty.
	Created [][]string `json:"created,omitempty"`
	Time    time.Time  `json:"time"`
}

func statePath(agent string) string { return filepath.Join(config.Dir(), "agents", agent+".json") }

func loadState(agent string) (*editState, error) {
	b, err := os.ReadFile(statePath(agent))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st editState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", statePath(agent), err)
	}
	return &st, nil
}

func saveState(agent string, st *editState) error {
	if err := os.MkdirAll(filepath.Dir(statePath(agent)), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(statePath(agent), append(b, '\n'), 0o600)
}

// readJSONObject reads a settings file. missing=true when it does not exist.
func readJSONObject(path string) (obj map[string]any, raw []byte, missing bool, err error) {
	raw, err = os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil, true, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, raw, false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep numbers exactly as written
	if err := dec.Decode(&obj); err != nil {
		return nil, nil, false, fmt.Errorf("%s is not valid JSON (comments are not supported), refusing to touch it: %w", path, err)
	}
	if dec.More() {
		return nil, nil, false, fmt.Errorf("%s has trailing data after the JSON object, refusing to touch it", path)
	}
	if obj == nil {
		return nil, nil, false, fmt.Errorf("%s does not hold a JSON object, refusing to touch it", path)
	}
	return obj, raw, false, nil
}

func encodeJSON(obj map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(obj); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// lookup walks path; ok=false if any step is missing or not an object.
func lookup(obj map[string]any, path []string) (any, bool) {
	var cur any = obj
	for _, k := range path {
		m, isObj := cur.(map[string]any)
		if !isObj {
			return nil, false
		}
		if cur, isObj = m[k]; !isObj {
			return nil, false
		}
	}
	return cur, true
}

// set writes v at path, creating objects on the way. It refuses to replace a
// non-object on the way (that would destroy user data).
func set(obj map[string]any, path []string, v any) (created [][]string, err error) {
	cur := obj
	for i, k := range path[:len(path)-1] {
		next, exists := cur[k]
		if !exists {
			m := map[string]any{}
			cur[k] = m
			created = append(created, append([]string{}, path[:i+1]...))
			cur = m
			continue
		}
		m, isObj := next.(map[string]any)
		if !isObj {
			return nil, fmt.Errorf("%v is not an object", path[:i+1])
		}
		cur = m
	}
	cur[path[len(path)-1]] = v
	return created, nil
}

func del(obj map[string]any, path []string) {
	parent, ok := lookup(obj, path[:len(path)-1])
	if m, isObj := parent.(map[string]any); ok && isObj {
		delete(m, path[len(path)-1])
	}
}

func writeBackup(path string, b []byte) error {
	backup := backupName(path)
	return os.WriteFile(backup, b, 0o600)
}

// backupName is a timestamped name next to path that does not exist yet,
// so two runs in the same second never overwrite each other's backup.
func backupName(path string) string {
	base := fmt.Sprintf("%s.rlcd-backup-%s", path, time.Now().Format("20060102-150405"))
	name := base
	for i := 1; ; i++ {
		if _, err := os.Lstat(name); errors.Is(err, os.ErrNotExist) {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

// writeKeepMode writes b to path, keeping the file's mode if it exists.
func writeKeepMode(path string, b []byte) error {
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := path + ".rlcd-tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// jsonSet is one key setup writes.
type jsonSet struct {
	Path  []string
	Value any
}

// applyJSON sets each path to its value in the file and records what was
// there before. Re-running it while the file still points at the gateway
// keeps the first recorded state, so the value from before the gateway is
// never lost; isOurs tells a gateway value from the user's own.
func applyJSON(agent, path string, sets []jsonSet, isOurs func(any) bool) error {
	obj, raw, missing, err := readJSONObject(path)
	if err != nil {
		return err
	}
	prev, err := loadState(agent)
	if err != nil {
		return err
	}
	if prev != nil {
		ours := false
		for _, ks := range sets {
			if v, ok := lookup(obj, ks.Path); ok && isOurs(v) {
				ours = true
			}
		}
		if prev.File != path || !ours {
			prev = nil // stale: the file was changed back by hand since
		}
	}
	st := &editState{File: path, FileExisted: !missing, Time: time.Now()}
	if prev != nil {
		st.FileExisted, st.Backup, st.Created = prev.FileExisted, prev.Backup, prev.Created
	}
	for _, s := range sets {
		ks := keyState{Path: s.Path}
		if old := findKey(prev, s.Path); old != nil {
			ks = *old
		} else if v, ok := lookup(obj, s.Path); ok {
			ks.Existed = true
			ks.Previous, _ = json.Marshal(v)
		}
		st.Keys = append(st.Keys, ks)
		created, err := set(obj, s.Path, s.Value)
		if err != nil {
			return fmt.Errorf("%s: %w, refusing to touch it", path, err)
		}
		st.Created = append(st.Created, created...)
	}
	out, err := encodeJSON(obj)
	if err != nil {
		return err
	}
	if missing {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
	} else {
		backup := backupName(path)
		if err := os.WriteFile(backup, raw, 0o600); err != nil {
			return err
		}
		if st.Backup == "" {
			st.Backup = backup
		}
	}
	st.Written = sha(out)
	if err := saveState(agent, st); err != nil {
		return err
	}
	return writeKeepMode(path, out)
}

func findKey(st *editState, p []string) *keyState {
	if st == nil {
		return nil
	}
	for i := range st.Keys {
		if pathEq(st.Keys[i].Path, p...) {
			return &st.Keys[i]
		}
	}
	return nil
}

// undoJSON reverts applyJSON. Without recorded state it only removes keys
// that point at the gateway (isOurs), so a value the user set is never lost.
// It returns a short description of what it did.
func undoJSON(agent, path string, paths [][]string, isOurs func(any) bool) (string, error) {
	st, err := loadState(agent)
	if err != nil {
		return "", err
	}
	if st != nil && st.File != path {
		st = nil
	}
	obj, raw, missing, err := readJSONObject(path)
	if err != nil {
		return "", err
	}
	if missing {
		if st != nil {
			_ = os.Remove(statePath(agent))
		}
		return "nothing to undo", nil
	}

	// Untouched since setup: restore the original bytes.
	if st != nil && sha(raw) == st.Written {
		if !st.FileExisted {
			if err := os.Remove(path); err != nil {
				return "", err
			}
			_ = os.Remove(statePath(agent))
			return "removed the file setup had created", nil
		}
		if orig, err := os.ReadFile(st.Backup); err == nil {
			if err := writeKeepMode(path, orig); err != nil {
				return "", err
			}
			_ = os.Remove(statePath(agent))
			return "restored the original file", nil
		}
	}

	changed := false
	if st != nil {
		for _, ks := range st.Keys {
			cur, ok := lookup(obj, ks.Path)
			if ok && !isOurs(cur) {
				continue // the user changed it since; leave their value
			}
			if ks.Existed {
				var v any
				dec := json.NewDecoder(bytes.NewReader(ks.Previous))
				dec.UseNumber()
				if dec.Decode(&v) == nil {
					if _, err := set(obj, ks.Path, v); err == nil {
						changed = true
					}
				}
			} else if ok {
				del(obj, ks.Path)
				changed = true
			}
		}
		// Innermost first, so a parent emptied by removing its child goes too.
		created := append([][]string{}, st.Created...)
		sort.SliceStable(created, func(i, j int) bool { return len(created[i]) > len(created[j]) })
		for _, c := range created {
			if v, ok := lookup(obj, c); ok {
				if m, isObj := v.(map[string]any); isObj && len(m) == 0 {
					del(obj, c)
					changed = true
				}
			}
		}
	} else {
		for _, p := range paths {
			if v, ok := lookup(obj, p); ok && isOurs(v) {
				del(obj, p)
				changed = true
				// Drop parents left empty.
				for k := len(p) - 1; k > 0; k-- {
					if v, ok := lookup(obj, p[:k]); ok {
						if m, isObj := v.(map[string]any); isObj && len(m) == 0 {
							del(obj, p[:k])
							continue
						}
					}
					break
				}
			}
		}
	}
	if !changed {
		_ = os.Remove(statePath(agent))
		return "nothing pointed at the gateway; left unchanged", nil
	}
	out, err := encodeJSON(obj)
	if err != nil {
		return "", err
	}
	if err := writeBackup(path, raw); err != nil {
		return "", err
	}
	if err := writeKeepMode(path, out); err != nil {
		return "", err
	}
	_ = os.Remove(statePath(agent))
	if st != nil {
		return "restored the previous values", nil
	}
	return "removed the gateway override", nil
}
