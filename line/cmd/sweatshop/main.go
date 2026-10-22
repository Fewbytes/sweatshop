// Command sweatshop administers an assembly line (witness keys, the merge
// guard's hook, line directories, the station API) and is the station
// client agents use from inside their containers.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	dockerclient "github.com/docker/docker/client"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Fewbytes/sweatshop/line/andon"
	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/cistats"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/controller"
	"github.com/Fewbytes/sweatshop/line/deploy"
	"github.com/Fewbytes/sweatshop/line/gemba"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/heijunka"
	"github.com/Fewbytes/sweatshop/line/kaizen"
	"github.com/Fewbytes/sweatshop/line/linedir"
	"github.com/Fewbytes/sweatshop/line/linelog"
	"github.com/Fewbytes/sweatshop/line/linestore"
	"github.com/Fewbytes/sweatshop/line/maker"
	"github.com/Fewbytes/sweatshop/line/mirror"
	"github.com/Fewbytes/sweatshop/line/quadlet"
	"github.com/Fewbytes/sweatshop/line/secret"
	"github.com/Fewbytes/sweatshop/line/station"
	"github.com/Fewbytes/sweatshop/line/viceroy"
	"github.com/Fewbytes/sweatshop/line/witness"
)

const usage = `usage:
  sweatshop keygen PATH                 generate a witness signing key (PATH, PATH.pub)
  sweatshop guard install-hooks REPO    install the merge guard's pre-receive hook in a bare repo
  sweatshop line init --dir D --source S [--engine E] [--image I] [--port P]
                  [--sovereign-name N --sovereign-discord ID] [--channel ID]
                                        create a line directory seeded from S's master
  sweatshop line install --name N --dir D --project P [--image I] [--cpu-quota Q] [--memory-max M] [--mirror-creds CD] [--quadlet-dir QD] [--slice-dir SD]
                                        write the line's Quadlet units (serve, heijunka, andon, kaizen, mirror if --mirror-creds, network, volume) and resource slice; then systemctl --user daemon-reload and start sweatshop-N-serve
  sweatshop line mirror --dir D --creds CD [--once] [--interval 1m]
                                        push master, tags and attestations fast-forward to the storage mirrors in D's constitution; CD holds deploy_key and known_hosts
  sweatshop line station add --dir D --name NAME --role ROLE
                                        add a station (key, token, trusted key) to the line in D; restart line serve
  sweatshop line serve --dir D [--addr 127.0.0.1:PORT] [--unsafe-listen] [--beads DIR] [--cold-checks]
                                        serve the station API for the line in D
  sweatshop line attest [--rev HEAD] [--repo .]
                                        have this station's witness check rev
  sweatshop line merge [--rev HEAD] [--repo .]
                                        check rev and submit it to the merge guard
  sweatshop line status                 print the line's master and andon state
  sweatshop line usage --card ID (--tokens N | --claude-output FILE)
                                        report tokens this station spent on a card, counted against the constitution's token budgets; with --claude-output, read the tokens from a claude -p --output-format json result and print its text
  sweatshop line stats [--since D]      print CI health: pass rate, durations, slow, failing and flaky tests, time to green (D: 36h, 7d)
  sweatshop line ci stats [--since D] [--check C] [--commit SHA]
                                        print per check: runs, failure rate, duration p50/p95/max and commits where it both passed and failed (D: 36h, 7d)
  sweatshop line events [--since D] [--card ID] [--station S]
                                        print the line's event log, oldest first, as JSON: offers, claims, releases, give-ups, closes, reaps, submits, merges, andon pulls and clears
  sweatshop line logs [--since D] [--station S]
                                        print recent redacted logs of line components (viceroy only; S=serve is the station API)
  sweatshop line reports list [--prefix P] | get KEY | presign KEY [--ttl D]
                                        viceroy only, read-only: list keys in the reports bucket as JSON / print one object / print a time-limited read URL for a kaizen report (D: 15m, at most the constitution's viceroy.presign_max_ttl)
  sweatshop line evidence ID            print a check log
  sweatshop line cards list [--status S] [--label L] [--parent ID] | show ID | create --title T --type TYPE --priority N [--parent ID] [--acceptance A] (description on stdin) | comment ID (text on stdin) | offer ID | close ID (reason on stdin) | label ID [--add L]... [--remove L]...
                                        read and (viceroy only) write cards; output is JSON
  sweatshop line channel inbox [--after ID] [--wait 30s] | post [--from WHO] (text on stdin)
                                        read the king's channel / post to it (viceroy only)
  sweatshop line testament get | put (notes on stdin)
                                        read / replace the viceroy's testament, <= 64 KiB (viceroy only)
  sweatshop line constitution get | propose (TOML on stdin) | log
                                        read the constitution / propose a full replacement for the king to approve (viceroy only) / list its history
  sweatshop line viceroy start --dir D --commit C [--engine E] [--url URL] [--station NAME]
                                        owner: build and start viceroy generation 1 at master commit C
  sweatshop viceroy succeed [--commit C] | deploy [--commit C] | checkin
                                        viceroy: request succession to C (default: master) / ask the king to redeploy the line to C / report this generation is up
  sweatshop line deployer --dir D [--backend B]
                                        the privileged actuator: apply the king-approved release with backend launchd or k8s (line.toml [deploy])
  sweatshop line controller --dir D [--socket unix:///run/podman/podman.sock] [--once]
                                        the privileged workload controller: run approved workload requests on the podman socket (controller token <dir>/controller.token)
  sweatshop line andon --beads DIR [--interval 30s] [--triage]
                                        watch master; pull/clear the andon cord (andon station token)
  sweatshop line heijunka --beads DIR [--interval 5m] [--min-agents 1] [--max-agents 4]
                                        level flow: publish agent and WIP-limit levels to the line (SIGUSR1 wakes it now)
  sweatshop line heijunka state         read-only: levels, offer queue and why makers idle
  sweatshop line maker --dir D --station NAME (--beads DIR | --cards api) [--once] [--engine E] [--url URL] [--timeout 2h] [--max-attempts 3] [--memory 4g] [--pids 1024] [--cpus 2]
                                        work ready "line" beads in station containers
  sweatshop line gemba --dir D --station NAME --beads DIR [--once] [--engine E] [--url URL] [--timeout 2h] [--max-attempts 3] [--memory 4g] [--pids 1024] [--cpus 2]
                                        work ready "gemba" beads (acceptance-test add/fix/retire) in station containers
  sweatshop line kaizen --dir D --station NAME [--once] [--engine E] [--url URL] [--timeout 2h] [--interval 24h] [--memory 4g] [--pids 1024] [--cpus 2]
                                        run one measured refactoring per interval, when master has moved, in a station container
  sweatshop line kaizen context --commit SHA --out DIR
                                        kaizen run: save the CI runs (runs.json) and the previous run's signals (previous.json, if any) for the signal scripts
  sweatshop line kaizen publish --commit SHA --signals F --report F [--after F]
                                        kaizen run: store signals.json and report.html under reports/kaizen/<commit>/ next to the CI reports
  sweatshop line kaizen mutation --commit SHA --source kaizen|card --file mutation.json
                                        kaizen run: record the mutation score (with tool, version, sample size and runtime) kaizen_signals.py mutation measured
  sweatshop line kaizen mutation-trend  kaizen run: print every recorded mutation measurement, oldest first
  sweatshop line kaizen report (Markdown on stdin)
                                        kaizen run: publish this run's analysis report to the line's object store (SWEATSHOP_BEAD names the run)
  sweatshop viceroy run [--heartbeat 6h]
                                        the viceroy container's wake loop (reads SWEATSHOP_VICEROY_GEN, SWEATSHOP_PREDECESSOR)

Client commands read SWEATSHOP_LINE_URL and SWEATSHOP_LINE_TOKEN.`

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
	case len(args) >= 2 && args[0] == "viceroy" && (args[1] == "succeed" || args[1] == "checkin" || args[1] == "deploy"):
		return viceroyClient(args[1], args[2:], stdout)
	case len(args) >= 2 && args[0] == "viceroy" && args[1] == "run":
		return viceroyRun(args[2:], viceroy.ClaudeCLI{Dir: "/work", Timeout: viceroy.DefaultRunLimit})
	case len(args) >= 2 && args[0] == "line":
		return runLine(args[1:], stdout)
	default:
		return errors.New(usage)
	}
}

