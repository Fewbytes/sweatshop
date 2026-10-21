package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// commitAnnotation records the release commit on the Deployment.
const commitAnnotation = "sweatshop.line/commit"

// Kubernetes patches one container of a Deployment to the release's image
// digest and waits for the rollout. It speaks to the API server over HTTPS
// (a service-account token in-cluster) through client-go and touches no
// local file. API errors keep their client-go type (apierrors.IsNotFound).
type Kubernetes struct {
	API        string // API server base URL
	Token      string
	Namespace  string
	Deployment string
	Container  string
	Probe      HTTPHealth
	Client     kubernetes.Interface // default: built from API and Token
	Rollout    time.Duration        // longest rollout wait, default HealthWindow
	Poll       time.Duration        // rollout poll interval, default 2s
}

// clientset is the injected client, or one built from API and Token.
func (k Kubernetes) clientset() (kubernetes.Interface, error) {
	if k.Client != nil {
		return k.Client, nil
	}
	return kubernetes.NewForConfig(&rest.Config{Host: k.API, BearerToken: k.Token, Timeout: 30 * time.Second})
}

func (k Kubernetes) get(ctx context.Context) (*appsv1.Deployment, string, error) {
	cs, err := k.clientset()
	if err != nil {
		return nil, "", err
	}
	d, err := cs.AppsV1().Deployments(k.Namespace).Get(ctx, k.Deployment, metav1.GetOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("get deployment %s/%s: %w", k.Namespace, k.Deployment, err)
	}
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == k.Container {
			return d, c.Image, nil
		}
	}
	return d, "", fmt.Errorf("deployment %s/%s has no container %q", k.Namespace, k.Deployment, k.Container)
}

// splitImage separates repo@digest; a tag-only image has no digest.
func splitImage(ref string) (image, digest string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

func (k Kubernetes) Current(ctx context.Context) (Release, error) {
	d, ref, err := k.get(ctx)
	if err != nil {
		return Release{}, err
	}
	image, digest := splitImage(ref)
	return Release{Commit: d.Annotations[commitAnnotation], Image: image, Digest: digest}, nil
}

func (k Kubernetes) patch(ctx context.Context, ref, commit string) error {
	body, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]string{commitAnnotation: commit}},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []map[string]string{{"name": k.Container, "image": ref}},
		}}},
	})
	if err != nil {
		return err
	}
	cs, err := k.clientset()
	if err != nil {
		return err
	}
	d, err := cs.AppsV1().Deployments(k.Namespace).Patch(ctx, k.Deployment, types.StrategicMergePatchType, body, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch deployment %s/%s: %w", k.Namespace, k.Deployment, err)
	}
	return k.waitRollout(ctx, d.Generation)
}

// waitRollout is `kubectl rollout status`: the controller has seen the new
// generation and every replica is updated and available.
func (k Kubernetes) waitRollout(ctx context.Context, generation int64) error {
	limit, every := k.Rollout, k.Poll
	if limit <= 0 {
		limit = HealthWindow
	}
	if every <= 0 {
		every = 2 * time.Second
	}
	deadline := time.Now().Add(limit)
	for {
		d, _, err := k.get(ctx)
		if err != nil {
			return err
		}
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		s := d.Status
		if s.ObservedGeneration >= generation && s.UpdatedReplicas >= want && s.Replicas <= want &&
			s.AvailableReplicas >= want && s.UnavailableReplicas == 0 {
			return nil
		}
		if !time.Now().Add(every).Before(deadline) {
			return fmt.Errorf("rollout of %s/%s not complete after %s", k.Namespace, k.Deployment, limit)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

func (k Kubernetes) Apply(ctx context.Context, r Release) error {
	if err := r.Validate(true); err != nil {
		return err
	}
	return k.patch(ctx, r.Ref(), r.Commit)
}

func (k Kubernetes) Rollback(ctx context.Context, prev Release) error {
	if prev.Image == "" {
		return fmt.Errorf("no previous image to roll back to")
	}
	ref := prev.Image
	if prev.Digest != "" {
		ref = prev.Ref()
	}
	return k.patch(ctx, ref, prev.Commit)
}

func (k Kubernetes) Health(ctx context.Context) error { return k.Probe.Check(ctx) }
