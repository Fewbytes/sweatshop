package constitution

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
)

const (
	// RemoteAuthoritative is the line remote: the only source of truth that
	// agents read from and land to.
	RemoteAuthoritative = "authoritative"
	// RemoteStorage is a write-only, fast-forward-only mirror used to publish.
	// The line never reads a code source from it and never relies on the
	// host's policies or features (branch protection, PRs, Actions, webhooks).
	RemoteStorage = "storage"
)

// DefaultPublishRefs are the ref patterns pushed to a storage mirror when its
// Publish list is empty: master, tags and attestations.
var DefaultPublishRefs = []string{"refs/heads/master", "refs/tags/*", "refs/sweatshop/*"}

// Remote declares one place the line keeps code. Push credentials for storage
// mirrors live on the host only, never in the constitution or a station.
type Remote struct {
	Name string `toml:"name"`
	Kind string `toml:"kind"`
	// URL is where a storage mirror is pushed to. The authoritative remote
	// has none: the line's own remote.git is located by line.toml.
	URL string `toml:"url,omitempty"`
	// Publish lists ref patterns pushed to a storage mirror; empty means
	// DefaultPublishRefs.
	Publish []string `toml:"publish,omitempty"`
}

// PublishRefs is the ref patterns to push to this mirror, with the default applied.
func (r Remote) PublishRefs() []string {
	if len(r.Publish) == 0 {
		return DefaultPublishRefs
	}
	return r.Publish
}

// AuthoritativeRemote is the line remote. ok is false when the constitution
// declares no remotes, in which case the line uses its own remote.git.
func (c Constitution) AuthoritativeRemote() (Remote, bool) {
	for _, r := range c.Remotes {
		if r.Kind == RemoteAuthoritative {
			return r, true
		}
	}
	return Remote{}, false
}

// StorageMirrors are the declared storage remotes, in declaration order.
func (c Constitution) StorageMirrors() []Remote {
	var mirrors []Remote
	for _, r := range c.Remotes {
		if r.Kind == RemoteStorage {
			mirrors = append(mirrors, r)
		}
	}
	return mirrors
}

// validateRemotes allows no remotes at all; otherwise it demands exactly one
// authoritative remote. The list shape leaves room for several authoritative
// remotes later; v1 implements one.
func (c Constitution) validateRemotes() error {
	if len(c.Remotes) == 0 {
		return nil
	}
	names := map[string]bool{}
	authoritative := 0
	for i, r := range c.Remotes {
		if r.Name == "" {
			return fmt.Errorf("remotes[%d]: missing name", i)
		}
		if names[r.Name] {
			return fmt.Errorf("remotes[%d]: duplicate name %q", i, r.Name)
		}
		names[r.Name] = true
		switch r.Kind {
		case RemoteAuthoritative:
			authoritative++
			if r.URL != "" || len(r.Publish) > 0 {
				return fmt.Errorf("remotes[%d] %q: the authoritative remote takes no url or publish", i, r.Name)
			}
		case RemoteStorage:
			if r.URL == "" {
				return fmt.Errorf("remotes[%d] %q: a storage remote needs a url", i, r.Name)
			}
			if u, err := url.Parse(r.URL); err == nil && u.User != nil {
				if _, hasPassword := u.User.Password(); hasPassword {
					return fmt.Errorf("remotes[%d] %q: url must not embed credentials; they live on the host", i, r.Name)
				}
			}
			if slices.Contains(r.Publish, "") || slices.ContainsFunc(r.Publish, func(p string) bool { return !strings.HasPrefix(p, "refs/") }) {
				return fmt.Errorf("remotes[%d] %q: publish entries must be ref patterns starting with refs/", i, r.Name)
			}
		default:
			return fmt.Errorf("remotes[%d] %q: kind %q: want %s or %s", i, r.Name, r.Kind, RemoteAuthoritative, RemoteStorage)
		}
	}
	if authoritative != 1 {
		return fmt.Errorf("remotes: want exactly one authoritative remote, got %d (several are not implemented yet)", authoritative)
	}
	return nil
}