// runLine dispatches the `sweatshop line ...` commands; args start after "line".
func runLine(args []string, stdout io.Writer) error {
	switch {
	case args[0] == "init":
		return lineInit(args[1:], stdout)
	case args[0] == "install":
		return lineInstall(args[1:], stdout)
	case args[0] == "mirror":
		return lineMirror(args[1:])
	case len(args) >= 2 && args[0] == "station" && args[1] == "add":
		return lineStationAdd(args[2:], stdout)
	case args[0] == "serve":
		return lineServe(args[1:], stdout)
	case args[0] == "attest" || args[0] == "merge":
		return lineSubmit(args[0], args[1:], stdout)
	case args[0] == "usage":
		return lineUsage(args[1:], stdout)
	case args[0] == "stats":
		return lineStats(args[1:], stdout)
	case len(args) >= 2 && args[0] == "ci" && args[1] == "stats":
		return lineCIStats(args[2:], stdout)
	case args[0] == "events":
		return lineEvents(args[1:], stdout)
	case args[0] == "logs":
		return lineLogs(args[1:], stdout)
	case args[0] == "reports":
		return lineReports(args[1:], stdout)
	case len(args) == 1 && args[0] == "status":
		return lineStatus(stdout)
	case args[0] == "cards":
		return lineCards(args[1:], os.Stdin, stdout)
	case args[0] == "channel":
		return lineChannel(args[1:], os.Stdin, stdout)
	case args[0] == "constitution":
		return lineConstitution(args[1:], os.Stdin, stdout)
	case args[0] == "testament":
		return lineTestament(args[1:], os.Stdin, stdout)
	case len(args) >= 2 && args[0] == "viceroy" && args[1] == "start":
		return lineViceroyStart(args[2:], stdout)
	case args[0] == "deployer":
		return lineDeployer(args[1:])
	case args[0] == "controller":
		return lineController(args[1:])
	case args[0] == "andon":
		return lineAndon(args[1:])
	case len(args) == 2 && args[0] == "heijunka" && args[1] == "state":
		return lineHeijunkaState(stdout)
	case args[0] == "heijunka":
		return lineHeijunka(args[1:])
	case args[0] == "maker":
		return lineMaker(args[1:])
	case args[0] == "gemba":
		return lineGemba(args[1:])
	case len(args) >= 2 && args[0] == "kaizen" && args[1] == "report":
		return lineKaizenReport(os.Stdin, stdout)
	case len(args) >= 2 && args[0] == "kaizen" && args[1] == "context":
		return lineKaizenContext(args[2:], stdout)
	case len(args) >= 2 && args[0] == "kaizen" && args[1] == "mutation":
		return lineKaizenMutation(args[2:], stdout)
	case len(args) == 2 && args[0] == "kaizen" && args[1] == "mutation-trend":
		return lineKaizenMutationTrend(stdout)
	case len(args) >= 2 && args[0] == "kaizen" && args[1] == "publish":
		return lineKaizenPublish(args[2:], stdout)
	case args[0] == "kaizen":
		return lineKaizen(args[1:])
	case len(args) == 2 && args[0] == "evidence":
		c, err := lineClient()
		if err != nil {
			return err
		}
		data, err := c.Evidence(context.Background(), args[1])
		if err != nil {
			return err
		}
		_, err = stdout.Write(data)
		return err
	default:
		return errors.New(usage)
	}
}

// stationKeyID returns the key ID of the public key stored beside keyFile.
func stationKeyID(keyFile string) (string, error) {
	pub, err := os.ReadFile(keyFile + ".pub")
	if err != nil {
		return "", err
	}
	key, err := attest.ParsePublicKey(strings.TrimSpace(string(pub)))
	if err != nil {
		return "", err
	}
	return attest.KeyID(key), nil
}

func lineInit(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line init", flag.ContinueOnError)
	var opts linedir.InitOptions
	fs.StringVar(&opts.Dir, "dir", "", "line directory (must not exist or be empty)")
	fs.StringVar(&opts.Source, "source", "", "local git repository whose master seeds the line")
	fs.StringVar(&opts.Engine, "engine", "", "container engine (default docker)")
	fs.StringVar(&opts.Image, "image", "", "station image (default sweatshop-station:dev)")
	fs.IntVar(&opts.Port, "port", 0, "station API port (default 7468)")
	fs.StringVar(&opts.SovereignName, "sovereign-name", "", "the king's name (needs --sovereign-discord)")
	fs.StringVar(&opts.SovereignDiscord, "sovereign-discord", "", "the king's Discord user ID")
	fs.StringVar(&opts.Channel, "channel", "", "Discord channel ID for king/viceroy talk")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if opts.Dir == "" || opts.Source == "" {
		return errors.New(usage)
	}
	l, err := linedir.Init(context.Background(), opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "line %s\nport %d\n", l.Dir, l.Port)
	for _, s := range l.Stations {
		keyID, err := stationKeyID(s.KeyFile)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "station %s role %s key_id %s\n", s.Name, s.Role, keyID)
	}
	return nil
}

// lineInstall writes the Quadlet units of the line in --dir and the station
// env files they read. It starts
// nothing: the operator reloads systemd after reading the file list.
func lineInstall(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line install", flag.ContinueOnError)
	var spec quadlet.Spec
	fs.StringVar(&spec.Name, "name", "", "line name (unit names are sweatshop-NAME-...)")
	fs.StringVar(&spec.Dir, "dir", "", "line directory (absolute)")
	fs.StringVar(&spec.ProjectPath, "project", "", "beads workspace of the line (absolute)")
	fs.StringVar(&spec.Image, "image", "localhost/sweatshop-line:dev", "image holding the sweatshop binary")
	fs.StringVar(&spec.CPUQuota, "cpu-quota", "200%", "CPUQuota of the line's slice")
	fs.StringVar(&spec.MemoryMax, "memory-max", "8G", "MemoryMax of the line's slice")
	rewriteEnv := fs.Bool("rewrite-env", false, "rewrite existing station env files (default: keep them)")
	fs.StringVar(&spec.MirrorCredDir, "mirror-creds", "", "host directory with the line's mirror deploy_key and known_hosts (absolute); enables the mirror service")
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	quadletDir := fs.String("quadlet-dir", filepath.Join(home, ".config", "containers", "systemd"), "where Quadlet files go")
	sliceDir := fs.String("slice-dir", filepath.Join(home, ".config", "systemd", "user"), "where the slice unit goes")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if spec.Name == "" || spec.Dir == "" || spec.ProjectPath == "" {
		return errors.New(usage)
	}
	l, err := linedir.Load(spec.Dir)
	if err != nil {
		return err
	}
	spec.Port = l.Port
	spec.URL = l.URL
	if l.Network == "" || l.URL == "" {
		fmt.Fprintln(stdout, "note: line.toml has no network or url; station containers started by the runners cannot reach serve through host.containers.internal when it runs on the line network. Set both in line.toml.")
	}
	envFiles, err := quadlet.WriteEnvFiles(spec, secret.ForLine(spec.Dir), *rewriteEnv)
	if err != nil {
		return err
	}
	written, err := quadlet.Install(spec, *quadletDir, *sliceDir)
	if err != nil {
		return err
	}
	written = append(envFiles, written...)
	for _, path := range written {
		fmt.Fprintln(stdout, path)
	}
	return nil
}

