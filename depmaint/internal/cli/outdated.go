package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Fewbytes/sweatshop/depmaint/internal/enrich"
)

// Outdated lists direct dependencies in root's latest snapshot whose
// deps.dev default version differs from the resolved one. ecosystem, when
// non-empty, restricts the list to one ecosystem prefix (go, node, python,
// jvm). Dependencies whose lookup fails are reported as warnings and do not
// fail the command.
func Outdated(ctx context.Context, root, ecosystem string, pub Published, out io.Writer) error {
	s, nodes, err := openCache(ctx, root)
	if err != nil {
		return err
	}
	defer s.Close()

	found := 0
	for _, n := range nodes {
		if !n.Direct || n.Kind != "package" {
			continue
		}
		ref, err := parseNodeKey(n.Key)
		if err != nil {
			return err
		}
		system, ok := enrich.System(ref.Ecosystem)
		if !ok || (ecosystem != "" && ref.Ecosystem != ecosystem) || strings.HasPrefix(ref.Version, "(") {
			continue
		}
		var pkg enrich.PackageVersions
		err = fetchDecoded(ctx, s, pub.Now, out, enrich.SourceVersions, enrich.PackageKey(system, ref.Name),
			func(ctx context.Context) ([]byte, error) {
				return pub.DepsDev.FetchPackageVersions(ctx, system, ref.Name)
			}, &pkg)
		if err != nil {
			fmt.Fprintf(out, "warning: %s: %v\n", ref.Key, err)
			continue
		}
		if latest := pkg.Latest(); latest != "" && latest != ref.Version {
			fmt.Fprintf(out, "%s:%s %s → %s\n", ref.Ecosystem, ref.Name, ref.Version, latest)
			found++
		}
	}
	if found == 0 {
		fmt.Fprintln(out, "all direct dependencies are at the latest published version")
	}
	return nil
}
