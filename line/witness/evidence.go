package witness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
)

// maxEvidence bounds one check log: the tail of a long run is what explains a
// failure, and the station serves these logs to agents.
const maxEvidence = 4 << 20

const truncatedPrefix = "[truncated]\n"

var evidenceID = regexp.MustCompile(`^ev_[0-9a-f]{16}$`)

// LogRunner runs checks on the host like ExecRunner and keeps the combined
// output as evidence under Dir. ContainerRunner{Inner: LogRunner{...}} is the
// M1 runner.
type LogRunner struct{ Dir string }

func (r LogRunner) Run(ctx context.Context, dir, command string) (RunResult, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return RunResult{}, err
	}
	id := "ev_" + hex.EncodeToString(raw)

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = dir
	out := &tailBuffer{max: maxEvidence}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	var exitErr *exec.ExitError
	code := 0
	switch {
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	case err != nil:
		return RunResult{}, err
	}

	data := out.result()
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return RunResult{}, err
	}
	if err := os.WriteFile(filepath.Join(r.Dir, id+".log"), data, 0o600); err != nil {
		return RunResult{}, err
	}
	return RunResult{ExitCode: code, EvidenceID: id}, nil
}

// tailBuffer keeps only the last max bytes written, so agent-authored checks
// cannot exhaust the witness's memory with output.
type tailBuffer struct {
	mu      sync.Mutex
	max     int
	buf     []byte // ring storage, grows up to max
	start   int    // index of the oldest byte once full
	dropped bool
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(p) >= b.max {
		if len(p) > b.max || len(b.buf) > 0 {
			b.dropped = true
		}
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		b.start = 0
		return n, nil
	}
	if room := b.max - len(b.buf); room > 0 {
		take := min(room, len(p))
		b.buf = append(b.buf, p[:take]...)
		p = p[take:]
	}
	for len(p) > 0 { // full: overwrite the oldest bytes
		b.dropped = true
		c := copy(b.buf[b.start:], p)
		p = p[c:]
		b.start = (b.start + c) % b.max
	}
	return n, nil
}

// result returns the retained tail in order, prefixed when output was dropped.
func (b *tailBuffer) result() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, 0, len(truncatedPrefix)+len(b.buf))
	if b.dropped {
		out = append(out, truncatedPrefix...)
	}
	out = append(out, b.buf[b.start:]...)
	return append(out, b.buf[:b.start]...)
}

// ReadEvidence returns the log for id. The strict ID pattern is the path
// traversal defence: ids come from API callers.
func ReadEvidence(dir, id string) ([]byte, error) {
	if !evidenceID.MatchString(id) {
		return nil, fmt.Errorf("invalid evidence id %q", id)
	}
	return os.ReadFile(filepath.Join(dir, id+".log"))
}
