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

type BlobStore struct {
	Root  string
	Index string

	CheckpointLines int64
	CheckpointBytes int64
}

type BlobWriter struct {
	store BlobStore
	file  *os.File
	path  string
	hash  [sha256.Size]byte
	bytes int64
	lines int64

	checkpointLines int64
	checkpointBytes int64
	lineStart       int64
	index           LineIndex
	lastCheckLine   int64
	lastCheckByte   int64
}

func (s BlobStore) NewWriter() (*BlobWriter, error) {
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(s.Root, ".stream-*")
	if err != nil {
		return nil, err
	}
	return &BlobWriter{
		store: s, file: file, path: file.Name(),
		checkpointLines: s.checkpointLines(),
		checkpointBytes: s.checkpointBytes(),
	}, nil
}

func (w *BlobWriter) Write(data []byte) (int, error) {
	n, err := w.file.Write(data)
	written := data[:n]
	if n > 0 {
		pos := w.bytes
		w.bytes += int64(n)
		for i, b := range written {
			if b == '\n' {
				w.lines++
				w.lineStart = pos + int64(i) + 1
				w.maybeCheckpoint()
			}
		}
	}
	return n, err
}

func (w *BlobWriter) maybeCheckpoint() {
	if len(w.index.Entries) == 0 {
		w.index.Entries = append(w.index.Entries, IndexEntry{Line: 1, Offset: 0})
		w.lastCheckLine, w.lastCheckByte = 1, 0
	}
	current := w.lines + 1
	if current-w.lastCheckLine >= w.checkpointLines || w.lineStart-w.lastCheckByte >= w.checkpointBytes {
		w.index.Entries = append(w.index.Entries, IndexEntry{Line: current, Offset: w.lineStart})
		w.lastCheckLine, w.lastCheckByte = current, w.lineStart
	}
}

func (w *BlobWriter) totalLines() int64 {
	if w.bytes == 0 {
		return 0
	}
	if w.lineStart < w.bytes {
		return w.lines + 1
	}
	return w.lines
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
	w.index.Version = indexVersion
	w.index.Lines = w.totalLines()
	w.index.Bytes = w.bytes
	if w.store.Index != "" {
		_ = w.store.writeIndex(digest, &w.index)
	}
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
