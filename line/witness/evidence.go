package witness

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var exitErr *exec.ExitError
	code := 0
	switch {
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	case err != nil:
		return RunResult{}, err
	}

	data := out.Bytes()
	if len(data) > maxEvidence {
		data = append([]byte(truncatedPrefix), data[len(data)-maxEvidence:]...)
	}
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return RunResult{}, err
	}
	if err := os.WriteFile(filepath.Join(r.Dir, id+".log"), data, 0o600); err != nil {
		return RunResult{}, err
	}
	return RunResult{ExitCode: code, EvidenceID: id}, nil
}

// ReadEvidence returns the log for id. The strict ID pattern is the path
// traversal defence: ids come from API callers.
func ReadEvidence(dir, id string) ([]byte, error) {
	if !evidenceID.MatchString(id) {
		return nil, fmt.Errorf("invalid evidence id %q", id)
	}
	return os.ReadFile(filepath.Join(dir, id+".log"))
}
