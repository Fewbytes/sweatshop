package station

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"os"
	"path/filepath"
	"sync"
)

// EpicApprovals is the line's ledger of epics the king approved, persisted as
// JSON in the line dir. Only serve writes it (from the king's approval on the
// channel), so unlike the line label, which any agent may set, an entry proves
// the king approved that epic. Features and tasks inherit an epic's approval
// through an Inherit delegation only while the epic is listed here and open.
type EpicApprovals struct {
	Path string

	mu sync.Mutex
}

type epicApprovalsFile struct {
	// Epics maps an epic ID to the channel event that approved it.
	Epics map[string]string `json:"epics"`
}

func (e *EpicApprovals) load() (epicApprovalsFile, error) {
	var f epicApprovalsFile
	data, err := os.ReadFile(e.Path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(data, &f)
}

func (e *EpicApprovals) save(f epicApprovalsFile) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.Path), 0o700); err != nil {
		return err
	}
	tmp := e.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, e.Path)
}

// Approve records epic id as approved via the named channel event.
func (e *EpicApprovals) Approve(id, via string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	f, err := e.load()
	if err != nil {
		return err
	}
	if f.Epics == nil {
		f.Epics = map[string]string{}
	}
	f.Epics[id] = via
	return e.save(f)
}

// Revoke drops epic id's approval; revoking an unapproved epic is a no-op.
func (e *EpicApprovals) Revoke(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	f, err := e.load()
	if err != nil {
		return err
	}
	if _, ok := f.Epics[id]; !ok {
		return nil
	}
	delete(f.Epics, id)
	return e.save(f)
}

// Approved reports whether epic id is in the ledger.
func (e *EpicApprovals) Approved(id string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f, err := e.load()
	if err != nil {
		return false, err
	}
	_, ok := f.Epics[id]
	return ok, nil
}

// underApprovedEpic reports whether some ancestor is a ledgered epic that is
// still open. A closed epic loses its approval for good, so reopening it
// needs the king again.
func (s *Server) underApprovedEpic(ctx context.Context, cards Cards, ancestors []string) (bool, error) {
	if s.Epics == nil {
		return false, nil
	}
	for _, anc := range ancestors {
		approved, err := s.Epics.Approved(anc)
		if err != nil {
			return false, err
		}
		if !approved {
			continue
		}
		raw, err := cards.Show(ctx, anc)
		if err != nil {
			return false, err
		}
		var epic struct {
			Type   string `json:"issue_type"`
			Status string `json:"status"`
		}
		if !unmarshalIssue(raw, &epic) || epic.Type != constitution.EpicType {
			continue
		}
		if epic.Status == "closed" {
			if err := s.Epics.Revoke(anc); err != nil {
				return false, err
			}
			continue
		}
		return true, nil
	}
	return false, nil
}

// revokeEpicApproval drops id's approval when it closes.
func (s *Server) revokeEpicApproval(id string) error {
	if s.Epics == nil {
		return nil
	}
	return s.Epics.Revoke(id)
}
