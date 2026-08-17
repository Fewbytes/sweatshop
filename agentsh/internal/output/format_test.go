package output

import (
	"fmt"
	"strings"
	"testing"
)

func TestDetectFamily(t *testing.T) {
	cases := []struct {
		cmd        string
		wantFamily string
		wantOk     bool
	}{
		// 1. go test
		{"go test", "go test", true},
		{"go test ./...", "go test", true},
		{"go test -v -run TestFoo ./pkg/...", "go test", true},
		{"CGO_ENABLED=0 go test -race .", "go test", true},
		{"FOO=bar VAR=\"baz\" go test ./...", "go test", true},
		{"cd /app/backend && go test ./...", "go test", true},
		{"/usr/local/go/bin/go test -v", "go test", true},

		// 2. pytest
		{"pytest", "pytest", true},
		{"pytest tests/unit", "pytest", true},
		{"pytest-3 -v", "pytest", true},
		{"python -m pytest", "pytest", true},
		{"python3 -m pytest tests/", "pytest", true},
		{"python3.11 -m pytest -k test_auth", "pytest", true},
		{"poetry run pytest -v", "pytest", true},
		{"pipenv run pytest", "pytest", true},
		{"ENV=test python -m pytest", "pytest", true},

		// 3. jest
		{"jest", "jest", true},
		{"npx jest", "jest", true},
		{"npx jest --coverage", "jest", true},
		{"yarn jest", "jest", true},
		{"pnpm jest", "jest", true},
		{"bun jest", "jest", true},
		{"npm test", "jest", true},
		{"yarn test", "jest", true},
		{"pnpm test", "jest", true},

		// 4. cargo
		{"cargo test", "cargo", true},
		{"cargo test --all", "cargo", true},
		{"cargo build --release", "cargo", true},
		{"cargo check", "cargo", true},
		{"cargo clippy", "cargo", true},
		{"/root/.cargo/bin/cargo test", "cargo", true},

		// 5. kubectl
		{"kubectl apply -f k8s/deploy.yaml", "kubectl", true},
		{"kubectl get pods -n prod", "kubectl", true},
		{"kubectl describe deployment web", "kubectl", true},
		{"kubectl delete pod/web-12345", "kubectl", true},
		{"/usr/bin/kubectl logs -f web", "kubectl", true},

		// 6. terraform
		{"terraform plan", "terraform", true},
		{"terraform apply -auto-approve", "terraform", true},
		{"terraform validate", "terraform", true},
		{"terraform init", "terraform", true},
		{"tofu plan", "terraform", true},
		{"tofu apply", "terraform", true},

		// Unknown / Negative commands (conservative detection)
		{"ls -la", "", false},
		{"echo \"go test ./...\"", "", false},
		{"cat cargo.toml", "", false},
		{"git commit -m \"run pytest\"", "", false},
		{"grep kubectl file.txt", "", false},
		{"python script.py", "", false},
		{"python3 manage.py runserver", "", false},
		{"make test", "", false},
		{"curl -X POST https://api.example.com", "", false},
		{"go build ./...", "", false},
		{"go run main.go", "", false},
	}

	for _, tc := range cases {
		gotFamily, gotOk := DetectFamily(tc.cmd)
		if gotOk != tc.wantOk || gotFamily != tc.wantFamily {
			t.Errorf("DetectFamily(%q) = (%q, %v), want (%q, %v)", tc.cmd, gotFamily, gotOk, tc.wantFamily, tc.wantOk)
		}
	}
}

