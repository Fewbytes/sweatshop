package deploy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeKube is a Deployment API: PATCH swaps the image and bumps the
// generation; the rollout becomes ready after `lag` further GETs.
type fakeKube struct {
	mu         sync.Mutex
	image      string
	commit     string
	generation int64
	observed   int64
	lag, left  int
	patches    []string
	auth       string
	stuck      bool
}

func (f *fakeKube) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = r.Header.Get("Authorization")
	if r.URL.Path != "/apis/apps/v1/namespaces/line/deployments/serve" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPatch {
		if ct := r.Header.Get("Content-Type"); ct != "application/strategic-merge-patch+json" {
			http.Error(w, ct, http.StatusUnsupportedMediaType)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var p struct {
			Metadata struct{ Annotations map[string]string }
			Spec     struct {
				Template struct {
					Spec struct {
						Containers []struct{ Name, Image string }
					}
				}
			}
		}
		_ = json.Unmarshal(body, &p)
		c := p.Spec.Template.Spec.Containers
		if len(c) != 1 || c[0].Name != "serve" {
			http.Error(w, "bad patch", http.StatusUnprocessableEntity)
			return
		}
		f.image, f.commit = c[0].Image, p.Metadata.Annotations[commitAnnotation]
		f.patches = append(f.patches, f.image)
		f.generation++
		f.left = f.lag
	} else if f.left > 0 {
		f.left--
	} else if !f.stuck {
		f.observed = f.generation
	}
	ready := int32(0)
	if f.observed >= f.generation {
		ready = 2
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"metadata": map[string]any{"generation": f.generation, "annotations": map[string]string{commitAnnotation: f.commit}},
		"spec": map[string]any{"replicas": 2, "template": map[string]any{"spec": map[string]any{
			"containers": []map[string]string{{"name": "sidecar", "image": "x"}, {"name": "serve", "image": f.image}}}}},
		"status": map[string]any{"observedGeneration": f.observed, "replicas": 2, "updatedReplicas": ready, "availableReplicas": ready},
	})
}

func newKube(t *testing.T, f *fakeKube) Kubernetes {
	t.Helper()
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	return Kubernetes{
		API: ts.URL, Token: "sa-token", Namespace: "line", Deployment: "serve", Container: "serve",
		Rollout: 2 * time.Second, Poll: time.Millisecond,
	}
}

func TestKubernetesPatchesDigestAndWaitsForRollout(t *testing.T) {
	f := &fakeKube{image: "reg.example/sweatshop-line@sha256:" + strings.Repeat("9", 64), commit: c1, generation: 1, observed: 1, lag: 3}
	k := newKube(t, f)
	ctx := context.Background()
	old, err := k.Current(ctx)
	if err != nil || old.Commit != c1 || old.Image != "reg.example/sweatshop-line" || old.Digest != "sha256:"+strings.Repeat("9", 64) {
		t.Fatalf("current %+v %v", old, err)
	}
	if err := k.Apply(ctx, *rel(c2)); err != nil {
		t.Fatal(err)
	}
	if f.auth != "Bearer sa-token" || len(f.patches) != 1 || f.patches[0] != "reg.example/sweatshop-line@"+digest {
		t.Fatalf("patches %v auth %q", f.patches, f.auth)
	}
	if err := k.Rollback(ctx, old); err != nil {
		t.Fatal(err)
	}
	if cur, _ := k.Current(ctx); cur != old {
		t.Fatalf("after rollback %+v, want %+v", cur, old)
	}
}

func TestKubernetesRefusesUnpinnedAndTimesOut(t *testing.T) {
	f := &fakeKube{image: "x@sha256:" + strings.Repeat("9", 64), generation: 1, observed: 1}
	k := newKube(t, f)
	ctx := context.Background()
	if err := k.Apply(ctx, Release{Commit: c2, Image: "reg.example/sweatshop-line"}); err == nil || len(f.patches) != 0 {
		t.Fatalf("unpinned apply: %v %v", err, f.patches)
	}
	f.stuck = true
	k.Rollout = 20 * time.Millisecond
	if err := k.Apply(ctx, *rel(c2)); err == nil || !strings.Contains(err.Error(), "not complete") {
		t.Fatalf("stuck rollout: %v", err)
	}
	k.Container = "missing"
	if _, err := k.Current(ctx); err == nil {
		t.Fatal("missing container")
	}
}