// lineMirror publishes the line's remote to the constitution's storage
// mirrors, once or every --interval. A failed push is logged and, with --once,
// returned; the loop keeps trying because the next landing may fast-forward.
func lineMirror(args []string) error {
	fs := flag.NewFlagSet("line mirror", flag.ContinueOnError)
	dir := fs.String("dir", "", "line directory")
	creds := fs.String("creds", "", "directory holding the deploy_key and known_hosts files")
	once := fs.Bool("once", false, "publish once, then exit")
	interval := fs.Duration("interval", time.Minute, "time between publishes")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *dir == "" || *creds == "" || *interval <= 0 {
		return errors.New(usage)
	}
	l, err := linedir.Load(*dir)
	if err != nil {
		return err
	}
	auth, err := mirror.SSHAuth(*creds)
	if err != nil {
		return err
	}
	logger := log.New(os.Stderr, "mirror: ", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	repo := gitx.Repo{Dir: l.Remote}
	for {
		c, _, err := station.LoadConstitution(ctx, repo, l.Constitution)
		if err == nil {
			err = mirror.Publish(ctx, repo, c, auth)
		}
		if *once {
			return err
		}
		if err != nil {
			logger.Print(err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*interval):
		}
	}
}

func lineStationAdd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line station add", flag.ContinueOnError)
	dir := fs.String("dir", "", "line directory")
	name := fs.String("name", "", "new station's name")
	role := fs.String("role", "", "new station's role")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *dir == "" || *name == "" || *role == "" {
		return errors.New(usage)
	}
	s, err := linedir.AddStation(*dir, linedir.StationSpec{Name: *name, Role: *role})
	if err != nil {
		return err
	}
	keyID, err := stationKeyID(s.KeyFile)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "station %s role %s key_id %s\nrestart line serve to load it\n", s.Name, s.Role, keyID)
	return nil
}

// loopback reports whether addr's host can only be reached from this machine.
// An empty host listens on every interface.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func lineServe(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line serve", flag.ContinueOnError)
	dir := fs.String("dir", "", "line directory")
	addr := fs.String("addr", "", "listen address (default 127.0.0.1:<port from line.toml>)")
	unsafe := fs.Bool("unsafe-listen", false, "allow a non-loopback listen address")
	beads := fs.String("beads", "", "beads workspace for the cards routes (default: routes return 503)")
	cold := fs.Bool("cold-checks", false, "run every check with empty Go caches and module downloads allowed, under checks.cold_timeout")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *dir == "" {
		return errors.New(usage)
	}
	l, err := linedir.Load(*dir)
	if err != nil {
		return err
	}
	if *addr == "" {
		*addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(l.Port))
	}
	// The API holds every station's signing key; tokens are its only guard.
	if !loopback(*addr) && !*unsafe {
		return fmt.Errorf("refusing to listen on non-loopback address %s without --unsafe-listen", *addr)
	}
	srv, err := station.NewFromLine(l)
	if err != nil {
		return err
	}
	for _, st := range srv.Stations {
		st.Witness.Cold = *cold
	}
	if err := srv.OpenStore(context.Background(), l.Dir); err != nil {
		return err
	}
	defer srv.Close()
	serveLog := linelog.Tee{Out: os.Stderr, Secrets: srv.StationTokens(), Record: func(entry string) {
		if err := srv.RecordLog(context.Background(), station.ServeLogSource, entry); err != nil {
			fmt.Fprintf(os.Stderr, "sweatshop: record log line: %v\n", err)
		}
	}}
	log.SetOutput(serveLog)
	if *beads != "" {
		srv.Cards = station.BeadsCards{Dir: *beads}
	}
	if l.Deploy.Image != "" {
		srv.Releases = station.EngineReleaseBuilder{
			Repo: srv.Guard.Repo, Image: l.Deploy.Image, Dockerfile: l.Deploy.Dockerfile,
			Run: station.CLIEngine{Bin: l.Engine}.Output,
		}
	}
	ch, err := station.OpenChannel(l.Dir, srv.Guard.Constitution, srv.Cards)
	if err != nil {
		return err
	}
	srv.Channel = ch
	reload := make(chan struct{}, 1)
	if ch != nil {
		ch.Constitution = srv.ApproveConstitution
		ch.Deploy = srv.ApproveDeploy
		ch.Workload = srv.ApproveWorkload
		ch.Epics = srv.Epics
		ch.Restart = func() {
			select {
			case reload <- struct{}{}:
			default:
			}
		}
	}
	sup := station.NewSupervisor(srv, l, station.CLIEngine{Bin: l.Engine, Log: log.New(serveLog, "viceroy: ", log.LstdFlags)})
	if err := sup.Resume(context.Background()); err != nil {
		log.Printf("viceroy: resume: %v", err)
	}
	if ch != nil {
		pollCtx, stopPoll := context.WithCancel(context.Background())
		defer stopPoll()
		go ch.Run(pollCtx)
		watch := &station.MakerWatch{Server: srv, Announce: ch.Announce, Started: time.Now()}
		go watch.Run(pollCtx)
	}
	fmt.Fprintf(stdout, "station api for %s on %s\n", l.Dir, *addr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-reload
		// Exit cleanly so the supervisor (launchd) restarts serve with the
		// approved constitution.
		log.Printf("constitution changed; exiting to reload")
		stop()
	}()
	return serveUntilDone(ctx, newStationHTTPServer(*addr, srv))
}

const (
	// stationReadTimeout bounds reading a whole request, body included, so a
	// stalled bundle upload cannot hold a connection and its temp file forever.
	stationReadTimeout = 10 * time.Minute
	// stationShutdownTimeout is how long in-flight requests (attest and merge
	// run checks synchronously) get to finish after a shutdown signal.
	stationShutdownTimeout = 5 * time.Minute
)

// newStationHTTPServer returns the station API server. It sets no write
// timeout: attest and merge run the line's checks synchronously.
func newStationHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: stationReadTimeout}
}

// serveUntilDone serves hs until ctx is done, then drains in-flight requests
// for up to stationShutdownTimeout. It returns nil after a clean shutdown.
func serveUntilDone(ctx context.Context, hs *http.Server) error {
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), stationShutdownTimeout)
	defer cancel()
	if err := hs.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// lineAndon runs the andon watcher until interrupted. It needs the andon
