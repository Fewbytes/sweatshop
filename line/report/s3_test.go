package report

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
