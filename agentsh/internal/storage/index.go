package storage

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const (
	// DefaultCheckpointLines is the default number of lines between sparse
	// index checkpoints. Seeking to any line scans at most this many lines
	// from the nearest checkpoint.
	DefaultCheckpointLines = 1024
	// DefaultCheckpointBytes is the default number of bytes between sparse
	// index checkpoints, bounding the scan cost for streams with very long
	// lines.
	DefaultCheckpointBytes = 64 * 1024

	indexMagic      = "AGSHIDX"
	indexVersion    = 1
	indexHeaderSize = 24 // magic(7) + version(1) + lines(8) + bytes(8)
	indexEntrySize  = 16 // line(8) + offset(8)
)

// IndexEntry records the byte offset at which a 1-based line number begins.
type IndexEntry struct {
	Line   int64
	Offset int64
}

// LineIndex is a sparse, versioned line-to-byte-offset index for an immutable
// blob. Entries are sorted by Line ascending and always begin with {1, 0} for
// non-empty streams. Lines counts a trailing unterminated line as a line,
// matching the line counts reported by output.Read.
type LineIndex struct {
	Version uint32
	Lines   int64
	Bytes   int64
	Entries []IndexEntry
}

// Lookup returns the greatest entry whose Line does not exceed the requested
// line. Seeking to that entry's offset and scanning forward reaches the line.
func (idx *LineIndex) Lookup(line int64) IndexEntry {
	i := sort.Search(len(idx.Entries), func(i int) bool { return idx.Entries[i].Line > line })
	if i == 0 {
		return IndexEntry{Line: 1, Offset: 0}
	}
	return idx.Entries[i-1]
}

func (s BlobStore) checkpointLines() int64 {
	if s.CheckpointLines <= 0 {
		return DefaultCheckpointLines
	}
	return s.CheckpointLines
}

func (s BlobStore) checkpointBytes() int64 {
	if s.CheckpointBytes <= 0 {
		return DefaultCheckpointBytes
	}
	return s.CheckpointBytes
}

// IndexPath returns the sidecar index path for a blob digest.
func (s BlobStore) IndexPath(sha string) string {
	return filepath.Join(s.Index, sha+".idx")
}

// LoadIndex reads the index for a blob. It returns (nil, nil) when no index
// exists and (nil, err) when the index is corrupt or from an unsupported
// version.
func (s BlobStore) LoadIndex(sha string) (*LineIndex, error) {
	if s.Index == "" {
		return nil, nil
	}
	file, err := os.Open(s.IndexPath(sha))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	return readIndex(file)
}

// Rebuild scans a blob and regenerates its index, persisting the result. It is
// the recovery path for old blobs that predate indexing and corrupt indexes.
func (s BlobStore) Rebuild(sha string) (*LineIndex, error) {
	file, err := s.Open(sha)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	idx, err := BuildLineIndex(file, s.checkpointLines(), s.checkpointBytes())
	if err != nil {
		return nil, err
	}
	if s.Index != "" {
		if err := s.writeIndex(sha, idx); err != nil {
			return nil, err
		}
	}
	return idx, nil
}

// BuildLineIndex scans a stream and constructs a sparse line index using the
// given checkpoint strides. It is the shared implementation behind both the
// writer's in-memory index and Rebuild.
func BuildLineIndex(r io.Reader, strideLines, strideBytes int64) (*LineIndex, error) {
	if strideLines <= 0 {
		strideLines = DefaultCheckpointLines
	}
	if strideBytes <= 0 {
		strideBytes = DefaultCheckpointBytes
	}
	idx := &LineIndex{Version: indexVersion, Entries: []IndexEntry{{Line: 1, Offset: 0}}}
	br := bufio.NewReaderSize(r, 64*1024)
	var offset int64
	line := int64(1)
	lastLine := int64(1)
	lastOffset := int64(0)
	for {
		b, err := br.ReadBytes('\n')
		if len(b) == 0 {
			if err == nil {
				continue
			}
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		idx.Bytes += int64(len(b))
		idx.Lines++
		offset += int64(len(b))
		line++
		if line-lastLine >= strideLines || offset-lastOffset >= strideBytes {
			idx.Entries = append(idx.Entries, IndexEntry{Line: line, Offset: offset})
			lastLine, lastOffset = line, offset
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}
	if idx.Lines == 0 {
		idx.Entries = nil
	}
	return idx, nil
}

func (s BlobStore) writeIndex(sha string, idx *LineIndex) error {
	if s.Index == "" {
		return nil
	}
	if err := os.MkdirAll(s.Index, 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	header := make([]byte, indexHeaderSize)
	copy(header[:7], indexMagic)
	header[7] = indexVersion
	binary.BigEndian.PutUint64(header[8:16], uint64(idx.Lines))
	binary.BigEndian.PutUint64(header[16:24], uint64(idx.Bytes))
	buf.Write(header)
	var entry [indexEntrySize]byte
	for _, item := range idx.Entries {
		binary.BigEndian.PutUint64(entry[:8], uint64(item.Line))
		binary.BigEndian.PutUint64(entry[8:16], uint64(item.Offset))
		buf.Write(entry[:])
	}
	tmp, err := os.CreateTemp(s.Index, ".idx-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.IndexPath(sha))
}

func readIndex(r io.Reader) (*LineIndex, error) {
	header := make([]byte, indexHeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}
	if string(header[:7]) != indexMagic {
		return nil, errors.New("invalid line index magic")
	}
	version := header[7]
	if version != indexVersion {
		return nil, fmt.Errorf("unsupported line index version %d", version)
	}
	idx := &LineIndex{
		Version: uint32(version),
		Lines:   int64(binary.BigEndian.Uint64(header[8:16])),
		Bytes:   int64(binary.BigEndian.Uint64(header[16:24])),
	}
	var entry [indexEntrySize]byte
	for {
		n, err := io.ReadFull(r, entry[:])
		if n == indexEntrySize {
			idx.Entries = append(idx.Entries, IndexEntry{
				Line:   int64(binary.BigEndian.Uint64(entry[:8])),
				Offset: int64(binary.BigEndian.Uint64(entry[8:16])),
			})
			continue
		}
		if errors.Is(err, io.EOF) {
			break
		}
		return nil, errors.New("truncated line index")
	}
	return idx, nil
}