// station's token.
func lineAndon(args []string) error {
	fs := flag.NewFlagSet("line andon", flag.ContinueOnError)
	beads := fs.String("beads", "", "directory whose beads database receives andon cards")
	interval := fs.Duration("interval", 30*time.Second, "how often to look at master")
	triage := fs.Bool("triage", false, "have a tool-less claude -p comment a triage on each new andon card")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *beads == "" {
		return errors.New(usage)
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var cards andon.Cards = andon.BeadCards{Dir: *beads}
	if *triage {
		bead := andon.BeadCards{Dir: *beads}
		cards = andon.TriagedBeadCards{BeadCards: bead, LLMTriage: andon.LLMTriage{
			Agent:    andon.ClaudeText{Timeout: 5 * time.Minute},
			Evidence: c.Evidence,
			Comments: bead,
		}}
	}
	w := &andon.Watcher{
		API: c, Cards: cards, Interval: *interval,
		Log: componentLogger(c, "andon: "),
	}
	return w.Run(ctx)
}

// lineHeijunka runs the heijunka worker until interrupted. It wakes every
// interval and on SIGUSR1 (the manual trigger).
func lineHeijunka(args []string) error {
	fs := flag.NewFlagSet("line heijunka", flag.ContinueOnError)
	beads := fs.String("beads", "", "directory whose beads database holds the cards")
	interval := fs.Duration("interval", 5*time.Minute, "periodic wake")
	policy := heijunka.DefaultPolicy
	fs.IntVar(&policy.MinAgents, "min-agents", policy.MinAgents, "fewest agents")
	fs.IntVar(&policy.MaxAgents, "max-agents", policy.MaxAgents, "most agents")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *beads == "" {
		return errors.New(usage)
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	poke := make(chan struct{}, 1)
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1)
	go func() {
		for range usr1 {
			select {
			case poke <- struct{}{}:
			default:
			}
		}
	}()
	raw, err := c.ConstitutionGet(ctx)
	if err != nil {
		return fmt.Errorf("read constitution: %w", err)
	}
	needsTimeout, err := heijunka.NeedsTimeoutFromConstitution(raw)
	if err != nil {
		return fmt.Errorf("constitution: %w", err)
	}
	logger := componentLogger(c, "heijunka: ")
	w := &heijunka.Worker{
		Needs:    &heijunka.NeedsTriager{Cards: heijunka.BeadsNeeds{Dir: *beads}, Timeout: needsTimeout, Log: logger},
		Reaper:   &heijunka.Reaper{Cards: heijunka.BeadsReap{BeadsNeeds: heijunka.BeadsNeeds{Dir: *beads}}, Events: c, Log: logger},
		Status:   c,
		Observer: heijunka.BeadsObserver{API: c, Dir: *beads, Label: station.LabelLine},
		Kaizen:   c,
		Actuator: heijunka.LevelsPublisher{API: c}, Policy: policy, Interval: *interval, Poke: poke, Log: logger,
	}
	return w.Run(ctx)
}

// lineMaker runs a Maker station on the host: it reads the station's token
// from the line directory and hands it only to the station container.
func lineMaker(args []string) error {
	fs := flag.NewFlagSet("line maker", flag.ContinueOnError)
	dir := fs.String("dir", "", "line directory")
	name := fs.String("station", "", "maker station name (e.g. maker-1)")
	beads := fs.String("beads", "", "directory whose beads database holds the cards (--cards beads)")
	cardsMode := fs.String("cards", "beads", "how to reach cards: beads (bd in --beads) or api (line serve, via SWEATSHOP_LINE_URL and SWEATSHOP_LINE_TOKEN; never opens .beads)")
	once := fs.Bool("once", false, "work at most one card, then exit")
	engine := fs.String("engine", "", "container engine CLI (default: the line's engine)")
	url := fs.String("url", "", "station API URL as seen from the container (default: url of line.toml, else http://host.containers.internal:<port>)")
	timeout := fs.Duration("timeout", maker.DefaultTimeout, "longest one card's container may run")
	maxAttempts := fs.Int("max-attempts", maker.DefaultMaxAttempts, "runs one card gets before it is labelled "+maker.GaveUpLabel)
	memory := fs.String("memory", constitution.DefaultMemory, "station container memory limit (engine --memory syntax)")
	pids := fs.Int("pids", constitution.DefaultPids, "station container process limit")
	cpus := fs.String("cpus", constitution.DefaultCPUs, "station container CPU limit")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *dir == "" || *name == "" || (*cardsMode == "beads") != (*beads != "") {
		return errors.New(usage)
	}
	if *cardsMode != "beads" && *cardsMode != "api" {
		return fmt.Errorf("--cards %q: want beads or api", *cardsMode)
	}
	if *pids < 1 || *memory == "" || *cpus == "" {
		return errors.New("--memory, --pids and --cpus must be set (limits cannot be disabled)")
	}
	if *maxAttempts < 1 {
		return fmt.Errorf("--max-attempts %d: must be at least 1", *maxAttempts)
	}
	l, err := linedir.Load(*dir)
	if err != nil {
		return err
	}
	var st *linedir.Station
	for i := range l.Stations {
		if l.Stations[i].Name == *name {
			st = &l.Stations[i]
		}
	}
	if st == nil {
		return fmt.Errorf("line %s has no station %q", l.Dir, *name)
	}
	if *engine == "" {
		*engine = l.Engine
	}
	logger := log.New(os.Stderr, "maker: ", log.LstdFlags)
	var cards maker.Beads = maker.BeadsCLI{Dir: *beads, Actor: st.AgentID}
	var token string
	var heartbeats maker.Heartbeater
	if *cardsMode == "api" {
		c, err := lineClient()
		if err != nil {
			return err
		}
		cards, token, heartbeats = maker.APICards{Client: c}, c.Token, c
		logger = componentLogger(c, "maker: ")
	}
	r := &maker.Runner{
		Line: l, Station: *st, URL: *url, Timeout: *timeout, MaxAttempts: *maxAttempts,
		Memory: *memory, Pids: *pids, CPUs: *cpus, Token: token,
		Beads:      cards,
		Engine:     maker.DockerEngine{Bin: *engine, Log: logger},
		Creds:      maker.HostCreds(os.LookupEnv),
		Log:        logger,
		Heartbeats: heartbeats, Commit: binaryCommit(),
	}
	// The signal becomes the context's cause so the runner can log it.
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		if sig, ok := <-sigs; ok {
			cancel(maker.SignalCause(sig))
		}
	}()
	if *once {
		worked, err := r.Once(ctx)
		if err != nil {
			logger.Printf("exiting on error: %v", err)
			return err
		}
		logger.Printf("exiting: --once done (card worked: %t)", worked)
		return nil
	}
	return r.Loop(ctx, station.MakerPollInterval)
}

// lineGemba runs a Gemba station on the host: lineMaker's runner, offered
// only cards labelled gemba. It reads cards through bd only: the cards API
// restricts claim, release and close to role maker.
func lineGemba(args []string) error {
	fs := flag.NewFlagSet("line gemba", flag.ContinueOnError)
	dir := fs.String("dir", "", "line directory")
	name := fs.String("station", "", "gemba station name (e.g. gemba-1)")
	beads := fs.String("beads", "", "directory whose beads database holds the cards")
	once := fs.Bool("once", false, "work at most one card, then exit")
	engine := fs.String("engine", "", "container engine CLI (default: the line's engine)")
	url := fs.String("url", "", "station API URL as seen from the container (default: url of line.toml, else http://host.containers.internal:<port>)")
	timeout := fs.Duration("timeout", maker.DefaultTimeout, "longest one card's container may run")
	maxAttempts := fs.Int("max-attempts", maker.DefaultMaxAttempts, "runs one card gets before it is labelled "+maker.GaveUpLabel)
	memory := fs.String("memory", constitution.DefaultMemory, "station container memory limit (engine --memory syntax)")
	pids := fs.Int("pids", constitution.DefaultPids, "station container process limit")
	cpus := fs.String("cpus", constitution.DefaultCPUs, "station container CPU limit")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *dir == "" || *name == "" || *beads == "" {
		return errors.New(usage)
	}
	if *pids < 1 || *memory == "" || *cpus == "" {
		return errors.New("--memory, --pids and --cpus must be set (limits cannot be disabled)")
	}
	if *maxAttempts < 1 {
		return fmt.Errorf("--max-attempts %d: must be at least 1", *maxAttempts)
	}
	l, err := linedir.Load(*dir)
	if err != nil {
		return err
	}
	var st *linedir.Station
	for i := range l.Stations {
		if l.Stations[i].Name == *name {
			st = &l.Stations[i]
		}
	}
	if st == nil {
		return fmt.Errorf("line %s has no station %q", l.Dir, *name)
	}
	if st.Role != "gemba" {
		return fmt.Errorf("station %q has role %q, not gemba", *name, st.Role)
	}
	if *engine == "" {
		*engine = l.Engine
	}
	logger := log.New(os.Stderr, "gemba: ", log.LstdFlags)
	r := &maker.Runner{
		Line: l, Station: *st, URL: *url, Timeout: *timeout, MaxAttempts: *maxAttempts,
		Memory: *memory, Pids: *pids, CPUs: *cpus,
		Beads:  maker.BeadsCLI{Dir: *beads, Actor: st.AgentID},
		Engine: maker.DockerEngine{Bin: *engine, Log: logger},
		Creds:  maker.HostCreds(os.LookupEnv),
		Log:    logger,
	}
	gemba.Configure(r)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		if sig, ok := <-sigs; ok {
			cancel(maker.SignalCause(sig))
		}
	}()
	if *once {
		_, err := r.Once(ctx)
		return err
	}
	return r.Loop(ctx, station.MakerPollInterval)
}

