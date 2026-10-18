package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Fewbytes/sweatshop/depmaint/internal/campaign"
)

// CampaignReport renders the campaign's report to out, or to the default
// committed location when out is empty.
func CampaignReport(ctx context.Context, root, id, outPath string, pub Published, w io.Writer) error {
	e, closeAll, err := openCampaignEngine(ctx, root, pub, w)
	if err != nil {
		return err
	}
	defer closeAll()
	report, err := e.Report(ctx, id)
	if err != nil {
		return err
	}
	if outPath == "" {
		outPath = filepath.Join(root, campaign.ReportPath(id))
	}
	if err := writeFileAll(outPath, report); err != nil {
		return err
	}
	fmt.Fprintf(w, "report: %s\n", outPath)
	return nil
}

// Escalate records the escalation, writes the campaign report, and files a
// bead pointing at it. filer is nil when beads is unavailable, which is
// reported but never an error (spec: Integration).
func Escalate(ctx context.Context, root, id, reason string, pub Published, filer campaign.BeadFiler, w io.Writer) error {
	e, closeAll, err := openCampaignEngine(ctx, root, pub, w)
	if err != nil {
		return err
	}
	defer closeAll()
	r, err := e.Escalate(ctx, id, reason, filer)
	if err != nil {
		return err
	}
	if err := writeFileAll(filepath.Join(root, r.ReportPath), r.Report); err != nil {
		return err
	}
	if !r.Filed {
		fmt.Fprintf(w, "escalation %s recorded; beads is unavailable (no bd on PATH or no .beads/), nobody was notified\nreport: %s\n", r.Escalation.ID, r.ReportPath)
		return nil
	}
	fmt.Fprintf(w, "escalation %s filed as %s\nreport: %s\n", r.Escalation.ID, r.Escalation.BeadID, r.ReportPath)
	return nil
}

func writeFileAll(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// bdFiler files beads by running `bd create`.
type bdFiler struct {
	bdPath string
	dir    string
}

// BdFiler returns a filer when bd is on PATH (per lookPath) and root holds
// a .beads/ directory, else nil.
func BdFiler(root string, lookPath func(string) (string, error)) campaign.BeadFiler {
	bd, err := lookPath("bd")
	if err != nil {
		return nil
	}
	if info, err := os.Stat(filepath.Join(root, ".beads")); err != nil || !info.IsDir() {
		return nil
	}
	return bdFiler{bdPath: bd, dir: root}
}

func (f bdFiler) File(ctx context.Context, title, body string) (string, error) {
	cmd := exec.CommandContext(ctx, f.bdPath, "create", title, "--description", body, "--silent")
	cmd.Dir = f.dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("bd create: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
