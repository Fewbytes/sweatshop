package station

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/linestore"
)

// heijunkaCardsFake serves the ready and in-progress lists the state view reads.
type heijunkaCardsFake struct {
	fakeCards
	ready, inProgress string
}

func (f *heijunkaCardsFake) List(_ context.Context, l ListFilter) (json.RawMessage, error) {
	switch {
	case l.Ready:
		return json.RawMessage(f.ready), nil
	case l.Status == "in_progress" && l.Label == LabelLine:
		return json.RawMessage(f.inProgress), nil
	}
	return nil, errors.New("unexpected filter")
}

func TestHeijunkaStateExplainsWhyAMakerIsIdle(t *testing.T) {
	cases := []struct {
		name              string
		ready, inProgress string
		levels            linestore.Levels
		wantReason        string
	}{
		{"no cards", `[]`, `[]`, linestore.Levels{Agents: 1, WIPLimit: 2}, "no offerable cards"},
		{"cards without the line label", `[{"id":"a-1","labels":[]}]`, `[]`, linestore.Levels{WIPLimit: 2}, "label mismatch: 1"},
		{"cap reached", `[{"id":"a-1","labels":["line"]}]`, `[{"id":"b-1"}]`, linestore.Levels{WIPLimit: 1}, "WIP cap reached: 1 in progress, limit 1"},
		{"offered but unpulled", `[{"id":"a-1","labels":["line"]}]`, `[]`, linestore.Levels{WIPLimit: 2}, "1 offered cards wait"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newKaizenRig(t)
			r.srv.Store = openTestStore(t)
			r.srv.Cards = &heijunkaCardsFake{ready: c.ready, inProgress: c.inProgress}
			ctx := context.Background()
			if err := r.srv.Store.SetLevels(ctx, c.levels); err != nil {
				t.Fatal(err)
			}
			if err := r.mkr.Heartbeat(ctx, Heartbeat{State: StateIdle}); err != nil {
				t.Fatal(err)
			}
			hs, err := r.mkr.HeijunkaState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if hs.Levels != c.levels {
				t.Fatalf("levels = %+v, want %+v", hs.Levels, c.levels)
			}
			if len(hs.Idle) != 1 || hs.Idle[0].Station != "m" || !strings.Contains(hs.Idle[0].Reason, c.wantReason) {
				t.Fatalf("idle = %+v, want reason containing %q", hs.Idle, c.wantReason)
			}
		})
	}
}

func TestHeijunkaStateListsOfferedAndUnlabelledReadyCards(t *testing.T) {
	r := newKaizenRig(t)
	r.srv.Store = openTestStore(t)
	r.srv.Cards = &heijunkaCardsFake{
		ready:      `[{"id":"a-1","labels":["line"]},{"id":"a-2","labels":["proposed"]}]`,
		inProgress: `[{"id":"b-1"},{"id":"b-2"}]`,
	}
	hs, err := r.mkr.HeijunkaState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hs.Offered, []string{"a-1"}) || !reflect.DeepEqual(hs.Unlabelled, []string{"a-2"}) || hs.InProgress != 2 {
		t.Fatalf("state = %+v", hs)
	}
}

func TestHeijunkaStateWithoutDatabaseIs503(t *testing.T) {
	r := newKaizenRig(t)
	if _, err := r.mkr.HeijunkaState(context.Background()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("no database: %v", err)
	}
}