// lineKaizenReport publishes the analysis report read from stdin under the
// run ID the runner put in SWEATSHOP_BEAD.
func lineKaizenReport(stdin io.Reader, stdout io.Writer) error {
	run := os.Getenv("SWEATSHOP_BEAD")
	if run == "" {
		return errors.New("SWEATSHOP_BEAD is not set: line kaizen report runs inside a kaizen run")
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	md, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return err
	}
	key, err := c.PublishKaizenAnalysis(context.Background(), station.KaizenAnalysis{Run: run, Markdown: string(md)})
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "published", key)
	return nil
}

// lineKaizenContext saves what the signal scripts cannot read from the clone:
// the CI metrics store's runs, and the signals of the previous Kaizen run.
func lineKaizenContext(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line kaizen context", flag.ContinueOnError)
	commit := fs.String("commit", "", "master commit the run measures")
	out := fs.String("out", "", "directory to write runs.json and previous.json to")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *commit == "" || *out == "" {
		return errors.New(usage)
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	runs, err := c.KaizenRuns(ctx)
	if err != nil {
		return err
	}
	previous, err := c.PreviousKaizenSignals(ctx, *commit)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	runsJSON, err := json.Marshal(runs)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "runs.json"), runsJSON, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s (%d runs)\n", filepath.Join(*out, "runs.json"), len(runs.Runs))
	if previous == nil {
		fmt.Fprintln(stdout, "no previous kaizen run: this run has no trend")
		return nil
	}
	if err := os.WriteFile(filepath.Join(*out, "previous.json"), previous.Signals, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s (measured at %s)\n", filepath.Join(*out, "previous.json"), previous.Commit)
	return nil
}

// lineKaizenPublish stores a run's signals and status report next to the CI
// reports; with --after it also stores the signals re-measured on the change.
func lineKaizenPublish(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line kaizen publish", flag.ContinueOnError)
	commit := fs.String("commit", "", "master commit the baseline was measured at")
	signals := fs.String("signals", "", "signals.json of the baseline")
	report := fs.String("report", "", "report.html")
	after := fs.String("after", "", "signals.json measured on the changed tree (optional)")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *commit == "" || *signals == "" || *report == "" {
		return errors.New(usage)
	}
	k := station.KaizenSignals{Commit: *commit}
	var err error
	if k.Signals, err = os.ReadFile(*signals); err != nil {
		return err
	}
	if *after != "" {
		if k.After, err = os.ReadFile(*after); err != nil {
			return err
		}
	}
	html, err := os.ReadFile(*report)
	if err != nil {
		return err
	}
	k.ReportHTML = string(html)
	c, err := lineClient()
	if err != nil {
		return err
	}
	key, err := c.PublishKaizenSignals(context.Background(), k)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "published", key)
	return nil
}

// lineKaizenMutation records the mutation measurement `kaizen_signals.py
// mutation` wrote. Mutation score is tracked, never gated on.
func lineKaizenMutation(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line kaizen mutation", flag.ContinueOnError)
	commit := fs.String("commit", "", "commit the mutation score was measured at")
	source := fs.String("source", "", "what the measurement was taken for: kaizen (a kaizen run) or card (a landed card)")
	file := fs.String("file", "", "mutation.json")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *commit == "" || *source == "" || *file == "" {
		return errors.New(usage)
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	m, err := station.MutationRunFromSignals(*commit, linestore.MutationSource(*source), data)
	if err != nil {
		return err
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	if err := c.RecordKaizenMutation(context.Background(), m); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "recorded mutation run at", *commit)
	return nil
}

// lineKaizenMutationTrend prints every recorded mutation measurement, so the
// trend, the sample sizes and the cost of a run can be read side by side.
func lineKaizenMutationTrend(stdout io.Writer) error {
	c, err := lineClient()
	if err != nil {
		return err
	}
	runs, err := c.KaizenMutationRuns(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, formatMutationTrend(runs))
	return nil
}

// formatMutationTrend renders mutation measurements one per line; an
// unavailable score prints its reason, never a number.
func formatMutationTrend(runs []linestore.MutationRun) string {
	var b strings.Builder
	for _, m := range runs {
		score := "unavailable: " + m.Unavailable
		if m.Score != nil {
			score = fmt.Sprintf("score %.3f over %d mutants", *m.Score, *m.SampleSize)
		}
		fmt.Fprintf(&b, "%s %-6s %-10s %s %s %s %s in %s\n", m.Commit[:12], m.Source, m.Language, m.Tool, m.ToolVersion,
			strings.Join(m.Scope, ","), score, time.Duration(m.DurationMS)*time.Millisecond)
	}
	return b.String()
}

// lineKaizen runs a Kaizen station on the host, like lineMaker but with no
// cards: the schedule and master's movement start each run.
func lineKaizen(args []string) error {
	fs := flag.NewFlagSet("line kaizen", flag.ContinueOnError)
	dir := fs.String("dir", "", "line directory")
	name := fs.String("station", "", "kaizen station name (e.g. kaizen-1)")
	once := fs.Bool("once", false, "run at most once, then exit")
	engine := fs.String("engine", "", "container engine CLI (default: the line's engine)")
	url := fs.String("url", "", "station API URL as seen from the container (default: url of line.toml, else http://host.containers.internal:<port>)")
	timeout := fs.Duration("timeout", maker.DefaultTimeout, "longest one run's container may run")
	interval := fs.Duration("interval", kaizen.DefaultInterval, "least time between two runs")
	memory := fs.String("memory", constitution.DefaultMemory, "station container memory limit (engine --memory syntax)")
	pids := fs.Int("pids", constitution.DefaultPids, "station container process limit")
	cpus := fs.String("cpus", constitution.DefaultCPUs, "station container CPU limit")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *dir == "" || *name == "" {
		return errors.New(usage)
	}
	if *pids < 1 || *memory == "" || *cpus == "" {
		return errors.New("--memory, --pids and --cpus must be set (limits cannot be disabled)")
	}
	if *interval <= 0 {
		return fmt.Errorf("--interval %s: must be positive", *interval)
	}
	l, err := linedir.Load(*dir)
	if err != nil {
		return err
	}
	var st *linedir.Station
	for i := range l.Stations {
		if l.Stations[i].Name == *name {
			st = &l.Stations[i]
		}
	}
	if st == nil {
		return fmt.Errorf("line %s has no station %q", l.Dir, *name)
	}
	if st.Role != "kaizen" {
		return fmt.Errorf("station %q has role %q, not kaizen", *name, st.Role)
	}
	if *engine == "" {
		*engine = l.Engine
	}
	logger := log.New(os.Stderr, "kaizen: ", log.LstdFlags)
	r := &kaizen.Runner{
		Line: l, Station: *st, URL: *url, Timeout: *timeout, Interval: *interval,
		Memory: *memory, Pids: *pids, CPUs: *cpus,
		Engine: maker.DockerEngine{Bin: *engine, Log: logger},
		Creds:  maker.HostCreds(os.LookupEnv),
		Log:    logger,
		Commit: binaryCommit(),
	}
	if c, err := lineClient(); err == nil {
		r.Heartbeats, r.Requests, r.Token = c, c, c.Token
	} else {
		logger.Printf("no heartbeats: %v", err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		if sig, ok := <-sigs; ok {
			cancel(maker.SignalCause(sig))
		}
	}()
	if *once {
		_, err := r.Once(ctx)
		return err
	}
	return r.Loop(ctx, kaizen.DefaultPoll)
}

// binaryCommit is the VCS revision this binary was built from, "unknown"
// when the build carries none.
func binaryCommit() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range info.Settings {
			if kv.Key == "vcs.revision" {
				return kv.Value
			}
		}
	}
	return "unknown"
}

