package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/depmaint/internal/enrich"
	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
	"github.com/Fewbytes/sweatshop/depmaint/internal/snapshot"
	"github.com/Fewbytes/sweatshop/depmaint/internal/store"
)

// publishedWorld is a fake deps.dev + endoflife.date plus a fake clock.
type publishedWorld struct {
	t      *testing.T
	root   string
	server *httptest.Server
	hits   atomic.Int64
	now    time.Time
	routes map[string]string // URL path (escaped) -> JSON body
}

func newPublishedWorld(t *testing.T, routes map[string]string, nodes ...expander.Node) *publishedWorld {
	t.Helper()
	w := &publishedWorld{t: t, root: t.TempDir(), now: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC), routes: routes}
	w.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.hits.Add(1)
		body, ok := w.routes[r.URL.EscapedPath()]
		if !ok {
			http.NotFound(rw, r)
			return
		}
		rw.Write([]byte(body))
	}))
	t.Cleanup(w.server.Close)

	if err := Init(w.root, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(Paths(w.root).DB)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := snapshot.Snapshot{ID: "snap-1", Root: w.root, CreatedAt: w.now.Format(time.RFC3339), Expanders: "fake", NodeCount: len(nodes)}
	if err := s.InsertSnapshot(context.Background(), snap, nodes, nil); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *publishedWorld) published() Published {
	return Published{
		DepsDev:   enrich.DepsDev{BaseURL: w.server.URL, HTTP: w.server.Client()},
		EndOfLife: enrich.EndOfLife{BaseURL: w.server.URL, HTTP: w.server.Client()},
		Now:       func() time.Time { return w.now },
	}
}

func (w *publishedWorld) outdated(ecosystem string) string {
	w.t.Helper()
	var out bytes.Buffer
	if err := Outdated(context.Background(), w.root, ecosystem, w.published(), &out); err != nil {
		w.t.Fatalf("Outdated: %v", err)
	}
	return out.String()
}

func (w *publishedWorld) audit(minSeverity string) string {
	w.t.Helper()
	var out bytes.Buffer
	if err := Audit(context.Background(), w.root, minSeverity, w.published(), &out); err != nil {
		w.t.Fatalf("Audit: %v", err)
	}
	return out.String()
}

func requireContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("output missing %q:\n%s", want, got)
	}
}

func requireAbsent(t *testing.T, got, unwanted string) {
	t.Helper()
	if strings.Contains(got, unwanted) {
		t.Errorf("output should not contain %q:\n%s", unwanted, got)
	}
}

const zapVersions = `{"versions":[
 {"versionKey":{"version":"1.24.0"}},
 {"versionKey":{"version":"1.27.0"},"isDefault":true}]}`

func outdatedNodes() []expander.Node {
	return []expander.Node{
		{Key: "go:go.uber.org/zap@1.24.0", Kind: "package", Direct: true},
		{Key: "go:example.com/current@2.0.0", Kind: "package", Direct: true},
		{Key: "go:example.com/transitive@1.0.0", Kind: "package", Direct: false},
		{Key: "node:@scope/left@1.0.0", Kind: "package", Direct: true},
	}
}

func outdatedRoutes() map[string]string {
	return map[string]string{
		"/v3/systems/GO/packages/go.uber.org%2Fzap":     zapVersions,
		"/v3/systems/GO/packages/example.com%2Fcurrent": `{"versions":[{"versionKey":{"version":"2.0.0"},"isDefault":true}]}`,
		"/v3/systems/NPM/packages/@scope%2Fleft":        `{"versions":[{"versionKey":{"version":"1.1.0"},"isDefault":true}]}`,
	}
}

func TestOutdatedListsOnlyDirectDepsBehindLatest(t *testing.T) {
	w := newPublishedWorld(t, outdatedRoutes(), outdatedNodes()...)

	got := w.outdated("")

	requireContains(t, got, "go:go.uber.org/zap 1.24.0 → 1.27.0")
	requireContains(t, got, "node:@scope/left 1.0.0 → 1.1.0")
	requireAbsent(t, got, "example.com/current")
	requireAbsent(t, got, "transitive")
}

func TestOutdatedEcosystemFilterRestrictsList(t *testing.T) {
	w := newPublishedWorld(t, outdatedRoutes(), outdatedNodes()...)

	got := w.outdated("node")

	requireContains(t, got, "node:@scope/left")
	requireAbsent(t, got, "zap")
}

func TestOutdatedServesFreshCacheThenRefetchesAfterTTL(t *testing.T) {
	w := newPublishedWorld(t, outdatedRoutes(), outdatedNodes()...)
	w.outdated("")
	afterFirst := w.hits.Load()

	w.outdated("")
	if w.hits.Load() != afterFirst {
		t.Fatalf("second run within TTL hit the network: %d -> %d requests", afterFirst, w.hits.Load())
	}

	w.now = w.now.Add(enrich.SourceVersions.TTL() + time.Minute)
	w.outdated("")
	if w.hits.Load() == afterFirst {
		t.Fatal("run past the versions TTL did not refetch")
	}
}

