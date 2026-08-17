package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type BlobStore struct{ Root string }

type BlobWriter struct {
	store BlobStore
	file  *os.File
	path  string
	hash  [sha256.Size]byte
	bytes int64
	lines int64
}

func (s BlobStore) NewWriter() (*BlobWriter, error) {
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(s.Root, ".stream-*")
	if err != nil {
		return nil, err
	}
	return &BlobWriter{store: s, file: file, path: file.Name()}, nil
}

func (w *BlobWriter) Write(data []byte) (int, error) {
	n, err := w.file.Write(data)
	w.bytes += int64(n)
	for _, b := range data[:n] {
		if b == '\n' {
			w.lines++
		}
	}
	return n, err
}

// OpenSnapshot opens the stream's current temporary file. It remains readable
// while the command appends to it.
func (w *BlobWriter) OpenSnapshot() (*os.File, error) {
	if w.path == "" {
		return nil, errors.New("blob writer has no backing file")
	}
	return os.Open(w.path)
}

func (w *BlobWriter) Commit() (StreamRef, error) {
	if w.file == nil {
		return StreamRef{}, errors.New("blob writer is closed")
	}
	name := w.file.Name()
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return StreamRef{}, w.abort(err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, w.file); err != nil {
		return StreamRef{}, w.abort(err)
	}
	copy(w.hash[:], h.Sum(nil))
	if err := w.file.Sync(); err != nil {
		return StreamRef{}, w.abort(err)
	}
	if err := w.file.Close(); err != nil {
		return StreamRef{}, w.abort(err)
	}
	w.file = nil
	digest := hex.EncodeToString(w.hash[:])
	destination := filepath.Join(w.store.Root, digest)
	if err := os.Rename(name, destination); err != nil {
		if _, statErr := os.Stat(destination); statErr == nil {
			_ = os.Remove(name)
		} else {
			return StreamRef{}, fmt.Errorf("finalize blob: %w", err)
		}
	}
	w.path = destination
	return StreamRef{SHA256: digest, Bytes: w.bytes, Lines: w.lines}, nil
}

func (w *BlobWriter) abort(cause error) error {
	name := w.file.Name()
	_ = w.file.Close()
	_ = os.Remove(name)
	w.file = nil
	return cause
}

func (s BlobStore) Open(sha string) (*os.File, error) {
	if len(sha) != sha256.Size*2 {
		return nil, errors.New("invalid blob digest")
	}
	return os.Open(filepath.Join(s.Root, sha))
}
