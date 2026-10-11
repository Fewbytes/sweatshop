package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// commitAnnotation records the release commit on the Deployment.
const commitAnnotation = "sweatshop.line/commit"

// Kubernetes patches one container of a Deployment to the release's image
// digest and waits for the rollout. It speaks to the API server over HTTPS
// (a service-account token in-cluster) and touches no local file.
type Kubernetes struct {
	API        string // API server base URL
	Token      string
	Namespace  string
	Deployment string
	Container  string
	Probe      HTTPHealth
	Client     *http.Client  // default: 30 s timeout
	Rollout    time.Duration // longest rollout wait, default HealthWindow
	Poll       time.Duration // rollout poll interval, default 2s
}

type k8sDeployment struct {
	Metadata struct {
		Generation  int64             `json:"generation"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Replicas *int32 `json:"replicas"`
		Template struct {
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration  int64 `json:"observedGeneration"`
		Replicas            int32 `json:"replicas"`
		UpdatedReplicas     int32 `json:"updatedReplicas"`
		AvailableReplicas   int32 `json:"availableReplicas"`
		UnavailableReplicas int32 `json:"unavailableReplicas"`
	} `json:"status"`
}

func (k Kubernetes) path() string {
	return "/apis/apps/v1/namespaces/" + url.PathEscape(k.Namespace) + "/deployments/" + url.PathEscape(k.Deployment)
}

func (k Kubernetes) do(ctx context.Context, method, contentType string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(k.API, "/")+k.path(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+k.Token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	c := k.Client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, k.path(), resp.Status, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, out)
}

func (k Kubernetes) get(ctx context.Context) (k8sDeployment, string, error) {
	var d k8sDeployment
	if err := k.do(ctx, http.MethodGet, "", nil, &d); err != nil {
		return d, "", err
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
	return Release{Commit: d.Metadata.Annotations[commitAnnotation], Image: image, Digest: digest}, nil
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
	var d k8sDeployment
	if err := k.do(ctx, http.MethodPatch, "application/strategic-merge-patch+json", body, &d); err != nil {
		return err
	}
	return k.waitRollout(ctx, d.Metadata.Generation)
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
