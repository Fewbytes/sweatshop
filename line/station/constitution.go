package station

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/linedir"
)

const (
	proposalPrefix    = "refs/line/constitution-proposals/"
	maxConstitution   = 256 << 10
	maxDiffSummary    = 1200
	maxProposalRetry  = 5
	proposedByTrailer = "Proposed-By"
)

// digestOf is the constitution digest stamped on attestations.
func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// LoadConstitution reads the constitution from refs/line/constitution in the
// line remote. When the ref is missing it imports file once and commits it;
// otherwise it logs a warning if file differs from the ref, since the ref wins.
func LoadConstitution(ctx context.Context, repo gitx.Repo, file string) (constitution.Constitution, string, error) {
	ok, err := repo.RefExists(ctx, linedir.ConstitutionRef)
	if err != nil {
		return constitution.Constitution{}, "", err
	}
	var data []byte
	if ok {
		if data, err = repo.ReadBlob(ctx, linedir.ConstitutionRef+":"+linedir.ConstitutionBlob); err != nil {
			return constitution.Constitution{}, "", err
		}
		if onDisk, err := os.ReadFile(file); err != nil || !bytes.Equal(onDisk, data) {
			log.Printf("warning: %s differs from %s; the ref is authoritative and the file is ignored", file, linedir.ConstitutionRef)
		}
	} else {
		if data, err = os.ReadFile(file); err != nil {
			return constitution.Constitution{}, "", err
		}
		if _, err := constitution.Parse(data); err != nil {
			return constitution.Constitution{}, "", fmt.Errorf("%s: %w", file, err)
		}
		if _, err := linedir.CommitConstitution(ctx, repo, data, "", "constitution: imported from constitution.toml", "line"); err != nil {
			return constitution.Constitution{}, "", fmt.Errorf("import constitution: %w", err)
		}
	}
	c, err := constitution.Parse(data)
	if err != nil {
		return constitution.Constitution{}, "", fmt.Errorf("%s: %w", linedir.ConstitutionRef, err)
	}
	return c, digestOf(data), nil
}

