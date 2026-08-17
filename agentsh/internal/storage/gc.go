package storage

import (
	"os"
	"path/filepath"
	"sort"
	"time"
)

type GCResult struct {
	Removed int   `json:"removed"`
	Bytes   int64 `json:"bytes"`
}

func (s BlobStore) GC(olderThan time.Duration, maxBytes int64) (GCResult, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return GCResult{}, err
	}
	type blob struct {
		path string
		size int64
		mod  time.Time
	}
	var blobs []blob
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || len(entry.Name()) != 64 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return GCResult{}, err
		}
		blobs = append(blobs, blob{filepath.Join(s.Root, entry.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(blobs, func(i, j int) bool { return blobs[i].mod.Before(blobs[j].mod) })
	cutoff := time.Now().Add(-olderThan)
	result := GCResult{}
	for _, item := range blobs {
		remove := (olderThan > 0 && item.mod.Before(cutoff)) || (maxBytes > 0 && total > maxBytes)
		if !remove {
			continue
		}
		if err := os.Remove(item.path); err != nil {
			return result, err
		}
		if s.Index != "" {
			_ = os.Remove(s.IndexPath(filepath.Base(item.path)))
		}
		result.Removed++
		result.Bytes += item.size
		total -= item.size
	}
	return result, nil
}
