package cli

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/Fewbytes/sweatshop/depmaint/internal/store"
)

const defaultConfig = `# depmaint configuration. Every field is optional.
#
# severity_floor: low | medium | high — drops advisories below this rank in audit
# ignore:         package names or ecosystem prefixes excluded from audit/hygiene

severity_floor: low
ignore: []
`

// Init creates the .depmaint directory, cache database, and default config.
// Re-running it never overwrites an existing config.
func Init(root string, out io.Writer) error {
	l := Paths(root)
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return err
	}

	if _, err := os.Stat(l.Config); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(l.Config, []byte(defaultConfig), 0o644); err != nil {
			return err
		}
		io.WriteString(out, "wrote "+l.Config+"\n")
	} else if err != nil {
		return err
	}

	s, err := store.Open(l.DB)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Migrate(context.Background()); err != nil {
		return err
	}
	io.WriteString(out, "initialized cache at "+l.DB+"\n")
	return nil
}
