package station

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/report"
)

func TestViceroyReadsReportsBucketButCannotWriteIt(t *testing.T) {
	r := newKaizenRig(t)
	r.srv.Stations = append(r.srv.Stations, &Station{Name: "v", Role: viceroyRole, Token: "vt"})
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(r.reportsDir, "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"runs/a.xml": "<a/>", "runs/b.xml": "<b/>", "other.txt": "o"} {
		if err := os.WriteFile(filepath.Join(r.reportsDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	viceroy := Client{URL: r.heijunka.URL, Token: "vt"}

	keys, err := viceroy.ListReports(ctx, "runs/")
	if err != nil || len(keys) != 2 || keys[0] != "runs/a.xml" || keys[1] != "runs/b.xml" {
		t.Fatalf("list = %v, %v", keys, err)
	}
	got, err := viceroy.GetReport(ctx, "runs/b.xml")
	if err != nil || string(got) != "<b/>" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if _, err := viceroy.GetReport(ctx, "../etc/passwd"); err == nil {
		t.Fatal("get outside the bucket succeeded")
	}
	if _, err := viceroy.GetReport(ctx, "missing"); err == nil {
		t.Fatal("get of a missing key succeeded")
	}

	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		req, _ := http.NewRequestWithContext(ctx, method, r.heijunka.URL+"/v1/reports/object?key=runs/a.xml", nil)
		req.Header.Set("Authorization", "Bearer vt")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s status %d, want 405", method, resp.StatusCode)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(r.reportsDir, "runs/a.xml")); string(b) != "<a/>" {
		t.Fatalf("object changed: %q", b)
	}
}

func TestOtherRolesCannotReadReportsBucket(t *testing.T) {
	r := newKaizenRig(t)
	for name, c := range map[string]Client{"maker": r.mkr, "kaizen": r.kaizen, "heijunka": r.heijunka} {
		if _, err := c.ListReports(context.Background(), ""); err == nil {
			t.Errorf("%s listed the reports bucket", name)
		}
	}
}

func TestViceroyPresignsKaizenReportsWithinTheConstitutionCap(t *testing.T) {
	r := newKaizenRig(t)
	r.srv.Stations = append(r.srv.Stations, &Station{Name: "v", Role: viceroyRole, Token: "vt"})
	r.srv.Reports = report.S3Store{Endpoint: "https://s3.example.com", Bucket: "reports", AccessKey: "AK", SecretKey: "SK"}
	r.srv.Guard.Constitution.Viceroy.PresignMaxTTL = "30m"
	ctx := context.Background()
	viceroy := Client{URL: r.heijunka.URL, Token: "vt"}

	got, err := viceroy.PresignReport(ctx, KaizenAnalysisKey("run1"), 10*time.Minute)
	if err != nil || got.TTL != "10m0s" || !strings.Contains(got.URL, "/reports/kaizen/run1/analysis.md?") ||
		!strings.Contains(got.URL, "X-Amz-Expires=600") {
		t.Fatalf("presign = %+v, %v", got, err)
	}
	got, err = viceroy.PresignReport(ctx, KaizenSignalsPrefix("abc")+"/signals.json", 0)
	if err != nil || got.TTL != "30m0s" || !strings.Contains(got.URL, "X-Amz-Expires=1800") {
		t.Fatalf("default ttl presign = %+v, %v", got, err)
	}
	for name, tc := range map[string]struct {
		key string
		ttl time.Duration
	}{
		"ttl over the cap":    {KaizenAnalysisKey("run1"), 31 * time.Minute},
		"not a kaizen report": {"reports/abc/junit.xml", time.Minute},
		"dot-dot in the key":  {"kaizen/../reports/abc/junit.xml", time.Minute},
		"empty key":           {"", time.Minute},
		"sub-second ttl":      {KaizenAnalysisKey("run1"), time.Millisecond},
	} {
		if _, err := viceroy.PresignReport(ctx, tc.key, tc.ttl); err == nil {
			t.Errorf("%s: presign succeeded", name)
		}
	}
}

func TestPresignNeedsAViceroyAndAPresigningStore(t *testing.T) {
	r := newKaizenRig(t)
	r.srv.Stations = append(r.srv.Stations, &Station{Name: "v", Role: viceroyRole, Token: "vt"})
	ctx := context.Background()
	if _, err := r.mkr.PresignReport(ctx, KaizenAnalysisKey("run1"), time.Minute); err == nil {
		t.Error("maker presigned a report")
	}
	viceroy := Client{URL: r.heijunka.URL, Token: "vt"}
	if _, err := viceroy.PresignReport(ctx, KaizenAnalysisKey("run1"), time.Minute); err == nil {
		t.Error("filesystem store presigned a report")
	}
}
