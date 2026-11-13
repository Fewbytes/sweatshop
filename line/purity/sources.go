package purity

// Source is a kind of side effect or hidden input that makes a function's
// result depend on more than its arguments.
type Source string

const (
	SourceClock       Source = "clock"
	SourceRand        Source = "rand"
	SourceEnv         Source = "env"
	SourceFS          Source = "fs"
	SourceNet         Source = "net"
	SourceProcess     Source = "process"
	SourceStdio       Source = "stdio"
	SourceGlobal      Source = "global"
	SourceArgMutation Source = "arg-mutation"
)

// effectfulImports taints every call into the package, by import path. It is
// coarse on purpose (net/http.StatusText is pure) so the table stays short;
// a precise entry belongs in effectfulCalls.
var effectfulImports = map[string]Source{
	"math/rand":    SourceRand,
	"math/rand/v2": SourceRand,
	"crypto/rand":  SourceRand,
	"net":          SourceNet,
	"net/http":     SourceNet,
	"net/rpc":      SourceNet,
	"net/smtp":     SourceNet,
	"database/sql": SourceNet,
	"os/exec":      SourceProcess,
	"os/signal":    SourceProcess,
	"io/ioutil":    SourceFS,
	"log":          SourceStdio,
	"log/slog":     SourceStdio,
}

// effectfulCalls taints calls to one function, keyed "importpath.Func".
var effectfulCalls = map[string]Source{
	"time.Now":       SourceClock,
	"time.Since":     SourceClock,
	"time.Until":     SourceClock,
	"time.Sleep":     SourceClock,
	"time.After":     SourceClock,
	"time.AfterFunc": SourceClock,
	"time.Tick":      SourceClock,
	"time.NewTimer":  SourceClock,
	"time.NewTicker": SourceClock,

	"os.Getenv":    SourceEnv,
	"os.LookupEnv": SourceEnv,
	"os.Environ":   SourceEnv,
	"os.ExpandEnv": SourceEnv,
	"os.Setenv":    SourceEnv,
	"os.Unsetenv":  SourceEnv,
	"os.Clearenv":  SourceEnv,

	"os.Exit":    SourceProcess,
	"os.Getpid":  SourceProcess,
	"os.Getppid": SourceProcess,
	"os.Getuid":  SourceProcess,
	"os.Getgid":  SourceProcess,

	"fmt.Print":   SourceStdio,
	"fmt.Printf":  SourceStdio,
	"fmt.Println": SourceStdio,
}

// osPureFunctions are the os functions that only inspect their argument; every
// other call into os is a filesystem effect unless effectfulCalls says more.
var osPureFunctions = map[string]bool{
	"os.IsNotExist":      true,
	"os.IsExist":         true,
	"os.IsPermission":    true,
	"os.IsTimeout":       true,
	"os.IsPathSeparator": true,
	"os.NewSyscallError": true,
	"os.SameFile":        true,
}

// externalSource classifies a call to function name in the package with the
// given import path outside the analysed modules. It reports false for calls
// assumed pure.
func externalSource(importPath, name string) (Source, bool) {
	qualified := importPath + "." + name
	if src, ok := effectfulCalls[qualified]; ok {
		return src, true
	}
	if src, ok := effectfulImports[importPath]; ok {
		return src, true
	}
	if importPath == "os" && !osPureFunctions[qualified] {
		return SourceFS, true
	}
	return "", false
}
