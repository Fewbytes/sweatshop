package controller

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func newKubernetes() (Kubernetes, *fake.Clientset) {
	cs := fake.NewSimpleClientset()
	return Kubernetes{Client: cs, Line: "demo", Namespace: "line-demo"}, cs
}

func TestKubernetesCreateAppliesTheHardLimits(t *testing.T) {
	k, cs := newKubernetes()
	err := k.Create(t.Context(), Instance{Workload: "wl-1", Image: testImage,
		Args: []string{"just", "ci"}, Env: map[string]string{"B": "2", "A": "1"}, Memory: "1g", CPUs: "1.5", Pids: 64})
	if err != nil {
		t.Fatal(err)
	}
	pod, err := cs.CoreV1().Pods("line-demo").Get(t.Context(), "sweatshop-demo-wl-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := pod.Spec.Containers[0]
	sc, psc := c.SecurityContext, pod.Spec.SecurityContext
	var env []string
	for _, e := range c.Env {
		env = append(env, e.Name+"="+e.Value)
	}
	checks := map[string]bool{
		"labels":       pod.Labels[LabelLine] == "demo" && pod.Labels[LabelWorkload] == "wl-1",
		"image":        c.Image == testImage,
		"args":         strings.Join(c.Args, " ") == "just ci",
		"env":          strings.Join(env, ",") == "A=1,B=2",
		"never":        pod.Spec.RestartPolicy == corev1.RestartPolicyNever,
		"no-token":     *pod.Spec.AutomountServiceAccountToken == false,
		"non-root":     *psc.RunAsNonRoot && *psc.RunAsUser == 65534 && *psc.RunAsGroup == 65534,
		"cap-drop":     len(sc.Capabilities.Drop) == 1 && sc.Capabilities.Drop[0] == "ALL",
		"no-new-priv":  !*sc.AllowPrivilegeEscalation,
		"unprivileged": !*sc.Privileged,
		"memory":       c.Resources.Limits.Memory().Value() == 1<<30,
		"cpus":         c.Resources.Limits.Cpu().MilliValue() == 1500,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("pod check %q failed: %+v", name, pod.Spec)
		}
	}
}

func TestKubernetesCreateRefusesASecondPodForTheWorkload(t *testing.T) {
	k, _ := newKubernetes()
	in := Instance{Workload: "wl-1", Image: testImage}
	if err := k.Create(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if err := k.Create(t.Context(), in); !apierrors.IsAlreadyExists(err) {
		t.Fatalf("second create = %v; want AlreadyExists", err)
	}
}

func TestKubernetesListReportsStateOfOnlyThisLinesPods(t *testing.T) {
	k, cs := newKubernetes()
	created := metav1.NewTime(time.Unix(1000, 0))
	finished := metav1.NewTime(time.Unix(2000, 0))
	pod := func(name, line, workload string, phase corev1.PodPhase, term *corev1.ContainerStateTerminated) *corev1.Pod {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "line-demo", CreationTimestamp: created,
				Labels: map[string]string{LabelLine: line, LabelWorkload: workload}},
			Status: corev1.PodStatus{Phase: phase},
		}
		if term != nil {
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: term}}}
		}
		return p
	}
	for _, p := range []*corev1.Pod{
		pod("a", "demo", "wl-run", corev1.PodRunning, nil),
		pod("b", "demo", "wl-fail", corev1.PodFailed, &corev1.ContainerStateTerminated{ExitCode: 7, FinishedAt: finished}),
		pod("c", "demo", "wl-evicted", corev1.PodFailed, nil),
		pod("d", "other", "wl-other", corev1.PodRunning, nil),
	} {
		if _, err := cs.CoreV1().Pods("line-demo").Create(t.Context(), p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := k.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Observed{}
	for _, o := range got {
		byID[o.Workload] = o
	}
	if len(got) != 3 ||
		!byID["wl-run"].Running || byID["wl-run"].Since.Unix() != 1000 ||
		byID["wl-fail"].Running || byID["wl-fail"].ExitCode != 7 || byID["wl-fail"].Since.Unix() != 2000 ||
		byID["wl-evicted"].Running || byID["wl-evicted"].ExitCode != 1 {
		t.Fatalf("list = %+v", got)
	}
}

func TestKubernetesLogsReadsThePodLog(t *testing.T) {
	k, _ := newKubernetes()
	if err := k.Create(t.Context(), Instance{Workload: "wl-1", Image: testImage}); err != nil {
		t.Fatal(err)
	}
	logs, err := k.Logs(t.Context(), "wl-1")
	if err != nil || logs != "fake logs" { // the fake clientset's canned log
		t.Fatalf("logs = %q, %v; want the pod log", logs, err)
	}
}

func TestKubernetesRemoveDeletesAndToleratesAPodAlreadyGone(t *testing.T) {
	k, cs := newKubernetes()
	if err := k.Create(t.Context(), Instance{Workload: "wl-1", Image: testImage}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := k.Remove(t.Context(), "wl-1"); err != nil {
			t.Fatalf("remove: %v", err)
		}
	}
	pods, err := cs.CoreV1().Pods("line-demo").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(pods.Items) != 0 {
		t.Fatalf("pods after remove = %v, %v", pods, err)
	}
}
