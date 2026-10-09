// Command sweatshop administers an assembly line: witness keys and the merge
// guard's repository hook.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/guard"
)

const usage = `usage:
  sweatshop keygen PATH                 generate a witness signing key (PATH, PATH.pub)
  sweatshop guard install-hooks REPO    install the merge guard's pre-receive hook in a bare repo`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sweatshop:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	switch {
	case len(args) == 2 && args[0] == "keygen":
		pub, err := attest.GenerateKeyFile(args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "key_id %s\npublic_key %s\n", attest.KeyID(pub), attest.EncodePublicKey(pub))
		return nil
	case len(args) == 3 && args[0] == "guard" && args[1] == "install-hooks":
		return guard.InstallHooks(args[2])
	default:
		return errors.New(usage)
	}
}