// parseWithoutOperands parses args into fs and rejects any positional operand
// with the usage error.
func parseWithoutOperands(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New(usage)
	}
	return nil
}

// lineClient builds the station client from the environment. The token is
// trimmed because a secret made from a token file carries its trailing
// newline, which net/http rejects in the Authorization header.
func lineClient() (station.Client, error) {
	c := station.Client{URL: os.Getenv("SWEATSHOP_LINE_URL"), Token: strings.TrimSpace(os.Getenv("SWEATSHOP_LINE_TOKEN")), Gen: os.Getenv("SWEATSHOP_VICEROY_GEN")}
	if c.URL == "" {
		return c, errors.New("SWEATSHOP_LINE_URL is not set")
	}
	if c.Token == "" {
		return c, errors.New("SWEATSHOP_LINE_TOKEN is empty or not set")
	}
	return c, nil
}

// lineSubmit runs `line attest` or `line merge`; it fails unless the checks
// passed (attest) or the guard merged (merge).
func lineSubmit(cmd string, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line "+cmd, flag.ContinueOnError)
	rev := fs.String("rev", "HEAD", "revision to submit")
	repo := fs.String("repo", ".", "repository holding rev")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	var attestation witness.AttestResult
	var outcome error
	if cmd == "merge" {
		// In a Maker station the runner names the card; refuse a commit
		// whose trailers would not let the runner see that it landed.
		if bead := os.Getenv("SWEATSHOP_BEAD"); bead != "" {
			if err := maker.CheckTrailers(ctx, gitx.Repo{Dir: *repo}, *rev, bead, os.Getenv("SWEATSHOP_ANDON_EVENT")); err != nil {
				return err
			}
		}
		// A Kaizen run's evidence is the change report in the commit message.
		if os.Getenv("SWEATSHOP_KAIZEN") != "" {
			if err := kaizen.CheckCommitReport(ctx, gitx.Repo{Dir: *repo}, *rev); err != nil {
				return err
			}
		}
		res, err := c.Merge(ctx, *repo, *rev)
		if err != nil {
			return err
		}
		attestation = res.Attestation
		fmt.Fprintf(stdout, "commit %s\nstatus %s\n", res.Guard.Commit, res.Guard.Status)
		if res.Guard.Reason != "" {
			fmt.Fprintf(stdout, "reason %s\n", res.Guard.Reason)
		}
		if res.Guard.Detail != "" {
			fmt.Fprintf(stdout, "detail %s\n", res.Guard.Detail)
		}
		if res.Guard.Status != guard.Merged {
			outcome = fmt.Errorf("not merged: %s %s", res.Guard.Status, res.Guard.Reason)
		}
	} else {
		res, err := c.Attest(ctx, *repo, *rev)
		if err != nil {
			return err
		}
		attestation = res
		fmt.Fprintf(stdout, "commit %s\npassed %t\n", res.Commit, res.Passed)
		if !res.Passed {
			outcome = errors.New("checks failed")
		}
	}
	for _, check := range attestation.Statement.Predicate.Checks {
		fmt.Fprintf(stdout, "check %s exit %d evidence %s\n", check.Name, check.ExitCode, check.EvidenceID)
		if check.ExitCode == 0 || check.EvidenceID == "" {
			continue
		}
		log, err := c.Evidence(ctx, check.EvidenceID)
		if err != nil {
			fmt.Fprintf(stdout, "(evidence unavailable: %v)\n", err)
			continue
		}
		fmt.Fprintf(stdout, "--- last 200 lines of %s ---\n%s\n", check.EvidenceID, tail(string(log), 200))
	}
	return outcome
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// stationLine is one station's row in `line status`; a scheduled station also
// shows when it last ran and when it may next.
func stationLine(s station.StationStatus) string {
	line := fmt.Sprintf("station %s (%s) %s %s alive=%t", s.Name, s.Role, s.State, s.Card, s.Alive)
	if s.LastCard != "" {
		line += " last_card=" + s.LastCard
	}
	if s.IdleReason != "" {
		line += fmt.Sprintf(" idle_reason=%q", s.IdleReason)
	}
	if s.LastRun != nil {
		line += " last_run=" + s.LastRun.Format(time.RFC3339)
	}
	if s.NextRun != nil {
		line += " next_run=" + s.NextRun.Format(time.RFC3339)
	}
	return line
}

// budgetLine is one token budget's row in `line status`.
func budgetLine(b station.BudgetStatus) string {
	scope := b.Scope
	if b.Card != "" {
		scope += " " + b.Card
	}
	line := fmt.Sprintf("budget %s: %d of %d tokens used (%d%%), %d remaining, per %s", scope, b.Used, b.Limit, b.Percent, max(b.Limit-b.Used, 0), b.Period)
	if b.Exhausted {
		line += " EXHAUSTED"
	}
	return line
}

// lineUsage implements `line usage`. With --claude-output it prints the run's
// text before reporting, so a failed report never hides what the agent said.
func lineUsage(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line usage", flag.ContinueOnError)
	card := fs.String("card", "", "card the tokens were spent on")
	tokens := fs.Int64("tokens", 0, "tokens spent")
	claudeOutput := fs.String("claude-output", "", "file holding a claude -p --output-format json result to report instead of --tokens")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *claudeOutput != "" {
		if *tokens != 0 {
			return errors.New("line usage: --tokens and --claude-output are exclusive")
		}
		data, err := os.ReadFile(*claudeOutput)
		if err != nil {
			return err
		}
		run, err := station.ParseClaudeRun(data)
		if err != nil {
			fmt.Fprintf(stdout, "%s\n", data) // not a result object (claude crashed): keep what it printed
			return err
		}
		fmt.Fprintln(stdout, run.Result)
		*tokens = run.Tokens
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	return c.ReportTokenUsage(context.Background(), *card, *tokens)
}

func lineStatus(stdout io.Writer) error {
	c, err := lineClient()
	if err != nil {
		return err
	}
	st, err := c.Status(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "master %s\n", st.Master)
	for _, s := range st.Stations {
		fmt.Fprintln(stdout, stationLine(s))
	}
	for _, h := range st.Held {
		fmt.Fprintf(stdout, "held %s: %s\n", h.ID, h.Reason)
	}
	for _, b := range st.Budgets {
		fmt.Fprintln(stdout, budgetLine(b))
	}
	if st.Event == "" {
		fmt.Fprintln(stdout, "andon clear")
		return nil
	}
	fmt.Fprintf(stdout, "andon pulled event %s\n", st.Event)
	if e := st.Andon; e != nil {
		fmt.Fprintf(stdout, "andon commit %s check %s exit %d evidence %s by %s (%s) at %s\n",
			e.Commit, e.Check, e.ExitCode, e.EvidenceID, e.AgentID, e.Role, e.PulledAt.Format(time.RFC3339))
	}
	return nil
}

// lineHeijunkaState prints heijunka's levels, the offer queue and why makers idle.
func lineHeijunkaState(stdout io.Writer) error {
	c, err := lineClient()
	if err != nil {
		return err
	}
	hs, err := c.HeijunkaState(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "levels agents=%d wip_limit=%d in_progress=%d\n", hs.Levels.Agents, hs.Levels.WIPLimit, hs.InProgress)
	fmt.Fprintf(stdout, "offered %v\nunlabelled %v\n", hs.Offered, hs.Unlabelled)
	for _, m := range hs.Idle {
		fmt.Fprintf(stdout, "idle %s: %s\n", m.Station, m.Reason)
	}
	return nil
}

func lineStats(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line stats", flag.ContinueOnError)
	since := fs.String("since", "", "only runs from the last D (36h, 7d); default all history")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	sum, err := c.Stats(context.Background(), *since)
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, sum.Text())
	return err
}

// lineCIStats implements `line ci stats`.
func lineCIStats(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line ci stats", flag.ContinueOnError)
	since := fs.String("since", "", "only runs from the last D (36h, 7d); default all history")
	check := fs.String("check", "", "only this check")
	commit := fs.String("commit", "", "only this commit")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	sums, err := c.CheckStats(context.Background(), *since, *check, *commit)
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, cistats.CheckText(sums))
	return err
}