func TestFormatGoTest(t *testing.T) {
	t.Run("verbose pass and fail with location and assertion", func(t *testing.T) {
		stdout := `=== RUN   TestAdd
--- PASS: TestAdd (0.00s)
=== RUN   TestSubtract
    math_test.go:25: assertion failed: got 3, want 2
--- FAIL: TestSubtract (0.01s)
=== RUN   TestMultiply
--- SKIP: TestMultiply (0.00s)
FAIL
exit status 1
FAIL	github.com/example/math	0.012s`

		summary := FormatCommand("go test -v ./...", stdout, "", 1)
		if summary == nil {
			t.Fatal("expected summary, got nil")
		}
		if summary.Family != "go test" {
			t.Errorf("Family = %q, want 'go test'", summary.Family)
		}
		if summary.Status != "failed" {
			t.Errorf("Status = %q, want 'failed'", summary.Status)
		}
		if summary.Passed != 1 || summary.Failed != 1 || summary.Skipped != 1 || summary.Total != 3 {
			t.Errorf("Counts: passed=%d failed=%d skipped=%d total=%d, want 1, 1, 1, 3", summary.Passed, summary.Failed, summary.Skipped, summary.Total)
		}
		if len(summary.Failures) != 1 {
			t.Fatalf("Failures count = %d, want 1", len(summary.Failures))
		}
		f := summary.Failures[0]
		if f.Name != "TestSubtract" {
			t.Errorf("Failure.Name = %q, want TestSubtract", f.Name)
		}
		if f.Location != "math_test.go:25" {
			t.Errorf("Failure.Location = %q, want math_test.go:25", f.Location)
		}
		if !strings.Contains(f.Message, "assertion failed") {
			t.Errorf("Failure.Message = %q, want to contain assertion failed", f.Message)
		}
	})

	t.Run("non-verbose package summaries pass", func(t *testing.T) {
		stdout := `ok  	github.com/example/pkg1	0.123s
ok  	github.com/example/pkg2	0.456s
?   	github.com/example/pkg3	[no test files]`

		summary := FormatCommand("go test ./...", stdout, "", 0)
		if summary == nil {
			t.Fatal("expected summary, got nil")
		}
		if summary.Status != "passed" {
			t.Errorf("Status = %q, want 'passed'", summary.Status)
		}
		if summary.Passed != 2 || summary.Skipped != 1 || summary.Failed != 0 || summary.Total != 3 {
			t.Errorf("Counts: passed=%d failed=%d skipped=%d total=%d", summary.Passed, summary.Failed, summary.Skipped, summary.Total)
		}
	})

	t.Run("build failure during go test", func(t *testing.T) {
		stdout := `# github.com/example/pkg
./foo_test.go:10:2: undefined: UnknownFunc
FAIL	github.com/example/pkg [build failed]`

		summary := FormatCommand("go test ./...", stdout, "", 1)
		if summary == nil {
			t.Fatal("expected summary, got nil")
		}
		if summary.Status != "failed" {
			t.Errorf("Status = %q, want 'failed'", summary.Status)
		}
		if len(summary.Failures) == 0 {
			t.Fatal("expected compiler failure frame")
		}
		f := summary.Failures[0]
		if f.Location != "./foo_test.go:10:2" && f.Location != "./foo_test.go:10" {
			t.Errorf("Failure.Location = %q", f.Location)
		}
		if !strings.Contains(f.Message, "undefined: UnknownFunc") {
			t.Errorf("Failure.Message = %q", f.Message)
		}
	})
}

func TestFormatPytest(t *testing.T) {
	t.Run("pytest failure with short test summary and assert frame", func(t *testing.T) {
		stdout := `============================= test session starts ==============================
rootdir: /app
collected 4 items

tests/test_auth.py .F.s                                                  [100%]

=================================== FAILURES ===================================
__________________________________ test_login __________________________________

    def test_login():
>       assert login("wrong") == True
E       AssertionError: assert False == True

tests/test_auth.py:15: AssertionError
=========================== short test summary info ============================
FAILED tests/test_auth.py::test_login - AssertionError: assert False == True
=================== 1 failed, 2 passed, 1 skipped in 0.23s ====================`

		summary := FormatCommand("pytest tests/", stdout, "", 1)
		if summary == nil {
			t.Fatal("expected summary, got nil")
		}
		if summary.Family != "pytest" {
			t.Errorf("Family = %q, want pytest", summary.Family)
		}
		if summary.Status != "failed" {
			t.Errorf("Status = %q, want failed", summary.Status)
		}
		if summary.Passed != 2 || summary.Failed != 1 || summary.Skipped != 1 || summary.Total != 4 {
			t.Errorf("Counts: passed=%d failed=%d skipped=%d total=%d", summary.Passed, summary.Failed, summary.Skipped, summary.Total)
		}
		if summary.Duration != "0.23s" {
			t.Errorf("Duration = %q, want 0.23s", summary.Duration)
		}
		if len(summary.Failures) == 0 {
			t.Fatal("expected failure item")
		}
		f := summary.Failures[0]
		if !strings.Contains(f.Name, "test_login") {
			t.Errorf("Failure.Name = %q", f.Name)
		}
		if !strings.Contains(f.Message, "AssertionError") {
			t.Errorf("Failure.Message = %q", f.Message)
		}
	})

	t.Run("pytest all passed", func(t *testing.T) {
		stdout := `============================= test session starts ==============================
collected 5 items
test_app.py .....                                                        [100%]
============================== 5 passed in 0.05s ===============================`

		summary := FormatCommand("python -m pytest", stdout, "", 0)
		if summary == nil {
			t.Fatal("expected summary, got nil")
		}
		if summary.Status != "passed" {
			t.Errorf("Status = %q, want passed", summary.Status)
		}
		if summary.Passed != 5 || summary.Failed != 0 || summary.Total != 5 {
			t.Errorf("Counts: passed=%d failed=%d total=%d", summary.Passed, summary.Failed, summary.Total)
		}
		if summary.Duration != "0.05s" {
			t.Errorf("Duration = %q, want 0.05s", summary.Duration)
		}
	})
}

