package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Fewbytes/sweatshop/line/station"
)

// memCards is an in-memory station.Cards for the line serve side of the API.
type memCards struct {
	mu       sync.Mutex
	assignee map[string]string
	closed   map[string]string // id -> reason
	notes    map[string]string // id -> release note
	labels   map[string][]string
	ready    []Bead
}

func (m *memCards) List(_ context.Context, f station.ListFilter) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !f.Ready {
		return nil, fmt.Errorf("maker listed without ready=true: %+v", f)
	}
	var rows []Bead
	for _, b := range m.ready {
		if m.assignee[b.ID] == "" && m.closed[b.ID] == "" {
			rows = append(rows, b)
		}
	}
	return json.Marshal(rows)
}

func (m *memCards) Show(_ context.Context, id string) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return json.Marshal(map[string]string{"id": id, "assignee": m.assignee[id]})
}

func (m *memCards) Claim(_ context.Context, id, agent string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assignee[id] = agent
	return nil
}

func (m *memCards) Release(_ context.Context, id, agent, note string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.assignee[id] != agent {
		return fmt.Errorf("%s holds %s, not %s", m.assignee[id], id, agent)
	}
	m.assignee[id] = ""
	m.notes[id] = note
	return nil
}

func (m *memCards) Close(_ context.Context, id, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed[id] = reason
	return nil
}

func (m *memCards) AddLabel(_ context.Context, id, label string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.labels[id] = append(m.labels[id], label)
	return nil
}

func (m *memCards) Create(context.Context, station.NewCard) (json.RawMessage, error) {
	return nil, fmt.Errorf("maker may not create")
}

func (m *memCards) Comment(context.Context, string, string) error { return nil }

// apiRig serves memCards over a real station.Server and returns a runner that
// reaches cards only through it. A bd stub on PATH fails the test if run.
func apiRig(t *testing.T, e Engine) (*Runner, *memCards, func(string)) {
	t.Helper()
	stubs := t.TempDir()
	stub := "#!/bin/sh\ntouch " + filepath.Join(stubs, "bd-ran") + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(stubs, "bd"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(stubs, "bd-ran")); err == nil {
			t.Error("bd was executed in api mode")
		}
	})
	bare, work := remote(t)
	cards := &memCards{
		assignee: map[string]string{}, closed: map[string]string{}, notes: map[string]string{},
		labels: map[string][]string{}, ready: []Bead{{ID: "b-1", Title: "t", Priority: 1, Labels: []string{"line"}}},
	}
	srv := &station.Server{Stations: []*station.Station{{Name: "maker-1", Role: "maker", AgentID: "maker-1", Token: "mt"}}, Cards: cards}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	r := newRunner(t, bare, APICards{Client: station.Client{URL: ts.URL, Token: "mt"}}, e)
	r.Token = "mt"
	return r, cards, func(msg string) { commit(t, work, msg) }
}

func TestOnceAPIClaimsAndClosesLandedCard(t *testing.T) {
	r, cards, land := apiRig(t, nil)
	r.Engine = &fakeEngine{run: func(spec ContainerSpec) (int, error) {
		if spec.Env["SWEATSHOP_LINE_TOKEN"] != "mt" {
			t.Errorf("container token = %q, want the Runner.Token", spec.Env["SWEATSHOP_LINE_TOKEN"])
		}
		if got := cards.assignee["b-1"]; got != "maker-1" {
			t.Errorf("assignee during run = %q, want maker-1", got)
		}
		land("work\n\nBead: b-1")
		return 0, nil
	}}
	if worked, err := r.Once(context.Background()); err != nil || !worked {
		t.Fatalf("Once = %v %v", worked, err)
	}
	if cards.closed["b-1"] != "landed via line" {
		t.Fatalf("closed = %v", cards.closed)
	}
}

func TestOnceAPIReleasesAndGivesUpUnlandedCard(t *testing.T) {
	r, cards, _ := apiRig(t, &fakeEngine{run: func(ContainerSpec) (int, error) { return 3, nil }})
	r.MaxAttempts = 1
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := cards.notes["b-1"]; !strings.Contains(got, "exit 3") {
		t.Fatalf("release note = %q", got)
	}
	if cards.assignee["b-1"] != "" || len(cards.closed) != 0 {
		t.Fatalf("assignee %q closed %v, want released and open", cards.assignee["b-1"], cards.closed)
	}
	if !slices.Equal(cards.labels["b-1"], []string{station.LabelGaveUp}) {
		t.Fatalf("labels = %v, want %s", cards.labels["b-1"], station.LabelGaveUp)
	}
}

func TestAPICardsRefuseSettlingCardsTheStationDoesNotHold(t *testing.T) {
	r, cards, _ := apiRig(t, nil)
	cards.assignee["b-1"] = "someone-else"
	api := r.Beads.(APICards)
	ctx := context.Background()
	for name, err := range map[string]error{
		"close":   api.Close(ctx, "b-1", "x"),
		"release": api.Release(ctx, "b-1", "x"),
		"give-up": api.GiveUp(ctx, "b-1"),
	} {
		if err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("%s of another agent's card: err = %v, want 403", name, err)
		}
	}
	if len(cards.closed) != 0 || cards.assignee["b-1"] != "someone-else" {
		t.Fatalf("card changed: closed %v assignee %q", cards.closed, cards.assignee["b-1"])
	}
}
