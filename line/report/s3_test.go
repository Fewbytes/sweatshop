package report

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/secret"
)

func TestS3StorePut(t *testing.T) {
	var gotPath, gotAuth, gotType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath, gotAuth, gotType, gotBody = r.URL.EscapedPath(), r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(b)
		if r.Method != http.MethodPut || r.Header.Get("X-Amz-Content-Sha256") != sha256Hex(b) {
			w.WriteHeader(400)
		}
	}))
	defer srv.Close()
	s := S3Store{Endpoint: srv.URL, Bucket: "reports", Prefix: "p", AccessKey: "AK", SecretKey: "SK",
		Now: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }}
	if err := s.Put(context.Background(), "abc/run 1.xml", "application/xml", []byte("<x/>")); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/reports/p/abc/run%201.xml" || gotType != "application/xml" || gotBody != "<x/>" {
		t.Fatalf("path=%q type=%q body=%q", gotPath, gotType, gotBody)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AK/20260102/us-east-1/s3/aws4_request, SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date, Signature=") {
		t.Fatalf("auth = %q", gotAuth)
	}
}

func TestS3StoreError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "AccessDenied", 403)
	}))
	defer srv.Close()
	s := S3Store{Endpoint: srv.URL, Bucket: "b", AccessKey: "a", SecretKey: "s"}
	if err := s.Put(context.Background(), "k", "", nil); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewObjectStore(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	st, err := NewObjectStore("/tmp/r", env(nil))
	if err != nil || st != (FSStore{Root: "/tmp/r"}) {
		t.Fatalf("default = %v, %v", st, err)
	}
	st, err = NewObjectStore("", env(map[string]string{"SWEATSHOP_S3_BUCKET": "b", "SWEATSHOP_S3_ENDPOINT": "http://m:9000", "AWS_ACCESS_KEY_ID": "a", "AWS_SECRET_ACCESS_KEY": "s"}))
	if s3, ok := st.(S3Store); err != nil || !ok || s3.Bucket != "b" || s3.AccessKey != "a" {
		t.Fatalf("s3 = %v, %v", st, err)
	}
	if _, err := NewObjectStore("", env(map[string]string{"SWEATSHOP_S3_BUCKET": "b"})); err == nil {
		t.Fatal("want error for missing endpoint/credentials")
	}
}

func TestS3StoreListAndGet(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		if r.URL.EscapedPath() == "/reports" {
			gotQuery = r.URL.RawQuery
			io.WriteString(w, `<ListBucketResult><Contents><Key>p/runs/a.xml</Key></Contents><Contents><Key>p/runs/b.xml</Key></Contents></ListBucketResult>`)
			return
		}
		if r.URL.EscapedPath() == "/reports/p/runs/a.xml" {
			io.WriteString(w, "<a/>")
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	s := S3Store{Endpoint: srv.URL, Bucket: "reports", Prefix: "p", AccessKey: "AK", SecretKey: "SK"}
	keys, err := s.List(context.Background(), "runs/")
	if err != nil || len(keys) != 2 || keys[0] != "runs/a.xml" || keys[1] != "runs/b.xml" {
		t.Fatalf("list = %v, %v", keys, err)
	}
	if gotQuery != "list-type=2&prefix=p%2Fruns%2F" {
		t.Fatalf("query = %q", gotQuery)
	}
	got, err := s.Get(context.Background(), "runs/a.xml")
	if err != nil || string(got) != "<a/>" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if _, err := s.Get(context.Background(), "nope"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("missing get err = %v", err)
	}
}

func TestS3StorePresignGetSignsOnlyHostAndExpiry(t *testing.T) {
	s := S3Store{Endpoint: "https://s3.example.com", Bucket: "reports", Prefix: "p", AccessKey: "AK", SecretKey: "SK",
		Now: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }}
	got, err := s.PresignGet("kaizen/run 1/analysis.md", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://s3.example.com/reports/p/kaizen/run%201/analysis.md?X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AK%2F20260102%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20260102T030405Z" +
		"&X-Amz-Expires=900&X-Amz-SignedHeaders=host&X-Amz-Signature=57d815ebbfd6e4850819ebdab3902efce53692358ed06c4384865ba7dd769165"
	if got != want {
		t.Fatalf("presigned URL = %s\nwant %s", got, want)
	}
}

func TestS3StorePresignGetRejectsTTLOutsideSigV4Bounds(t *testing.T) {
	s := S3Store{Endpoint: "https://s3.example.com", Bucket: "reports", AccessKey: "AK", SecretKey: "SK"}
	for _, ttl := range []time.Duration{0, time.Millisecond, MaxPresignTTL + time.Second} {
		if _, err := s.PresignGet("k", ttl); err == nil {
			t.Errorf("ttl %s accepted", ttl)
		}
	}
}

func TestObjectStoreFromEnvTakesS3KeysFromCredentialFiles(t *testing.T) {
	t.Setenv("SWEATSHOP_S3_BUCKET", "b")
	t.Setenv("SWEATSHOP_S3_ENDPOINT", "http://m:9000")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	dir := t.TempDir()
	for name, v := range map[string]string{secret.S3AccessKeyCred: "access\n", secret.S3SecretKeyCred: "secret\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := ObjectStoreFromEnv("", secret.File{Dir: dir})
	s3, ok := st.(S3Store)
	if err != nil || !ok || s3.AccessKey != "access" || s3.SecretKey != "secret" {
		t.Fatalf("store = %#v, %v; want S3Store with the file keys", st, err)
	}
}

func TestObjectStoreFromEnvRejectsLooseCredentialFile(t *testing.T) {
	t.Setenv("SWEATSHOP_S3_BUCKET", "b")
	t.Setenv("SWEATSHOP_S3_ENDPOINT", "http://m:9000")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, secret.S3AccessKeyCred), []byte("access"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ObjectStoreFromEnv("", secret.File{Dir: dir}); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("err = %v; want a mode error", err)
	}
}