func TestFormatJest(t *testing.T) {
	t.Run("jest multi-suite with failures", func(t *testing.T) {
		stdout := ` PASS  src/utils.test.ts
 FAIL  src/auth.test.ts
  ● Auth › login › should reject invalid password

    expect(received).toBe(expected) // Object.is equality

    Expected: 401
    Received: 500

      23 |     const res = await login("user", "wrong");
    > 24 |     expect(res.status).toBe(401);
         |                        ^
      25 |   });

      at Object.toBe (src/auth.test.ts:24:24)

Test Suites: 1 failed, 1 passed, 2 total
Tests:       1 failed, 3 passed, 4 total
Snapshots:   0 total
Time:        1.234 s
Ran all test suites.`

		summary := FormatCommand("npm test", stdout, "", 1)
		if summary == nil {
			t.Fatal("expected summary, got nil")
		}
		if summary.Family != "jest" {
			t.Errorf("Family = %q, want jest", summary.Family)
		}
		if summary.Status != "failed" {
			t.Errorf("Status = %q, want failed", summary.Status)
		}
		if summary.Passed != 3 || summary.Failed != 1 || summary.Total != 4 {
			t.Errorf("Counts: passed=%d failed=%d total=%d", summary.Passed, summary.Failed, summary.Total)
		}
		if summary.Duration != "1.234 s" {
			t.Errorf("Duration = %q", summary.Duration)
		}
		if len(summary.Failures) == 0 {
			t.Fatal("expected failure items")
		}
		f := summary.Failures[0]
		if !strings.Contains(f.Name, "Auth › login") {
			t.Errorf("Failure.Name = %q", f.Name)
		}
		if f.Location != "src/auth.test.ts:24:24" {
			t.Errorf("Failure.Location = %q, want src/auth.test.ts:24:24", f.Location)
		}
		if !strings.Contains(f.Message, "Expected: 401") {
			t.Errorf("Failure.Message = %q", f.Message)
		}
	})
}

