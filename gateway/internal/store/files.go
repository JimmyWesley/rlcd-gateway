package store

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// writeAtomic writes data to path through a temp file in the same directory
// and a rename, so a reader never sees a partial file. Files are 0600 and
// directories 0700: bodies hold full prompts.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, 0o600)
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		os.Remove(tmp)
	}
	return werr
}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

func gunzipFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return io.ReadAll(zr)
}

// blobPath is blobs/<first two hex digits>/<sha256>.gz.
func (s *Store) blobPath(hash string) string {
	return filepath.Join(s.dir, "blobs", hash[:2], hash+".gz")
}

func validHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// putBlob stores a blob once. An existing blob gets its modification time
// refreshed: the janitor never deletes a blob touched within its grace
// period, which is what keeps a blob alive between this call and the
// record that references it being written.
func (s *Store) putBlob(b blob) (written bool, err error) {
	p := s.blobPath(b.hash)
	now := time.Now()
	if err := os.Chtimes(p, now, now); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, writeAtomic(p, gzipBytes(b.data))
}

// ErrBlobMissing is returned when a record references a blob that is gone.
var ErrBlobMissing = errors.New("content blob missing")

func (s *Store) getBlob(hash string) ([]byte, error) {
	if !validHash(hash) {
		return nil, fmt.Errorf("bad blob hash %q", hash)
	}
	b, err := gunzipFile(s.blobPath(hash))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrBlobMissing, hash)
	}
	return b, err
}
