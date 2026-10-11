package runner

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
)

func TestCLIArgs(t *testing.T) {
	spec := Spec{
		Name: "st-1", Image: "img@sha256:abc", Memory: "1g", Pids: 10,
		Env:     map[string]string{"A": "1"},
		Secrets: map[string]string{"TOKEN": "s3cret"},
		Mounts:  []Mount{{Volume: VolumeLineRemote, Target: "/line/remote.git", ReadOnly: true}},
	}
	for _, bin := range []string{"docker", "podman"} {
		c := &CLI{Bin: bin, Volumes: Volumes{VolumeLineRemote: "/srv/remote.git"}}
		args, env, err := c.args(spec)
		if err != nil {
			t.Fatal(err)
		}
		line := strings.Join(args, " ")
		if strings.Contains(line, "s3cret") {
			t.Errorf("%s: secret in argv: %s", bin, line)
		}
		if !slices.Contains(env, "TOKEN=s3cret") {
			t.Errorf("%s: secret missing from environment", bin)
		}
		if !strings.Contains(line, "-v /srv/remote.git:/line/remote.git:ro") {
			t.Errorf("%s: mount missing: %s", bin, line)
		}
		if hasHost := strings.Contains(line, "--add-host"); hasHost == (bin == "podman") {
			t.Errorf("%s: add-host = %v", bin, hasHost)
		}
		if args[len(args)-1] != spec.Image {
			t.Errorf("%s: image not last: %s", bin, line)
		}
	}
}

func TestCLIArgsRejects(t *testing.T) {
	c := &CLI{Volumes: Volumes{"v": "/x"}}
	for _, s := range []Spec{
		{Name: "-x", Image: "i"},
		{Name: "n", Image: "-i"},
		{Name: "n", Image: "i", Env: map[string]string{"A=B": "1"}},
		{Name: "n", Image: "i", Mounts: []Mount{{Volume: "nope", Target: "/t"}}},
		{Name: "n", Image: "i", Mounts: []Mount{{Volume: "v", Target: "rel"}}},
	} {
		if _, _, err := c.args(s); err == nil {
			t.Errorf("accepted %+v", s)
		}
	}
}

func TestNew(t *testing.T) {
	for _, e := range []string{"", "docker", "podman"} {
		if _, err := New(e, nil); err != nil {
			t.Errorf("%q: %v", e, err)
		}
	}
	for _, e := range []string{"k8s", "nomad", "ecs", "lxc"} {
		if _, err := New(e, nil); err == nil {
			t.Errorf("%q accepted", e)
		}
	}
}

func TestFakeLifecycle(t *testing.T) {
	ctx := context.Background()
	f := &Fake{ExitCode: 3, Output: "hi"}
	if err := f.Start(ctx, Spec{Name: "a", Image: "i"}); err != nil {
		t.Fatal(err)
	}
	if code, err := f.Wait(ctx, "a"); err != nil || code != 3 {
		t.Fatalf("wait = %d, %v", code, err)
	}
	var b bytes.Buffer
	if err := f.Logs(ctx, "a", &b); err != nil || b.String() != "hi" {
		t.Fatalf("logs = %q, %v", b.String(), err)
	}
	if st, _ := f.Inspect(ctx, "a"); !st.Exists || st.Running || st.ExitCode != 3 {
		t.Fatalf("inspect = %+v", st)
	}
	_ = f.Remove(ctx, "a")
	if st, _ := f.Inspect(ctx, "a"); st.Exists {
		t.Fatal("still exists")
	}
}