func TestFormatCargo(t *testing.T) {
	t.Run("cargo test failure with panic backtrace", func(t *testing.T) {
		stdout := `   Compiling mypkg v0.1.0 (/path)
    Finished test [unoptimized + debuginfo] target(s) in 0.52s
     Running unittests src/lib.rs (target/debug/deps/mypkg-12345)

running 3 tests
test tests::test_add ... ok
test tests::test_bad ... FAILED
test tests::test_ignored ... ignored

failures:

---- tests::test_bad stdout ----
thread 'tests::test_bad' panicked at src/lib.rs:15:9:
assertion left == right failed
  left: 2
  right: 3

failures:
    tests::test_bad

test result: FAILED. 1 passed; 1 failed; 1 ignored; 0 measured; 0 filtered out; finished in 0.02s`

		summary := FormatCommand("cargo test", stdout, "", 101)
		if summary == nil {
			t.Fatal("expected summary, got nil")
		}
		if summary.Family != "cargo" {
			t.Errorf("Family = %q, want cargo", summary.Family)
		}
		if summary.Status != "failed" {
			t.Errorf("Status = %q, want failed", summary.Status)
		}
		if summary.Passed != 1 || summary.Failed != 1 || summary.Skipped != 1 || summary.Total != 3 {
			t.Errorf("Counts: passed=%d failed=%d skipped=%d total=%d", summary.Passed, summary.Failed, summary.Skipped, summary.Total)
		}
		if summary.Duration != "0.02s" {
			t.Errorf("Duration = %q, want 0.02s", summary.Duration)
		}
		if len(summary.Failures) == 0 {
			t.Fatal("expected failures")
		}
		f := summary.Failures[0]
		if f.Name != "tests::test_bad" {
			t.Errorf("Failure.Name = %q", f.Name)
		}
		if f.Location != "src/lib.rs:15:9" {
			t.Errorf("Failure.Location = %q, want src/lib.rs:15:9", f.Location)
		}
		if !strings.Contains(f.Message, "assertion left == right failed") {
			t.Errorf("Failure.Message = %q", f.Message)
		}
	})

	t.Run("cargo build compiler errors", func(t *testing.T) {
		stdout := `error[E0425]: cannot find value 'foo' in this scope
  --> src/main.rs:12:5
   |
12 |     foo();
   |     ^^^ not found in this scope

error: could not compile 'mypkg' (bin "mypkg") due to 1 previous error`

		summary := FormatCommand("cargo build", stdout, "", 101)
		if summary == nil {
			t.Fatal("expected summary, got nil")
		}
		if summary.Status != "failed" {
			t.Errorf("Status = %q, want failed", summary.Status)
		}
		if len(summary.Failures) == 0 {
			t.Fatal("expected compilation failure item")
		}
		f := summary.Failures[0]
		if f.Location != "src/main.rs:12:5" {
			t.Errorf("Failure.Location = %q, want src/main.rs:12:5", f.Location)
		}
		if !strings.Contains(f.Message, "cannot find value") {
			t.Errorf("Failure.Message = %q", f.Message)
		}
	})
}

func TestFormatKubectl(t *testing.T) {
	t.Run("kubectl apply mutations", func(t *testing.T) {
		stdout := `deployment.apps/web created
service/web-svc configured
configmap/app-config unchanged
pod/old-worker deleted`

		summary := FormatCommand("kubectl apply -f k8s/", stdout, "", 0)
		if summary == nil {
			t.Fatal("expected summary, got nil")
		}
		if summary.Family != "kubectl" {
			t.Errorf("Family = %q, want kubectl", summary.Family)
		}
		if summary.Status != "ok" {
			t.Errorf("Status = %q, want ok", summary.Status)
		}
		if summary.Added != 1 || summary.Changed != 1 || summary.Destroyed != 1 {
			t.Errorf("Mutations: added=%d changed=%d destroyed=%d, want 1, 1, 1", summary.Added, summary.Changed, summary.Destroyed)
		}
	})

	t.Run("kubectl get pods with failing pod", func(t *testing.T) {
		stdout := `NAME                    READY   STATUS             RESTARTS   AGE
web-6d5f78b7-abc12      1/1     Running            0          5m
web-6d5f78b7-def34      0/1     CrashLoopBackOff   3          5m
api-7c4d5e6f-ghi56      1/1     Running            0          10m`

		summary := FormatCommand("kubectl get pods", stdout, "", 0)
		if summary == nil {
			t.Fatal("expected summary, got nil")
		}
		if summary.Status != "failed" {
			t.Errorf("Status = %q, want failed due to CrashLoopBackOff pod", summary.Status)
		}
		if len(summary.Failures) != 1 {
			t.Fatalf("Failures = %d, want 1", len(summary.Failures))
		}
		f := summary.Failures[0]
		if f.Name != "web-6d5f78b7-def34" {
			t.Errorf("Failure.Name = %q", f.Name)
		}
		if !strings.Contains(f.Message, "CrashLoopBackOff") {
			t.Errorf("Failure.Message = %q", f.Message)
		}
	})

	t.Run("kubectl error from server", func(t *testing.T) {
		stderr := `Error from server (NotFound): deployments.apps "missing" not found`

		summary := FormatCommand("kubectl get deployment missing", "", stderr, 1)
		if summary == nil {
			t.Fatal("expected summary, got nil")
		}
		if summary.Status != "failed" {
			t.Errorf("Status = %q, want failed", summary.Status)
		}
		if len(summary.Failures) == 0 {
			t.Fatal("expected server error failure")
		}
		if !strings.Contains(summary.Failures[0].Message, "NotFound") {
			t.Errorf("Failure.Message = %q", summary.Failures[0].Message)
		}
	})
}