func TestOutdatedOfflineServesStaleCopyAndSaysSo(t *testing.T) {
	w := newPublishedWorld(t, outdatedRoutes(), outdatedNodes()...)
	w.outdated("")
	w.server.Close()
	w.now = w.now.Add(enrich.SourceVersions.TTL() + time.Hour)

	got := w.outdated("go")

	requireContains(t, got, "go:go.uber.org/zap 1.24.0 → 1.27.0")
	requireContains(t, got, "note: depsdev-versions GO/go.uber.org/zap served from cache fetched 2026-10-18T12:00:00Z (refresh failed")
}

func TestOutdatedWarnsAndContinuesWhenLookupFailsWithNoCache(t *testing.T) {
	routes := outdatedRoutes()
	delete(routes, "/v3/systems/GO/packages/go.uber.org%2Fzap")
	w := newPublishedWorld(t, routes, outdatedNodes()...)

	got := w.outdated("")

	requireContains(t, got, "warning: go:go.uber.org/zap@1.24.0")
	requireContains(t, got, "node:@scope/left 1.0.0 → 1.1.0")
}

func auditNodes() []expander.Node {
	return []expander.Node{
		{Key: "go:example.com/vuln@1.0.0", Kind: "package", Direct: true},
		{Key: "go:example.com/old@1.0.0", Kind: "package", Direct: false},
		{Key: "go:example.com/clean@1.0.0", Kind: "package", Direct: true},
		{Key: "go:example.com/ignored@1.0.0", Kind: "package", Direct: true},
		{Key: "runtime:go@1.18.2", Kind: "runtime", Direct: true},
		{Key: "runtime:nodejs@22.1.0", Kind: "runtime", Direct: true},
	}
}

func auditRoutes() map[string]string {
	return map[string]string{
		"/v3/systems/GO/packages/example.com%2Fvuln/versions/1.0.0":    `{"advisoryKeys":[{"id":"GHSA-high"},{"id":"GHSA-low"}]}`,
		"/v3/systems/GO/packages/example.com%2Fold/versions/1.0.0":     `{"isDeprecated":true}`,
		"/v3/systems/GO/packages/example.com%2Fclean/versions/1.0.0":   `{"licenses":["MIT"]}`,
		"/v3/systems/GO/packages/example.com%2Fignored/versions/1.0.0": `{"advisoryKeys":[{"id":"GHSA-high"}]}`,
		"/v3/advisories/GHSA-high":                                     `{"title":"RCE in parser","cvss3Score":9.8}`,
		"/v3/advisories/GHSA-low":                                      `{"title":"Minor info leak","cvss3Score":2.1}`,
		"/api/go.json":                                                 `[{"cycle":"1.18","eol":"2023-02-01"},{"cycle":"1.23","eol":false}]`,
		"/api/nodejs.json":                                             `[{"cycle":"22","eol":"2027-04-30"}]`,
	}
}

func TestAuditJoinsAdvisoriesDeprecationAndEOLIntoWorklist(t *testing.T) {
	w := newPublishedWorld(t, auditRoutes(), auditNodes()...)
	os.WriteFile(Paths(w.root).Config, []byte("ignore = [\"example.com/ignored\"]\n"), 0o644)

	got := w.audit("")

	requireContains(t, got, "[high] go:example.com/vuln@1.0.0 — advisory GHSA-high: RCE in parser → suggest: upgrade campaign")
	requireContains(t, got, "[low] go:example.com/vuln@1.0.0 — advisory GHSA-low: Minor info leak")
	requireContains(t, got, "[high] go:example.com/old@1.0.0 — deprecated → suggest: upgrade or remove campaign")
	requireContains(t, got, "[high] runtime:go@1.18.2 — end of life (cycle 1.18) → suggest: runtime campaign")
	requireAbsent(t, got, "clean")
	requireAbsent(t, got, "ignored")
	requireAbsent(t, got, "nodejs")
	if strings.Index(got, "[high]") > strings.Index(got, "[low]") {
		t.Errorf("worklist must list high severity before low:\n%s", got)
	}
}

func TestAuditMinSeverityDropsLowerAdvisories(t *testing.T) {
	w := newPublishedWorld(t, auditRoutes(), auditNodes()...)

	got := w.audit("high")

	requireContains(t, got, "GHSA-high")
	requireAbsent(t, got, "GHSA-low")
}

func TestAuditRejectsUnknownSeverity(t *testing.T) {
	w := newPublishedWorld(t, auditRoutes(), auditNodes()...)
	var out bytes.Buffer

	err := Audit(context.Background(), w.root, "urgent", w.published(), &out)

	if err == nil || !strings.Contains(err.Error(), `unknown severity "urgent"`) {
		t.Fatalf("want unknown-severity error, got %v", err)
	}
}

func TestAuditReportsCleanWhenNothingToAct(t *testing.T) {
	w := newPublishedWorld(t, auditRoutes(), expander.Node{Key: "go:example.com/clean@1.0.0", Kind: "package", Direct: true})

	requireContains(t, w.audit(""), "audit clean")
}

func TestAuditRequiresSnapshot(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer

	err := Audit(context.Background(), root, "", DefaultPublished(), &out)

	if err == nil || !strings.Contains(err.Error(), "run `depmaint graph` first") {
		t.Fatalf("want no-snapshot error, got %v", err)
	}
}
