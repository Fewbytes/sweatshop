package guard

import (
	"fmt"
	"os"
	"path/filepath"
)

const preReceive = `#!/bin/sh
# Installed by sweatshop. Only witness candidates may be pushed; branches move
# exclusively through the merge guard, which updates refs directly.
while read old new ref; do
	case "$ref" in
	refs/line/candidates/*) ;;
	*) echo "sweatshop: push to $ref rejected; submit through the witness" >&2; exit 1 ;;
	esac
done
`

// InstallHooks installs the pre-receive hook into a bare repository. It is
// idempotent and refuses to replace a hook it did not write.
func InstallHooks(repoDir string) error {
	path := filepath.Join(repoDir, "hooks", "pre-receive")
	existing, err := os.ReadFile(path)
	if err == nil {
		if string(existing) == preReceive {
			return nil
		}
		return fmt.Errorf("%s already exists with other content; refusing to overwrite", path)
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(preReceive), 0o755)
}
