// Package jvmexpander implements the depmaint JVM expander for Gradle
// projects: it reads gradle.lockfile (dependency locking) directly instead
// of running `gradle dependencies`, which needs a JVM, a daemon and network
// access. Read-only.
//
// gradle.lockfile is a flat list of resolved coordinates: it records no
// requirement edges, no direct/transitive distinction and no project
// dependencies. Nodes are therefore emitted with Direct=false and no edges
// are produced, so JVM roots never contribute internal edges to project
// derivation. Maven has no lockfile and is not covered.
package jvmexpander

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
)

func init() {
	expander.Register(Expander{})
}

// Expander implements expander.Expander for Gradle-locked JVM projects.
type Expander struct{}

func (Expander) Name() string { return "jvm" }

// Available reports whether a JVM is installed. Expand only reads a static
// file, so it works regardless; this is reported for the Expander contract.
func (Expander) Available() bool {
	_, err := exec.LookPath("java")
	return err == nil
}

// Expand reads root's resolved coordinates from gradle.lockfile. A root with
// no gradle.lockfile contributes no nodes (nil, nil, nil). A malformed
// coordinate line is an error.
func (Expander) Expand(root string) ([]expander.Node, []expander.Edge, error) {
	f, err := os.Open(filepath.Join(root, "gradle.lockfile"))
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading gradle.lockfile: %w", err)
	}
	defer f.Close()

	var nodes []expander.Node
	seen := make(map[string]bool)
	sc := bufio.NewScanner(f)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "empty=") {
			continue
		}
		coords, _, _ := strings.Cut(line, "=")
		parts := strings.Split(coords, ":")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return nil, nil, fmt.Errorf("gradle.lockfile:%d: want group:artifact:version=configurations, got %q", lineNo, line)
		}
		key := "jvm:" + parts[0] + ":" + parts[1] + "@" + parts[2]
		if seen[key] {
			continue
		}
		seen[key] = true
		nodes = append(nodes, expander.Node{Key: key, Kind: "package"})
	}
	if err := sc.Err(); err != nil {
		return nil, nil, fmt.Errorf("reading gradle.lockfile: %w", err)
	}
	return nodes, nil, nil
}