// lineEvents implements `line events`.
func lineEvents(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line events", flag.ContinueOnError)
	since := fs.String("since", "", "only events from the last D (36h, 7d); default all history")
	card := fs.String("card", "", "only events of this card")
	stationName := fs.String("station", "", "only events by this station")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	events, err := c.Events(context.Background(), *since, *card, *stationName)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(events)
}

// lineReports implements `line reports`.
func lineReports(args []string, stdout io.Writer) error {
	c, err := lineClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch {
	case len(args) >= 1 && args[0] == "list":
		fs := flag.NewFlagSet("line reports list", flag.ContinueOnError)
		prefix := fs.String("prefix", "", "only keys with this prefix")
		if err := parseWithoutOperands(fs, args[1:]); err != nil {
			return err
		}
		keys, err := c.ListReports(ctx, *prefix)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(keys)
	case len(args) >= 2 && args[0] == "presign":
		fs := flag.NewFlagSet("line reports presign", flag.ContinueOnError)
		ttl := fs.Duration("ttl", 0, "link lifetime; default is the constitution's cap")
		if err := parseWithoutOperands(fs, args[2:]); err != nil {
			return err
		}
		res, err := c.PresignReport(ctx, args[1], *ttl)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, res.URL)
		return err
	case len(args) == 2 && args[0] == "get":
		data, err := c.GetReport(ctx, args[1])
		if err != nil {
			return err
		}
		_, err = stdout.Write(data)
		return err
	}
	return errors.New(usage)
}

