package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	c1     = "1111111111111111111111111111111111111111"
	c2     = "2222222222222222222222222222222222222222"
	digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

type fakeAPI struct {
	want    *Release
	reports []Status
}

func (f *fakeAPI) Desired(context.Context) (*Release, error) { return f.want, nil }
func (f *fakeAPI) Report(_ context.Context, s Status) error {
	f.reports = append(f.reports, s)
	return nil
}

type fakeBackend struct {
	cur       Release
	log       []string
	applyErr  error
	healthy   bool
	rollbackE error
}

func (b *fakeBackend) Current(context.Context) (Release, error) { return b.cur, nil }
func (b *fakeBackend) Apply(_ context.Context, r Release) error {
	b.log = append(b.log, "apply "+r.Commit)
	if b.applyErr != nil {
		return b.applyErr
	}
	b.cur = r
	return nil
}
func (b *fakeBackend) Rollback(_ context.Context, p Release) error {
	b.log = append(b.log, "rollback "+p.Commit)
	if b.rollbackE != nil {
		return b.rollbackE
	}
	b.cur = p
	return nil
}
func (b *fakeBackend) Health(context.Context) error {
	if b.cur.Commit == c2 && !b.healthy {
		return errors.New("503")
	}
	return nil
}

func newActuator(api *fakeAPI, b *fakeBackend) *Actuator {
	return &Actuator{API: api, Backend: b, Container: true, Window: 30 * time.Millisecond, Poll: 5 * time.Millisecond}
}

func rel(c string) *Release {
	return &Release{Commit: c, Image: "reg.example/sweatshop-line", Digest: digest}
}

func TestActuatorAppliesHealthyRelease(t *testing.T) {
	api, b := &fakeAPI{want: rel(c2)}, &fakeBackend{cur: *rel(c1), healthy: true}
	a := newActuator(api, b)
	if err := a.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(api.reports) != 1 || api.reports[0].State != StateApplied || api.reports[0].Commit != c2 {
		t.Fatalf("reports %+v", api.reports)
	}
	if err := a.Once(context.Background()); err != nil || len(b.log) != 1 {
		t.Fatalf("applied twice: %v %v", err, b.log)
	}
}

func TestActuatorRollsBackOnHealthFailureAndDoesNotRetry(t *testing.T) {
	api, b := &fakeAPI{want: rel(c2)}, &fakeBackend{cur: *rel(c1)}
	a := newActuator(api, b)
	if err := a.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.log, ","); got != "apply "+c2+",rollback "+c1 {
		t.Fatalf("backend calls %s", got)
	}
	if len(api.reports) != 1 || api.reports[0].State != StateRolledBack || !strings.Contains(api.reports[0].Detail, "unhealthy") {
		t.Fatalf("reports %+v", api.reports)
	}
	_ = a.Once(context.Background())
	if len(b.log) != 2 {
		t.Fatalf("retried a failed release: %v", b.log)
	}
}

func TestActuatorApplyFailureAndFailedRollback(t *testing.T) {
	api, b := &fakeAPI{want: rel(c2)}, &fakeBackend{cur: *rel(c1), applyErr: errors.New("boom"), rollbackE: errors.New("stuck")}
	err := newActuator(api, b).Once(context.Background())
	if err == nil || len(api.reports) != 1 || api.reports[0].State != StateFailed {
		t.Fatalf("%v %+v", err, api.reports)
	}
	if !strings.Contains(api.reports[0].Detail, "boom") || !strings.Contains(api.reports[0].Detail, "stuck") {
		t.Fatalf("detail %q", api.reports[0].Detail)
	}
}

func TestActuatorRefusesUnpinnedImage(t *testing.T) {
	for _, r := range []*Release{
		{Commit: c2, Image: "reg.example/sweatshop-line"},
		{Commit: c2, Image: "reg.example/sweatshop-line:" + c2, Digest: digest},
		{Commit: c2, Image: "reg.example/sweatshop-line@" + digest, Digest: digest},
		{Commit: c2, Image: "reg.example/sweatshop-line", Digest: "sha256:abc"},
		{Commit: "main", Image: "reg.example/x", Digest: digest},
	} {
		api, b := &fakeAPI{want: r}, &fakeBackend{cur: *rel(c1)}
		_ = newActuator(api, b).Once(context.Background())
		if len(b.log) != 0 || len(api.reports) != 1 || api.reports[0].State != StateFailed {
			t.Fatalf("%+v applied: %v %+v", r, b.log, api.reports)
		}
	}
	// a registry port is not a tag
	if err := (Release{Commit: c1, Image: "localhost:5000/sweatshop-line", Digest: digest}).Validate(true); err != nil {
		t.Fatal(err)
	}
}

func TestActuatorNothingDesired(t *testing.T) {
	api, b := &fakeAPI{}, &fakeBackend{}
	if err := newActuator(api, b).Once(context.Background()); err != nil || len(b.log) != 0 || len(api.reports) != 0 {
		t.Fatalf("%v %v %v", err, b.log, api.reports)
	}
}
