package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionCarriesStateAcrossInvocations(t *testing.T) {
	exec, _, root := testExecutor(t)
	subDir := filepath.Join(root, "sub_directory")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. cd to sub_directory and export a variable
	inv1, err := exec.Execute(context.Background(), Request{
		Command: `cd sub_directory; export MY_VAR="sweatshop_value"; my_func() { echo "func_called"; }`,
		Session: "test_session",
		CWD:     root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inv1.CWDAfter == nil || !strings.HasSuffix(*inv1.CWDAfter, "sub_directory") {
		t.Fatalf("expected CWDAfter to be sub_directory, got %v", inv1.CWDAfter)
	}
	if inv1.EnvDelta["MY_VAR"] != "sweatshop_value" {
		t.Fatalf("expected MY_VAR in EnvDelta, got %+v", inv1.EnvDelta)
	}

	// 2. Next command in the same session sees cwd, exported env var, and function
	inv2, err := exec.Execute(context.Background(), Request{
		Command: `pwd; echo "var:$MY_VAR"; my_func`,
		Session: "test_session",
		CWD:     root,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := readBlob(t, exec.Blobs, inv2.Stdout.SHA256)
	if !strings.Contains(out, "sub_directory") {
		t.Fatalf("missing carried CWD in output: %q", out)
	}
	if !strings.Contains(out, "var:sweatshop_value") {
		t.Fatalf("missing carried variable in output: %q", out)
	}
	if !strings.Contains(out, "func_called") {
		t.Fatalf("missing carried function in output: %q", out)
	}

	// 3. A different session does NOT have the state
	inv3, err := exec.Execute(context.Background(), Request{
		Command: `echo "var:$MY_VAR"`,
		Session: "other_session",
		CWD:     root,
	})
	if err != nil {
		t.Fatal(err)
	}
	out3 := readBlob(t, exec.Blobs, inv3.Stdout.SHA256)
	if strings.Contains(out3, "sweatshop_value") {
		t.Fatalf("session leak detected in other_session: %q", out3)
	}
}
