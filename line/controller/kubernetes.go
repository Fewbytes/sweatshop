package controller

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/docker/go-units"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

// Kubernetes is the Engine backed by one Pod per workload in the line's
// namespace. Its service account needs only get, list, create and delete on
// pods and get on pods/log in that namespace; it never touches Jobs or any
// other kind. The namespace is the line's network boundary, so Instance has
// no network to set here. Instance.Pids is not applied: a Pod has no pid
// limit of its own; the kubelet's podPidsLimit bounds it.
type Kubernetes struct {
	Client    kubernetes.Interface
	Line      string // the line's name; scopes every list
	Namespace string
}

func (k Kubernetes) podName(workload string) string {
	return "sweatshop-" + k.Line + "-" + workload
}

func (k Kubernetes) lineSelector() string {
	return LabelLine + "=" + k.Line
}

// Create submits the Pod with the hard limits of the design: no capabilities,
// no privilege escalation, non-root, no service-account token. The kubelet
// pulls the image. It fails with an AlreadyExists error if the workload has a Pod.
func (k Kubernetes) Create(ctx context.Context, in Instance) error {
	limits := corev1.ResourceList{}
	if in.Memory != "" {
		mem, err := units.RAMInBytes(in.Memory)
		if err != nil {
			return fmt.Errorf("memory %q: %w", in.Memory, err)
		}
		limits[corev1.ResourceMemory] = *resource.NewQuantity(mem, resource.BinarySI)
	}
	if in.CPUs != "" {
		cpus, err := strconv.ParseFloat(in.CPUs, 64)
		if err != nil || cpus <= 0 {
			return fmt.Errorf("cpus %q: want a positive number", in.CPUs)
		}
		limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(int64(cpus*1000), resource.DecimalSI)
	}
	env := make([]corev1.EnvVar, 0, len(in.Env))
	for name, value := range in.Env {
		env = append(env, corev1.EnvVar{Name: name, Value: value})
	}
	sort.Slice(env, func(i, j int) bool { return env[i].Name < env[j].Name })
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      k.podName(in.Workload),
			Namespace: k.Namespace,
			Labels:    map[string]string{LabelLine: k.Line, LabelWorkload: in.Workload},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   ptr.To(true),
				RunAsUser:      ptr.To(nonRootID),
				RunAsGroup:     ptr.To(nonRootID),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:      "workload",
				Image:     in.Image,
				Args:      in.Args,
				Env:       env,
				Resources: corev1.ResourceRequirements{Limits: limits},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					Privileged:               ptr.To(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
	if _, err := k.Client.CoreV1().Pods(k.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create pod: %w", err)
	}
	return nil
}

// List returns the line's Pods in any phase.
func (k Kubernetes) List(ctx context.Context) ([]Observed, error) {
	pods, err := k.Client.CoreV1().Pods(k.Namespace).List(ctx, metav1.ListOptions{LabelSelector: k.lineSelector()})
	if err != nil {
		return nil, err
	}
	out := make([]Observed, 0, len(pods.Items))
	for _, p := range pods.Items {
		o := Observed{Workload: p.Labels[LabelWorkload], Since: p.CreationTimestamp.Time}
		switch p.Status.Phase {
		case corev1.PodSucceeded, corev1.PodFailed:
			o.ExitCode = podExitCode(p)
			if t := finishedAt(p); !t.IsZero() {
				o.Since = t.Time
			}
		default:
			o.Running = true
		}
		out = append(out, o)
	}
	return out, nil
}

// podExitCode is the workload container's exit code. A Failed Pod whose
// container never ran to completion (evicted, unschedulable) reports 1, so a
// failure is never read as success.
func podExitCode(p corev1.Pod) int {
	for _, cs := range p.Status.ContainerStatuses {
		if t := cs.State.Terminated; t != nil {
			return int(t.ExitCode)
		}
	}
	if p.Status.Phase == corev1.PodFailed {
		return 1
	}
	return 0
}

func finishedAt(p corev1.Pod) metav1.Time {
	for _, cs := range p.Status.ContainerStatuses {
		if t := cs.State.Terminated; t != nil {
			return t.FinishedAt
		}
	}
	return metav1.Time{}
}

// Logs returns the last lines of the workload container's output.
func (k Kubernetes) Logs(ctx context.Context, workload string) (string, error) {
	rc, err := k.Client.CoreV1().Pods(k.Namespace).GetLogs(k.podName(workload), &corev1.PodLogOptions{TailLines: ptr.To(logTailLines)}).Stream(ctx)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	return string(b), err
}

// Remove deletes the workload's Pod; one already gone is not an error.
func (k Kubernetes) Remove(ctx context.Context, workload string) error {
	err := k.Client.CoreV1().Pods(k.Namespace).Delete(ctx, k.podName(workload), metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
