package report

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
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
	_, err := s.send(ctx, http.MethodPut, s.objectPath(key), nil, contentType, data)
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

// Get returns the object at key; a missing key wraps ErrObjectNotFound.
func (s S3Store) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := s.send(ctx, http.MethodGet, s.objectPath(key), nil, "", nil)
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	return data, nil
}

// List returns the keys under prefix (relative to the store's Prefix), in key
// order, following continuation tokens.
func (s S3Store) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {strings.TrimLeft(s.Prefix+"/"+prefix, "/")}}
		if s.Prefix == "" {
			q.Set("prefix", strings.TrimLeft(prefix, "/"))
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		body, err := s.send(ctx, http.MethodGet, "/"+s.Bucket, q, "", nil)
		if err != nil {
			return nil, fmt.Errorf("s3 list %s: %w", prefix, err)
		}
		var page struct {
			Keys      []string `xml:"Contents>Key"`
			Truncated bool     `xml:"IsTruncated"`
			Next      string   `xml:"NextContinuationToken"`
		}
		if err := xml.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("s3 list %s: %w", prefix, err)
		}
		for _, k := range page.Keys {
			keys = append(keys, strings.TrimPrefix(strings.TrimPrefix(k, s.Prefix), "/"))
		}
		if !page.Truncated || page.Next == "" {
			return keys, nil
		}
		token = page.Next
	}
}

// objectPath is the bucket-relative request path of key.
func (s S3Store) objectPath(key string) string {
	objKey := strings.TrimLeft(key, "/")
	if s.Prefix != "" {
		objKey = strings.TrimLeft(s.Prefix+"/"+key, "/")
	}
	return "/" + s.Bucket + "/" + objKey
}

// send signs (SigV4) and performs one request against the endpoint at
// bucketPath (which starts with "/"+Bucket), returning the response body. A 404
// wraps ErrObjectNotFound; any other non-2xx status is an error.
func (s S3Store) send(ctx context.Context, method, bucketPath string, query url.Values, contentType string, data []byte) ([]byte, error) {
	region := s.region()
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
		return nil, fmt.Errorf("bad endpoint %q", s.Endpoint)
	}
	u := *base
	u.Path = base.Path + bucketPath
	canonURI := canonicalURI(u.Path)
	u.RawPath = canonURI
	// Canonical query: names and values URI-encoded, sorted by name.
	qnames := make([]string, 0, len(query))
	for k := range query {
		qnames = append(qnames, k)
	}
	sort.Strings(qnames)
	pairs := make([]string, 0, len(qnames))
	for _, k := range qnames {
		pairs = append(pairs, awsEscape(k)+"="+awsEscape(query.Get(k)))
	}
	canonQuery := strings.Join(pairs, "&")
	u.RawQuery = canonQuery

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
	canonReq := strings.Join([]string{method, canonURI, canonQuery, canonHdr.String(), signed, payload}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonReq))
	sig := hex.EncodeToString(hmacSHA256(s.signingKey(day, region), toSign))

	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	for name, v := range hdr {
		if name != "host" {
			req.Header.Set(name, v)
		}
	}
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", s.AccessKey, scope, signed, sig))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrObjectNotFound
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, MaxObjectBytes+1))
}

func (s S3Store) region() string {
	if s.Region == "" {
		return "us-east-1"
	}
	return s.Region
}

func (s S3Store) signingKey(day, region string) []byte {
	k := hmacSHA256([]byte("AWS4"+s.SecretKey), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	return hmacSHA256(k, "aws4_request")
}

// canonicalURI encodes each path segment once (S3 does not double-encode).
func canonicalURI(path string) string {
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		segs[i] = awsEscape(seg)
	}
	return strings.Join(segs, "/")
}

// MaxPresignTTL is the longest lifetime SigV4 allows a presigned URL.
const MaxPresignTTL = 7 * 24 * time.Hour

// PresignGet returns a SigV4 query-signed URL for GET of the object at key,
// valid for ttl from now (whole seconds, 1 to MaxPresignTTL). Only the host
// header is signed, so any client can fetch it; it grants read of that one
// object and nothing else.
func (s S3Store) PresignGet(key string, ttl time.Duration) (string, error) {
	if ttl < time.Second || ttl > MaxPresignTTL {
		return "", fmt.Errorf("presign ttl %s: want between 1s and %s", ttl, MaxPresignTTL)
	}
	base, err := url.Parse(strings.TrimRight(s.Endpoint, "/"))
	if err != nil || base.Host == "" {
		return "", fmt.Errorf("bad endpoint %q", s.Endpoint)
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	t := now().UTC()
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	region := s.region()
	scope := day + "/" + region + "/s3/aws4_request"

	query := url.Values{
		"X-Amz-Algorithm":     {"AWS4-HMAC-SHA256"},
		"X-Amz-Credential":    {s.AccessKey + "/" + scope},
		"X-Amz-Date":          {amzDate},
		"X-Amz-Expires":       {strconv.Itoa(int(ttl / time.Second))},
		"X-Amz-SignedHeaders": {"host"},
	}
	if s.SessionToken != "" {
		query.Set("X-Amz-Security-Token", s.SessionToken)
	}
	names := make([]string, 0, len(query))
	for k := range query {
		names = append(names, k)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	for _, k := range names {
		pairs = append(pairs, awsEscape(k)+"="+awsEscape(query.Get(k)))
	}
	canonQuery := strings.Join(pairs, "&")

	u := *base
	u.Path = base.Path + s.objectPath(key)
	canonURI := canonicalURI(u.Path)
	canonReq := strings.Join([]string{http.MethodGet, canonURI, canonQuery, "host:" + base.Host + "\n", "host", "UNSIGNED-PAYLOAD"}, "\n")
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonReq))
	sig := hex.EncodeToString(hmacSHA256(s.signingKey(day, region), toSign))
	u.RawPath = canonURI
	u.RawQuery = canonQuery + "&X-Amz-Signature=" + sig
	return u.String(), nil
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