// lineCards implements `line cards`; stdin carries descriptions and comments.
func lineCards(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet("line cards "+cmd, flag.ContinueOnError)
	var f station.ListFilter
	var nc station.NewCard
	var labels station.LabelChange
	switch cmd {
	case "list":
		fs.StringVar(&f.Status, "status", "", "filter by status")
		fs.StringVar(&f.Label, "label", "", "filter by label")
		fs.StringVar(&f.Parent, "parent", "", "filter by parent card")
	case "create":
		fs.StringVar(&nc.Title, "title", "", "card title")
		fs.StringVar(&nc.Type, "type", "", "bug|feature|task|epic|chore")
		fs.IntVar(&nc.Priority, "priority", 2, "priority 0..4")
		fs.StringVar(&nc.Parent, "parent", "", "parent card")
		fs.StringVar(&nc.Acceptance, "acceptance", "", "acceptance criteria")
	case "label":
		fs.Func("add", "label to add (repeatable)", func(l string) error { labels.Add = append(labels.Add, l); return nil })
		fs.Func("remove", "label to remove (repeatable)", func(l string) error { labels.Remove = append(labels.Remove, l); return nil })
	case "show", "comment", "offer", "close":
	default:
		return errors.New(usage)
	}
	// label takes its flags after the card ID, which flag.Parse would stop at.
	var labelID string
	if cmd == "label" {
		if len(args) == 0 {
			return errors.New(usage)
		}
		labelID, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	wantArgs := 0
	if cmd == "show" || cmd == "comment" || cmd == "offer" || cmd == "close" {
		wantArgs = 1
	}
	if fs.NArg() != wantArgs {
		return errors.New(usage)
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	var out []byte
	switch cmd {
	case "list":
		out, err = c.CardsList(ctx, f)
	case "show":
		out, err = c.CardShow(ctx, fs.Arg(0))
	case "create":
		desc, rerr := io.ReadAll(io.LimitReader(stdin, 1<<20))
		if rerr != nil {
			return rerr
		}
		nc.Description = strings.TrimRight(string(desc), "\n")
		out, err = c.CardCreate(ctx, nc)
	case "comment":
		text, rerr := io.ReadAll(io.LimitReader(stdin, 1<<20))
		if rerr != nil {
			return rerr
		}
		out, err = c.CardComment(ctx, fs.Arg(0), strings.TrimRight(string(text), "\n"))
	case "offer":
		out, err = c.CardOffer(ctx, fs.Arg(0))
	case "label":
		if err = c.CardLabels(ctx, labelID, labels); err == nil {
			out = []byte("labelled " + labelID)
		}
	case "close":
		reason, rerr := io.ReadAll(io.LimitReader(stdin, 1<<20))
		if rerr != nil {
			return rerr
		}
		if err = c.CardClose(ctx, fs.Arg(0), strings.TrimRight(string(reason), "\n")); err == nil {
			out = []byte("closed " + fs.Arg(0))
		}
	}
	if err != nil {
		return err
	}
	_, err = stdout.Write(bytes.TrimRight(out, "\n"))
	if err == nil {
		_, err = io.WriteString(stdout, "\n")
	}
	return err
}

// chunkText splits text into pieces of at most max characters, breaking on
// line boundaries (a single longer line is split mid-line).
func chunkText(text string, max int) []string {
	var out []string
	var cur []rune
	flush := func() {
		if s := strings.TrimRight(string(cur), "\n"); strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
		cur = cur[:0]
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		l := []rune(line)
		if len(cur)+len(l) > max {
			flush()
		}
		for len(l) > max {
			cur = append(cur, l[:max]...)
			flush()
			l = l[max:]
		}
		cur = append(cur, l...)
	}
	flush()
	return out
}

// lineChannel implements `line channel`; post reads its text from stdin.
func lineChannel(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet("line channel "+cmd, flag.ContinueOnError)
	var after, from string
	var wait time.Duration
	switch cmd {
	case "inbox":
		fs.StringVar(&after, "after", "", "only messages after this message ID")
		fs.DurationVar(&wait, "wait", 0, "long-poll up to this long (at most 60s)")
	case "post":
		fs.StringVar(&from, "from", "", "whose message this relays (shown in the footer)")
	default:
		return errors.New(usage)
	}
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if cmd == "post" {
		text, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
		if err != nil {
			return err
		}
		parts := chunkText(string(text), 1900)
		if len(parts) == 0 {
			return errors.New("line channel post: no text on stdin")
		}
		for _, p := range parts {
			if err := c.ChannelPost(ctx, p, from); err != nil {
				return err
			}
		}
		return nil
	}
	out, err := c.ChannelInbox(ctx, after, wait)
	if err != nil {
		return err
	}
	_, err = stdout.Write(bytes.TrimRight(out, "\n"))
	if err == nil {
		_, err = io.WriteString(stdout, "\n")
	}
	return err
}

// lineTestament implements `line testament get|put`; put reads stdin.
func lineTestament(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 1 || (args[0] != "get" && args[0] != "put") {
		return errors.New(usage)
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if args[0] == "get" {
		body, _, err := c.TestamentGet(ctx)
		if err != nil {
			return err
		}
		_, err = stdout.Write(body)
		return err
	}
	const max = 64 << 10
	body, err := io.ReadAll(io.LimitReader(stdin, max+1))
	if err != nil {
		return err
	}
	if len(body) > max {
		return fmt.Errorf("line testament put: input exceeds %d bytes", max)
	}
	return c.TestamentPut(ctx, body)
}

// lineConstitution implements `line constitution get|propose|log`; propose
// reads the full new TOML from stdin.
func lineConstitution(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 1 || (args[0] != "get" && args[0] != "propose" && args[0] != "log") {
		return errors.New(usage)
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch args[0] {
	case "get":
		body, err := c.ConstitutionGet(ctx)
		if err != nil {
			return err
		}
		_, err = stdout.Write(body)
		return err
	case "log":
		entries, err := c.ConstitutionLog(ctx)
		if err != nil {
			return err
		}
		for _, e := range entries {
			fmt.Fprintf(stdout, "%s %s\n", e.Commit[:min(len(e.Commit), 12)], e.Message)
		}
		return nil
	}
	const max = 256 << 10
	body, err := io.ReadAll(io.LimitReader(stdin, max+1))
	if err != nil {
		return err
	}
	if len(body) > max {
		return fmt.Errorf("line constitution propose: input exceeds %d bytes", max)
	}
	n, err := c.ConstitutionPropose(ctx, body)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "proposal %d posted to the channel; waiting for the king to approve\n", n)
	return nil
}

// viceroyRun implements `viceroy run`; the agent is a parameter so tests can
// swap the claude invocation.
func viceroyRun(args []string, agent viceroy.Agent) error {
	fs := flag.NewFlagSet("viceroy run", flag.ContinueOnError)
	heartbeat := fs.Duration("heartbeat", viceroy.DefaultHeartbeat, "status summary to the king at least this often")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *heartbeat <= 0 {
		return errors.New(usage)
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	l := &viceroy.Loop{
		Line: c, Agent: agent, Heartbeat: *heartbeat,
		Gen: os.Getenv("SWEATSHOP_VICEROY_GEN"), Predecessor: os.Getenv("SWEATSHOP_PREDECESSOR"),
		Log: log.New(os.Stderr, "viceroy: ", log.LstdFlags),
	}
	// A new generation checks in only after its startup run worked, so a
	// broken successor fails before it retires its predecessor. The first
	// generation has none.
	if os.Getenv("SWEATSHOP_PREDECESSOR") != "" {
		l.Checkin = func(ctx context.Context) error {
			_, err := c.ViceroyCheckin(ctx)
			return err
		}
	}
	if err := l.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// lineViceroyStart is the owner's bootstrap of viceroy generation 1.
func lineViceroyStart(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line viceroy start", flag.ContinueOnError)
	dir := fs.String("dir", "", "line directory")
	commit := fs.String("commit", "", "master commit to build (full hex)")
	engine := fs.String("engine", "", "container engine CLI (default: the line's engine)")
	url := fs.String("url", "", "station API URL as seen from the container (default: url of line.toml, else http://host.containers.internal:<port>)")
	name := fs.String("station", "", "viceroy station (default: the line's only viceroy station)")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *dir == "" || *commit == "" {
		return errors.New(usage)
	}
	l, err := linedir.Load(*dir)
	if err != nil {
		return err
	}
	srv, err := station.NewFromLine(l)
	if err != nil {
		return err
	}
	var st *station.Station
	for _, s := range srv.Stations {
		if s.Role == "viceroy" && (*name == "" || s.Name == *name) {
			if st != nil {
				return errors.New("several viceroy stations; choose one with --station")
			}
			st = s
		}
	}
	if st == nil {
		return fmt.Errorf("line %s has no viceroy station", l.Dir)
	}
	if *engine == "" {
		*engine = l.Engine
	}
	sup := station.NewSupervisor(srv, l, station.CLIEngine{Bin: *engine, Log: log.New(os.Stderr, "viceroy: ", log.LstdFlags)})
	sup.URL = *url
	g, err := sup.Bootstrap(context.Background(), st, *commit)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "generation %d commit %s container %s\n", g.Gen, g.Commit, station.ContainerName(g.Gen))
	return nil
}

// viceroyClient implements `viceroy succeed` and `viceroy checkin`.
func viceroyClient(cmd string, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("viceroy "+cmd, flag.ContinueOnError)
	var commit string
	if cmd == "succeed" || cmd == "deploy" {
		fs.StringVar(&commit, "commit", "", "commit to succeed or deploy to (default: the line's master)")
	}
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	var out []byte
	if cmd == "checkin" {
		out, err = c.ViceroyCheckin(ctx)
	} else {
		if commit == "" {
			st, serr := c.Status(ctx)
			if serr != nil {
				return serr
			}
			commit = st.Master
		}
		if cmd == "deploy" {
			out, err = c.DeployRequest(ctx, commit)
		} else {
			out, err = c.ViceroySucceed(ctx, commit)
		}
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, strings.TrimRight(string(out), "\n"))
	return err
}

// lineController reconciles the line's workload requests against the podman
// socket. It holds only the controller token (<dir>/controller.token); the
// engine objects it creates are labelled with the line directory's name.
func lineController(args []string) error {
	fs := flag.NewFlagSet("line controller", flag.ContinueOnError)
	dir := fs.String("dir", "", "line directory")
	socket := fs.String("socket", "unix:///run/podman/podman.sock", "engine API socket")
	once := fs.Bool("once", false, "reconcile once, then exit")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *dir == "" {
		return errors.New(usage)
	}
	l, err := linedir.Load(*dir)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(l.Dir, "controller.token"))
	if err != nil {
		return err
	}
	engine, err := dockerclient.NewClientWithOpts(dockerclient.WithHost(*socket), dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	c := &controller.Controller{
		API: station.Client{
			URL:   "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(l.Port)),
			Token: strings.TrimSpace(string(raw)),
		},
		Engine: controller.Podman{Client: engine, Line: filepath.Base(l.Dir), Network: l.Network},
		Log:    log.New(os.Stderr, "", log.LstdFlags).Printf,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *once {
		return c.Once(ctx)
	}
	if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// lineDeployer runs the actuator. It holds only the deployer token (the file
// <dir>/deploy.token) and the platform's own credentials; no model or Discord
// credential.
func lineDeployer(args []string) error {
	fs := flag.NewFlagSet("line deployer", flag.ContinueOnError)
	dir := fs.String("dir", "", "line directory")
	backend := fs.String("backend", "", "backend (default: [deploy] backend of line.toml)")
	once := fs.Bool("once", false, "handle the desired release once, then exit")
	if err := parseWithoutOperands(fs, args); err != nil {
		return err
	}
	if *dir == "" {
		return errors.New(usage)
	}
	l, err := linedir.Load(*dir)
	if err != nil {
		return err
	}
	d := l.Deploy
	if *backend != "" {
		d.Backend = *backend
	}
	raw, err := os.ReadFile(filepath.Join(l.Dir, "deploy.token"))
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(raw))
	if d.URL == "" {
		d.URL = "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(l.Port))
	}
	probe := deploy.HTTPHealth{URL: strings.TrimRight(d.URL, "/") + "/v1/status", Token: token}
	var b deploy.Backend
	container := false
	switch d.Backend {
	case "launchd":
		if d.Dir == "" || d.Domain == "" || len(d.Labels) == 0 {
			return errors.New("launchd backend needs [deploy] dir, domain and labels")
		}
		b = deploy.Launchd{Dir: d.Dir, Source: l.Remote, Domain: d.Domain, Labels: d.Labels, Probe: probe}
	case "k8s":
		if d.API == "" || d.Namespace == "" || d.Deployment == "" || d.Container == "" || d.TokenFile == "" {
			return errors.New("k8s backend needs [deploy] api, token_file, namespace, deployment and container")
		}
		kt, err := os.ReadFile(d.TokenFile)
		if err != nil {
			return err
		}
		b = deploy.Kubernetes{API: d.API, Token: strings.TrimSpace(string(kt)), Namespace: d.Namespace,
			Deployment: d.Deployment, Container: d.Container, Probe: probe}
		container = true
	default:
		return fmt.Errorf("unknown deploy backend %q (want launchd or k8s)", d.Backend)
	}
	a := &deploy.Actuator{
		API: deploy.Client{URL: d.URL, Token: token}, Backend: b, Container: container,
		Log: log.New(os.Stderr, "", log.LstdFlags).Printf,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *once {
		return a.Once(ctx)
	}
	if err := a.Run(ctx); !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