func TestFormatTerraform(t *testing.T) {
	t.Run("terraform plan with changes", func(t *testing.T) {
		stdout := `Terraform will perform the following actions:

  # aws_instance.web will be created
  + resource "aws_instance" "web" { ... }

Plan: 2 to add, 1 to change, 0 to destroy.`

		summary := FormatCommand("terraform plan", stdout, "", 0)
		if summary == nil {
			t.Fatal("expected summary, got nil")
		}
		if summary.Family != "terraform" {
			t.Errorf("Family = %q, want terraform", summary.Family)
		}
		if summary.Status != "changes_planned" {
			t.Errorf("Status = %q, want changes_planned", summary.Status)
		}
		if summary.Added != 2 || summary.Changed != 1 || summary.Destroyed != 0 {
			t.Errorf("Plan counts: added=%d changed=%d destroyed=%d", summary.Added, summary.Changed, summary.Destroyed)
		}
	})

	t.Run("terraform apply complete", func(t *testing.T) {
		stdout := `aws_instance.web: Creation complete after 10s [id=i-12345]

Apply complete! Resources: 1 added, 0 changed, 0 destroyed.`

		summary := FormatCommand("terraform apply", stdout, "", 0)
		if summary == nil {
			t.Fatal("expected summary, got nil")
		}
		if summary.Status != "ok" {
			t.Errorf("Status = %q, want ok", summary.Status)
		}
		if summary.Added != 1 || summary.Changed != 0 || summary.Destroyed != 0 {
			t.Errorf("Counts: added=%d changed=%d destroyed=%d", summary.Added, summary.Changed, summary.Destroyed)
		}
	})

	t.Run("terraform error diagnostics block", func(t *testing.T) {
		stdout := `╷
│ Error: Invalid resource type
│ 
│   on main.tf line 5, in resource "aws_instnace" "web":
│    5: resource "aws_instnace" "web" {
│ 
│ The provider hashicorp/aws does not support resource type "aws_instnace".
╵`

		summary := FormatCommand("terraform apply", stdout, "", 1)
		if summary == nil {
			t.Fatal("expected summary, got nil")
		}
		if summary.Status != "failed" {
			t.Errorf("Status = %q, want failed", summary.Status)
		}
		if len(summary.Failures) == 0 {
			t.Fatal("expected error failure")
		}
		f := summary.Failures[0]
		if !strings.Contains(f.Message, "Invalid resource type") {
			t.Errorf("Failure.Message = %q", f.Message)
		}
		if !strings.Contains(f.Location, "main.tf line 5") {
			t.Errorf("Failure.Location = %q, want main.tf line 5", f.Location)
		}
	})
}

func TestFormatMalformedAndDegradedBehavior(t *testing.T) {
	t.Run("malformed output for go test does not panic and degrades safely", func(t *testing.T) {
		summary := FormatCommand("go test ./...", "unexpected garbage \x00\xff nothing matches", "", 1)
		if summary == nil {
			t.Fatal("expected non-nil summary on supported command with non-zero exit")
		}
		if summary.Status != "failed" {
			t.Errorf("Status = %q, want failed", summary.Status)
		}
	})

	t.Run("empty output on success degrades safely", func(t *testing.T) {
		summary := FormatCommand("go test ./...", "", "", 0)
		if summary == nil {
			t.Fatal("expected summary")
		}
		if summary.Status != "passed" {
			t.Errorf("Status = %q, want passed", summary.Status)
		}
	})

	t.Run("bounding limits maximum failures to MaxSummaryFailures", func(t *testing.T) {
		var sb strings.Builder
		for i := 1; i <= 50; i++ {
			fmt.Fprintf(&sb, "=== RUN   Test%d\n--- FAIL: Test%d (0.00s)\n    test.go:%d: failed\n", i, i, i)
		}
		sb.WriteString("FAIL\n")

		summary := FormatCommand("go test ./...", sb.String(), "", 1)
		if summary == nil {
			t.Fatal("expected summary")
		}
		if summary.Failed != 50 {
			t.Errorf("Failed count = %d, want 50", summary.Failed)
		}
		if len(summary.Failures) > MaxSummaryFailures {
			t.Errorf("Failures slice len = %d, exceeded MaxSummaryFailures (%d)", len(summary.Failures), MaxSummaryFailures)
		}
	})
}
