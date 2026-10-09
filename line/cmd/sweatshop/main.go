// Command sweatshop administers an assembly line: witness keys and the merge
// guard's repository hook.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/linedir"
)

const usage = `usage:
  sweatshop keygen PATH                 generate a witness signing key (PATH, PATH.pub)
  sweatshop guard install-hooks REPO    install the merge guard's pre-receive hook in a bare repo
  sweatshop line init --dir D --source S [--engine E] [--image I] [--port P]
                                        create a line directory seeded from S's master`

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
	case len(args) >= 2 && args[0] == "line" && args[1] == "init":
		return lineInit(args[2:], stdout)
	default:
		return errors.New(usage)
	}
}

func lineInit(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line init", flag.ContinueOnError)
	var opts linedir.InitOptions
	fs.StringVar(&opts.Dir, "dir", "", "line directory (must not exist or be empty)")
	fs.StringVar(&opts.Source, "source", "", "git repository whose master seeds the line")
	fs.StringVar(&opts.Engine, "engine", "", "container engine (default docker)")
	fs.StringVar(&opts.Image, "image", "", "station image (default sweatshop-station:dev)")
	fs.IntVar(&opts.Port, "port", 0, "station API port (default 7468)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || opts.Dir == "" || opts.Source == "" {
		return errors.New(usage)
	}
	l, err := linedir.Init(context.Background(), opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "line %s\nport %d\n", l.Dir, l.Port)
	for _, s := range l.Stations {
		pub, err := os.ReadFile(s.KeyFile + ".pub")
		if err != nil {
			return err
		}
		key, err := attest.ParsePublicKey(strings.TrimSpace(string(pub)))
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "station %s role %s key_id %s\n", s.Name, s.Role, attest.KeyID(key))
	}
	return nil
}