// routeConstitution handles GET /v1/constitution, GET /v1/constitution/log
// and POST /v1/constitution/proposals (viceroy only).
func (s *Server) routeConstitution(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	repo := s.Guard.Repo
	if repo.Dir == "" {
		return "", fail(http.StatusServiceUnavailable, "no line remote")
	}
	ctx := r.Context()
	want := http.MethodGet
	if r.URL.Path == "/v1/constitution/proposals" {
		want = http.MethodPost
	} else if r.URL.Path != "/v1/constitution" && r.URL.Path != "/v1/constitution/log" {
		return "", fail(http.StatusNotFound, "no route %s", r.URL.Path)
	}
	if err := requireMethod(w, r, want); err != nil {
		return "", err
	}
	head, err := repo.Ref(ctx, linedir.ConstitutionRef)
	if err != nil {
		return "", fail(http.StatusServiceUnavailable, "no constitution ref: %v", err)
	}
	switch r.URL.Path {
	case "/v1/constitution":
		body, err := repo.ReadBlob(ctx, head+":"+linedir.ConstitutionBlob)
		if err != nil {
			return "", err
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(body)
		return "constitution get " + head, nil
	case "/v1/constitution/log":
		history, err := repo.History(ctx, head)
		if err != nil {
			return "", err
		}
		type entry struct {
			Commit  string `json:"commit"`
			Message string `json:"message"`
		}
		entries := []entry{}
		for _, c := range history {
			entries = append(entries, entry{c.Commit, c.Subject})
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
		return fmt.Sprintf("constitution log %d", len(entries)), nil
	}
	if st.Role != viceroyRole {
		return "", fail(http.StatusForbidden, "role %q may not propose constitution changes", st.Role)
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConstitution))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return "", fail(http.StatusRequestEntityTooLarge, "constitution exceeds %d bytes", maxConstitution)
		}
		return "", fail(http.StatusBadRequest, "read body: %v", err)
	}
	if _, err := constitution.Parse(data); err != nil {
		return "", fail(http.StatusBadRequest, "%v", err)
	}
	cur, err := repo.ReadBlob(ctx, head+":"+linedir.ConstitutionBlob)
	if err != nil {
		return "", err
	}
	if string(cur) == string(data) {
		return "", fail(http.StatusBadRequest, "proposal is identical to the current constitution")
	}
	blob, err := repo.WriteBlob(ctx, data)
	if err != nil {
		return "", err
	}
	tree, err := repo.WriteTreeWithBlob(ctx, linedir.ConstitutionBlob, blob)
	if err != nil {
		return "", err
	}
	var n int
	var commit string
	msg := "constitution proposal by " + st.Name + "\n\n" + proposedByTrailer + ": " + st.Name
	for range maxProposalRetry {
		if n, err = nextProposal(ctx, repo); err != nil {
			return "", err
		}
		if commit, err = repo.CommitTree(ctx, tree, head, msg, st.Name); err != nil {
			return "", err
		}
		if err = repo.UpdateRef(ctx, proposalPrefix+strconv.Itoa(n), commit, ""); !errors.Is(err, gitx.ErrRefMoved) {
			break
		}
	}
	if err != nil {
		return "", err
	}
	if ch := s.Channel; ch != nil {
		text := fmt.Sprintf("constitution proposal %d by %s. King: `approve constitution %d`\n```diff\n%s\n```", n, st.Name, n, diffSummary(string(cur), string(data)))
		if err := ch.Discord.Post(ctx, ch.ChannelID, text); err != nil {
			return "", fail(http.StatusBadGateway, "proposal %d stored but the channel post failed: %v", n, err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposal": n, "commit": commit})
	return fmt.Sprintf("constitution proposal %d by %s", n, st.Name), nil
}

// nextProposal is one more than the highest proposal number in the remote.
func nextProposal(ctx context.Context, repo gitx.Repo) (int, error) {
	refs, err := repo.RefNames(ctx, proposalPrefix)
	if err != nil {
		return 0, err
	}
	top := 0
	for _, ref := range refs {
		if n, err := strconv.Atoi(strings.TrimPrefix(ref, proposalPrefix)); err == nil {
			top = max(top, n)
		}
	}
	return top + 1, nil
}

// diffSummary lists the lines removed from and added to the constitution.
func diffSummary(old, new string) string {
	count := func(s string) map[string]int {
		m := map[string]int{}
		for _, l := range strings.Split(s, "\n") {
			m[l]++
		}
		return m
	}
	om, nm := count(old), count(new)
	var lines []string
	for _, l := range strings.Split(old, "\n") {
		if nm[l] > 0 {
			nm[l]--
		} else if strings.TrimSpace(l) != "" {
			lines = append(lines, "- "+l)
		}
	}
	for _, l := range strings.Split(new, "\n") {
		if om[l] > 0 {
			om[l]--
		} else if strings.TrimSpace(l) != "" {
			lines = append(lines, "+ "+l)
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i][0] == '-' && lines[j][0] == '+' })
	out := strings.Join(lines, "\n")
	if len(out) > maxDiffSummary {
		out = strings.ToValidUTF8(out[:maxDiffSummary], "") + "\n… (truncated)"
	}
	return strings.ReplaceAll(out, "```", "'''")
}

var approveConstitutionRe = regexp.MustCompile(`^approve constitution ([0-9]{1,9})$`)

// ApproveConstitution fast-forwards refs/line/constitution to proposal n,
// recording the proposer and the king's message ID in the commit. The caller
// has already checked the author is the sovereign. It fails if the
// constitution moved since the proposal was made.
func (s *Server) ApproveConstitution(ctx context.Context, n int, msgID string) error {
	repo := s.Guard.Repo
	ref := proposalPrefix + strconv.Itoa(n)
	if ok, err := repo.RefExists(ctx, ref); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("no constitution proposal %d", n)
	}
	prop, err := repo.Ref(ctx, ref)
	if err != nil {
		return err
	}
	head, err := repo.Ref(ctx, linedir.ConstitutionRef)
	if err != nil {
		return err
	}
	parents, err := repo.Parents(ctx, prop)
	if err != nil {
		return err
	}
	if len(parents) != 1 || parents[0] != head {
		return fmt.Errorf("proposal %d is stale: the constitution changed after it was proposed; propose again", n)
	}
	data, err := repo.ReadBlob(ctx, prop+":"+linedir.ConstitutionBlob)
	if err != nil {
		return err
	}
	if _, err := constitution.Parse(data); err != nil {
		return err
	}
	who, err := repo.Trailer(ctx, prop, proposedByTrailer)
	if err != nil || len(who) != 1 {
		return fmt.Errorf("proposal %d: cannot tell who proposed it: %v", n, err)
	}
	msg := fmt.Sprintf("constitution: proposal %d by %s, approved by king message %s\n\n%s: %s\nApproved-By: king message %s",
		n, who[0], msgID, proposedByTrailer, who[0], msgID)
	if _, err := linedir.CommitConstitution(ctx, repo, data, head, msg, "king"); err != nil {
		return err
	}
	return nil
}
