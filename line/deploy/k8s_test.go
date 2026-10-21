package deploy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
)

// fakeKube is a Deployment API behind client-go's fake clientset: a patch
// swaps the image and bumps the generation; the rollout becomes ready after
// `lag` further GETs.
type fakeKube struct {
	image, commit        string
	generation, observed int64
	lag, left            int
	patches              []string
	stuck                bool
}

func (f *fakeKube) deployment() *appsv1.Deployment {
	ready := int32(0)
	if f.observed >= f.generation {
		ready = 2
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "line", Name: "serve", Generation: f.generation, Annotations: map[string]string{commitAnnotation: f.commit}},
		Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(2)), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "sidecar", Image: "x"}, {Name: "serve", Image: f.image}}}}},
		Status: appsv1.DeploymentStatus{ObservedGeneration: f.observed, Replicas: 2, UpdatedReplicas: ready, AvailableReplicas: ready},
	}
}

func newKube(t *testing.T, f *fakeKube) Kubernetes {
	t.Helper()
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		if f.left > 0 {
			f.left--
		} else if !f.stuck {
			f.observed = f.generation
		}
		return true, f.deployment(), nil
	})
	cs.PrependReactor("patch", "deployments", func(a k8stesting.Action) (bool, runtime.Object, error) {
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
		if err := json.Unmarshal(a.(k8stesting.PatchAction).GetPatch(), &p); err != nil {
			return true, nil, err
		}
		c := p.Spec.Template.Spec.Containers
		if len(c) != 1 || c[0].Name != "serve" {
			t.Errorf("patch touches containers %+v, want only serve", c)
		}
		f.image, f.commit = c[0].Image, p.Metadata.Annotations[commitAnnotation]
		f.patches = append(f.patches, f.image)
		f.generation++
		f.left = f.lag
		return true, f.deployment(), nil
	})
	return Kubernetes{
		Client: cs, Namespace: "line", Deployment: "serve", Container: "serve",
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
	if len(f.patches) != 1 || f.patches[0] != "reg.example/sweatshop-line@"+digest || f.left != 0 {
		t.Fatalf("patches %v, rollout lag left %d", f.patches, f.left)
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

func TestKubernetesMissingDeploymentIsATypedNotFound(t *testing.T) {
	k := Kubernetes{Client: fake.NewSimpleClientset(), Namespace: "line", Deployment: "serve", Container: "serve"}
	if _, err := k.Current(context.Background()); !apierrors.IsNotFound(err) {
		t.Fatalf("Current on a missing deployment: %v, want a NotFound API error", err)
	}
}
