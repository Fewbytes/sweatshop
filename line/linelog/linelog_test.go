package linelog

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactHidesSecretsButKeepsTheRest(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"known token value", "dial with s3cr3t-value failed", "dial with " + Placeholder + " failed"},
		{"bearer header", "Authorization: Bearer abcdef0123456789", "Authorization: Bearer " + Placeholder},
		{"key=value", "retry token=abc123 attempt=2", "retry token=" + Placeholder + " attempt=2"},
		{"password colon", `password: "hunter2" next`, `password: "` + Placeholder + `" next`},
		{"anthropic key", "using sk-ant-api03-AbCdEf123456 now", "using " + Placeholder + " now"},
		{"github token", "clone ghp_abcdefghijklmnop1234 ok", "clone " + Placeholder + " ok"},
		{"private key", "k -----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY----- done", "k " + Placeholder + " done"},
		{"plain line", "maker: gave up sweatshop-uorr: checks failed", "maker: gave up sweatshop-uorr: checks failed"},
	}
	for _, tt := range tests {
		if got := Redact(tt.in, "s3cr3t-value"); got != tt.want {
			t.Errorf("%s: Redact(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestRedactIgnoresEmptySecret(t *testing.T) {
	if got := Redact("abc", ""); got != "abc" {
		t.Fatalf("Redact with an empty secret = %q, want abc", got)
	}
}

func TestTeeWritesAndRecordsOnlyRedactedText(t *testing.T) {
	var out bytes.Buffer
	var recorded []string
	tee := Tee{Out: &out, Record: func(e string) { recorded = append(recorded, e) }, Secrets: []string{"tok-123"}}
	n, err := tee.Write([]byte("maker: auth tok-123 rejected\n"))
	if err != nil || n != len("maker: auth tok-123 rejected\n") {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if strings.Contains(out.String(), "tok-123") || out.String() != "maker: auth "+Placeholder+" rejected\n" {
		t.Errorf("Out = %q, want the token redacted", out.String())
	}
	if len(recorded) != 1 || recorded[0] != "maker: auth "+Placeholder+" rejected" {
		t.Errorf("recorded = %q, want one redacted entry without the newline", recorded)
	}
}
