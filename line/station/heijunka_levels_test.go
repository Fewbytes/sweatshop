package station

import (
	"context"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/linestore"
)

func TestHeijunkaLevelsAreSetByHeijunkaAndReadByAnyStation(t *testing.T) {
	r := newKaizenRig(t)
	r.srv.Store = openTestStore(t)
	ctx := context.Background()
	if l, err := r.mkr.HeijunkaLevels(ctx); err != nil || l != (linestore.Levels{}) {
		t.Fatalf("unset levels: %+v, %v", l, err)
	}
	want := linestore.Levels{Agents: 3, WIPLimit: 6}
	if err := r.heijunka.PublishHeijunkaLevels(ctx, want); err != nil {
		t.Fatal(err)
	}
	if l, err := r.mkr.HeijunkaLevels(ctx); err != nil || l != want {
		t.Fatalf("levels read by a maker: %+v, %v; want %+v", l, err, want)
	}
	if err := r.mkr.PublishHeijunkaLevels(ctx, linestore.Levels{Agents: 9}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("maker publish: %v", err)
	}
	if err := r.heijunka.PublishHeijunkaLevels(ctx, linestore.Levels{Agents: -1}); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("negative levels: %v", err)
	}
	if l, _ := r.mkr.HeijunkaLevels(ctx); l != want {
		t.Fatalf("a refused publish changed the levels to %+v", l)
	}
}

func TestHeijunkaLevelsWithoutDatabaseAre503(t *testing.T) {
	r := newKaizenRig(t)
	r.srv.Store = nil
	if _, err := r.mkr.HeijunkaLevels(context.Background()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("no database: %v", err)
	}
}
