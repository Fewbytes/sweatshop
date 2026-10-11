package report

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// S3Store is an S3-compatible object store (AWS S3, MinIO, R2). It signs
// requests with SigV4 using only the standard library and addresses objects
// path-style (Endpoint/Bucket/key), which every S3-compatible server accepts.
type S3Store struct {
	Endpoint     string // e.g. https://s3.us-east-1.amazonaws.com or http://minio:9000
	Bucket       string
	Region       string // defaults to us-east-1
	AccessKey    string
	SecretKey    string
	SessionToken string // optional
	Prefix       string // optional key prefix
	Client       *http.Client
	Now          func() time.Time
}

// NewObjectStore selects the backend from the environment. With
// SWEATSHOP_S3_BUCKET unset it returns the filesystem store rooted at
// fsRoot; otherwise an S3Store configured from:
//
//	SWEATSHOP_S3_ENDPOINT, SWEATSHOP_S3_BUCKET, SWEATSHOP_S3_REGION,
//	SWEATSHOP_S3_PREFIX, SWEATSHOP_S3_ACCESS_KEY, SWEATSHOP_S3_SECRET_KEY,
//	SWEATSHOP_S3_SESSION_TOKEN
//
// The access and secret keys fall back to AWS_ACCESS_KEY_ID,
// AWS_SECRET_ACCESS_KEY and AWS_SESSION_TOKEN.
func NewObjectStore(fsRoot string, getenv func(string) string) (ObjectStore, error) {
	bucket := getenv("SWEATSHOP_S3_BUCKET")
	if bucket == "" {
		return FSStore{Root: fsRoot}, nil
	}
	first := func(names ...string) string {
		for _, n := range names {
			if v := getenv(n); v != "" {
				return v
			}
		}
		return ""
	}
	s := S3Store{
		Endpoint:     getenv("SWEATSHOP_S3_ENDPOINT"),
		Bucket:       bucket,
		Region:       getenv("SWEATSHOP_S3_REGION"),
		Prefix:       getenv("SWEATSHOP_S3_PREFIX"),
		AccessKey:    first("SWEATSHOP_S3_ACCESS_KEY", "AWS_ACCESS_KEY_ID"),
		SecretKey:    first("SWEATSHOP_S3_SECRET_KEY", "AWS_SECRET_ACCESS_KEY"),
		SessionToken: first("SWEATSHOP_S3_SESSION_TOKEN", "AWS_SESSION_TOKEN"),
	}
	if s.Endpoint == "" || s.AccessKey == "" || s.SecretKey == "" {
		return nil, fmt.Errorf("s3 object store: SWEATSHOP_S3_ENDPOINT and access/secret keys are required when SWEATSHOP_S3_BUCKET is set")
	}
	return s, nil
}

// ObjectStoreFromEnv is NewObjectStore over the process environment.
func ObjectStoreFromEnv(fsRoot string) (ObjectStore, error) {
	return NewObjectStore(fsRoot, os.Getenv)
}

func (s S3Store) Put(ctx context.Context, key, contentType string, data []byte) error {
	region := s.Region
	if region == "" {
		region = "us-east-1"
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	base, err := url.Parse(strings.TrimRight(s.Endpoint, "/"))
	if err != nil || base.Host == "" {
		return fmt.Errorf("s3 put %s: bad endpoint %q", key, s.Endpoint)
	}
	objKey := strings.TrimLeft(s.Prefix+"/"+key, "/")
	if s.Prefix == "" {
		objKey = strings.TrimLeft(key, "/")
	}
	path := base.Path + "/" + s.Bucket + "/" + objKey
	u := *base
	u.Path = path
	// Canonical URI: each segment URI-encoded once (S3 does not double-encode).
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		segs[i] = awsEscape(seg)
	}
	canonURI := strings.Join(segs, "/")
	u.RawPath = canonURI

	t := now().UTC()
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	payload := sha256Hex(data)

	hdr := map[string]string{
		"host":                 base.Host,
		"x-amz-content-sha256": payload,
		"x-amz-date":           amzDate,
	}
	if contentType != "" {
		hdr["content-type"] = contentType
	}
	if s.SessionToken != "" {
		hdr["x-amz-security-token"] = s.SessionToken
	}
	names := make([]string, 0, len(hdr))
	for k := range hdr {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHdr strings.Builder
	for _, k := range names {
		canonHdr.WriteString(k + ":" + hdr[k] + "\n")
	}
	signed := strings.Join(names, ";")
	canonReq := strings.Join([]string{"PUT", canonURI, "", canonHdr.String(), signed, payload}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonReq))
	k := hmacSHA256([]byte("AWS4"+s.SecretKey), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	for name, v := range hdr {
		if name != "host" {
			req.Header.Set(name, v)
		}
	}
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", s.AccessKey, scope, signed, sig))
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("s3 put %s: %s: %s", key, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

func awsEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
