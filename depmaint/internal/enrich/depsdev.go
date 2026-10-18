package enrich

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// DefaultDepsDevURL is the public deps.dev API root (no auth).
const DefaultDepsDevURL = "https://api.deps.dev"

// DepsDev reads published truth from deps.dev (which fronts OSV advisories).
// Each method returns raw JSON, ready for Fetch to cache, plus a decoder.
type DepsDev struct {
	BaseURL string // DefaultDepsDevURL unless a test points it elsewhere
	HTTP    *http.Client
}

// PackageVersion is one published version in a PackageVersions response.
type PackageVersion struct {
	VersionKey struct {
		Version string `json:"version"`
	} `json:"versionKey"`
	PublishedAt string `json:"publishedAt"`
	// IsDefault marks the version deps.dev considers current (latest stable).
	IsDefault bool `json:"isDefault"`
}

// PackageVersions is the decoded deps.dev package document.
type PackageVersions struct {
	Versions []PackageVersion `json:"versions"`
}

// VersionDetail is the decoded deps.dev document for one package version.
type VersionDetail struct {
	Licenses     []string `json:"licenses"`
	IsDeprecated bool     `json:"isDeprecated"`
	AdvisoryKeys []struct {
		ID string `json:"id"`
	} `json:"advisoryKeys"`
}

// Requirements is the decoded requirements document. Only npm is modelled:
// other ecosystems carry their requirements under differently named keys.
type Requirements struct {
	NPM struct {
		Dependencies []struct {
			Name        string `json:"name"`
			Requirement string `json:"requirement"`
		} `json:"dependencies"`
	} `json:"npm"`
}

// Advisory is the decoded deps.dev advisory document.
type Advisory struct {
	AdvisoryKey struct {
		ID string `json:"id"`
	} `json:"advisoryKey"`
	Title      string  `json:"title"`
	CVSS3Score float64 `json:"cvss3Score"`
}

// Severity buckets the CVSS v3 score into the config's floor vocabulary:
// >= 7 high, >= 4 medium, otherwise low.
func (a Advisory) Severity() Severity {
	switch {
	case a.CVSS3Score >= 7:
		return SeverityHigh
	case a.CVSS3Score >= 4:
		return SeverityMedium
	}
	return SeverityLow
}

// Severity ranks advisories; the zero value is invalid on purpose.
type Severity int

const (
	SeverityLow Severity = iota + 1
	SeverityMedium
	SeverityHigh
)

func (s Severity) String() string {
	switch s {
	case SeverityLow:
		return "low"
	case SeverityMedium:
		return "medium"
	case SeverityHigh:
		return "high"
	}
	return fmt.Sprintf("severity(%d)", int(s))
}

// ParseSeverity parses config's severity_floor vocabulary.
func ParseSeverity(s string) (Severity, error) {
	switch s {
	case "low":
		return SeverityLow, nil
	case "medium":
		return SeverityMedium, nil
	case "high":
		return SeverityHigh, nil
	}
	return 0, fmt.Errorf("unknown severity %q (want low, medium, or high)", s)
}

// System maps a depmaint node-key ecosystem prefix to the deps.dev system.
func System(ecosystem string) (string, bool) {
	switch ecosystem {
	case "go":
		return "GO", true
	case "node":
		return "NPM", true
	case "python":
		return "PYPI", true
	case "jvm":
		return "MAVEN", true
	}
	return "", false
}

// PackageKey is the cache key for a package's version list.
func PackageKey(system, name string) string { return system + "/" + name }

// VersionKey is the cache key for one package version.
func VersionKey(system, name, version string) string { return system + "/" + name + "@" + version }

func (c DepsDev) get(ctx context.Context, path string) ([]byte, error) {
	return httpGet(ctx, c.HTTP, strings.TrimSuffix(c.BaseURL, "/")+path)
}

// FetchPackageVersions returns the raw package document.
func (c DepsDev) FetchPackageVersions(ctx context.Context, system, name string) ([]byte, error) {
	return c.get(ctx, "/v3/systems/"+system+"/packages/"+url.PathEscape(name))
}

// FetchVersion returns the raw version document.
func (c DepsDev) FetchVersion(ctx context.Context, system, name, version string) ([]byte, error) {
	return c.get(ctx, "/v3/systems/"+system+"/packages/"+url.PathEscape(name)+"/versions/"+url.PathEscape(version))
}

// FetchRequirements returns the raw declared-requirements document for one
// package version. Only deps.dev's v3alpha API serves requirements.
func (c DepsDev) FetchRequirements(ctx context.Context, system, name, version string) ([]byte, error) {
	return c.get(ctx, "/v3alpha/systems/"+system+"/packages/"+url.PathEscape(name)+"/versions/"+url.PathEscape(version)+":requirements")
}

// FetchAdvisory returns the raw advisory document.
func (c DepsDev) FetchAdvisory(ctx context.Context, id string) ([]byte, error) {
	return c.get(ctx, "/v3/advisories/"+url.PathEscape(id))
}

// Latest returns the default (latest stable) version, or "" if none is marked.
func (p PackageVersions) Latest() string {
	for _, v := range p.Versions {
		if v.IsDefault {
			return v.VersionKey.Version
		}
	}
	return ""
}

// Decode unmarshals a cached payload into v.
func Decode(payload []byte, v any) error {
	return json.Unmarshal(payload, v)
}
