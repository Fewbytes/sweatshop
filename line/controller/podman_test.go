package controller

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/client"
)

const testImage = "localhost/check@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// fakeSocket speaks enough of the Docker-compatible API for Podman and
// records the create request and removals.
type fakeSocket struct {
	mu        sync.Mutex
	imageHere bool
	pulled    bool
	create    map[string]any
	deleted   []string
}

func (s *fakeSocket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/images/"+testImage+"/json"):
		if !s.imageHere {
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
			return
		}
		io.WriteString(w, `{}`)
	case strings.HasSuffix(r.URL.Path, "/images/create"):
		s.pulled = true
		io.WriteString(w, `{"status":"done"}`)
	case strings.HasSuffix(r.URL.Path, "/containers/create"):
		json.NewDecoder(r.Body).Decode(&s.create)
		s.create["name"] = r.URL.Query().Get("name")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"Id":"c1"}`)
	case strings.HasSuffix(r.URL.Path, "/containers/c1/start"):
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(r.URL.Path, "/containers/json"):
		if got := r.URL.Query().Get("filters"); !strings.Contains(got, "sweatshop.line=demo") {
			http.Error(w, "unscoped list: "+got, http.StatusBadRequest)
			return
		}
		io.WriteString(w, `[{"Id":"run","Created":1000,"Labels":{"sweatshop.workload":"wl-1"}},{"Id":"done","Created":1,"Labels":{"sweatshop.workload":"wl-2"}}]`)
	case strings.HasSuffix(r.URL.Path, "/containers/run/json"):
		io.WriteString(w, `{"State":{"Running":true}}`)
	case strings.HasSuffix(r.URL.Path, "/containers/done/json"):
		io.WriteString(w, `{"State":{"Running":false,"ExitCode":7,"FinishedAt":"2026-10-21T12:00:00.5Z"}}`)
	case r.Method == http.MethodDelete:
		s.deleted = append(s.deleted, r.URL.Path)
		if strings.Contains(r.URL.Path, "wl-gone") {
			http.Error(w, `{"message":"no such container"}`, http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(r.URL.Path, "/logs"):
		// one stdout frame (stream 1) holding "hello\n"
		w.Write([]byte{1, 0, 0, 0, 0, 0, 0, 6})
		io.WriteString(w, "hello\n")
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
	}
}

func newPodman(t *testing.T, s *fakeSocket) Podman {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	c, err := client.NewClientWithOpts(client.WithHost("tcp://"+srv.Listener.Addr().String()), client.WithVersion("1.41"))
	if err != nil {
		t.Fatal(err)
	}
	return Podman{Client: c, Line: "demo", Network: "line-demo"}
}

func TestPodmanCreatePullsAndAppliesTheHardLimits(t *testing.T) {
	s := &fakeSocket{}
	p := newPodman(t, s)
	err := p.Create(t.Context(), Instance{Workload: "wl-1", Image: testImage,
		Args: []string{"just", "ci"}, Env: map[string]string{"B": "2", "A": "1"}, Memory: "1g", CPUs: "1.5", Pids: 64})
	if err != nil {
		t.Fatal(err)
	}
	if !s.pulled {
		t.Error("a missing image was not pulled")
	}
	cfg := s.create
	host := cfg["HostConfig"].(map[string]any)
	labels := cfg["Labels"].(map[string]any)
	checks := map[string]bool{
		"name":         cfg["name"] == "sweatshop-demo-wl-1",
		"user":         cfg["User"] == "65534:65534",
		"labels":       labels["sweatshop.line"] == "demo" && labels["sweatshop.workload"] == "wl-1",
		"env":          strings.Join(toStrings(cfg["Env"]), ",") == "A=1,B=2",
		"cmd":          strings.Join(toStrings(cfg["Cmd"]), " ") == "just ci",
		"cap-drop":     strings.Join(toStrings(host["CapDrop"]), ",") == "ALL",
		"no-new-priv":  strings.Join(toStrings(host["SecurityOpt"]), ",") == "no-new-privileges",
		"network":      host["NetworkMode"] == "line-demo",
		"memory":       host["Memory"] == float64(1<<30),
		"cpus":         host["NanoCpus"] == float64(1.5e9),
		"pids":         host["PidsLimit"] == float64(64),
		"unprivileged": host["Privileged"] == false,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("create request check %q failed: %v", name, s.create)
		}
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestPodmanListReportsStateOfOnlyThisLinesContainers(t *testing.T) {
	p := newPodman(t, &fakeSocket{})
	got, err := p.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Workload != "wl-1" || !got[0].Running || got[0].Since.Unix() != 1000 ||
		got[1].Workload != "wl-2" || got[1].Running || got[1].ExitCode != 7 || got[1].Since.Unix() != 1792584000 {
		t.Fatalf("list = %+v", got)
	}
}

func TestPodmanLogsDecodesTheStream(t *testing.T) {
	logs, err := newPodman(t, &fakeSocket{}).Logs(t.Context(), "wl-1")
	if err != nil || logs != "hello\n" {
		t.Fatalf("logs = %q, %v; want hello", logs, err)
	}
}

func TestPodmanRemoveToleratesAnObjectAlreadyGone(t *testing.T) {
	s := &fakeSocket{}
	p := newPodman(t, s)
	if err := p.Remove(t.Context(), "wl-gone"); err != nil {
		t.Fatalf("removing a missing container failed: %v", err)
	}
	if len(s.deleted) != 1 || !strings.Contains(s.deleted[0], "sweatshop-demo-wl-gone") {
		t.Fatalf("deleted %v", s.deleted)
	}
}
