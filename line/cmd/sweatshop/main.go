// Command sweatshop administers an assembly line (witness keys, the merge
// guard's hook, line directories, the station API) and is the station
// client agents use from inside their containers.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
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
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/deploy"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/heijunka"
	"github.com/Fewbytes/sweatshop/line/linedir"
	"github.com/Fewbytes/sweatshop/line/maker"
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
  sweatshop line station add --dir D --name NAME --role ROLE
                                        add a station (key, token, trusted key) to the line in D; restart line serve
  sweatshop line serve --dir D [--addr 127.0.0.1:PORT] [--unsafe-listen] [--beads DIR]
                                        serve the station API for the line in D
  sweatshop line attest [--rev HEAD] [--repo .]
                                        have this station's witness check rev
  sweatshop line merge [--rev HEAD] [--repo .]
                                        check rev and submit it to the merge guard
  sweatshop line status                 print the line's master and andon state
  sweatshop line evidence ID            print a check log
  sweatshop line cards list [--status S] [--label L] [--parent ID] | show ID | create --title T --type TYPE --priority N [--parent ID] [--acceptance A] (description on stdin) | comment ID (text on stdin) | offer ID | close ID (reason on stdin)
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
  sweatshop line andon --beads DIR [--interval 30s]
                                        watch master; pull/clear the andon cord (andon station token)
  sweatshop line heijunka --dir D --beads DIR [--interval 5m] [--min-agents 1] [--max-agents 4]
                                        level flow: publish agent and WIP-limit levels to D/heijunka.json (SIGUSR1 wakes it now)
  sweatshop line maker --dir D --station NAME (--beads DIR | --cards api) [--once] [--engine E] [--url URL] [--timeout 2h] [--max-attempts 3] [--memory 4g] [--pids 1024] [--cpus 2]
                                        work ready "line" beads in station containers
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
	case len(args) >= 2 && args[0] == "line" && args[1] == "init":
		return lineInit(args[2:], stdout)
	case len(args) >= 3 && args[0] == "line" && args[1] == "station" && args[2] == "add":
		return lineStationAdd(args[3:], stdout)
	case len(args) >= 2 && args[0] == "line" && args[1] == "serve":
		return lineServe(args[2:], stdout)
	case len(args) >= 2 && args[0] == "line" && (args[1] == "attest" || args[1] == "merge"):
		return lineSubmit(args[1], args[2:], stdout)
	case len(args) == 2 && args[0] == "line" && args[1] == "status":
		return lineStatus(stdout)
	case len(args) >= 2 && args[0] == "line" && args[1] == "cards":
		return lineCards(args[2:], os.Stdin, stdout)
	case len(args) >= 2 && args[0] == "line" && args[1] == "channel":
		return lineChannel(args[2:], os.Stdin, stdout)
	case len(args) >= 2 && args[0] == "line" && args[1] == "constitution":
		return lineConstitution(args[2:], os.Stdin, stdout)
	case len(args) >= 2 && args[0] == "line" && args[1] == "testament":
		return lineTestament(args[2:], os.Stdin, stdout)
	case len(args) >= 3 && args[0] == "line" && args[1] == "viceroy" && args[2] == "start":
		return lineViceroyStart(args[3:], stdout)
	case len(args) >= 2 && args[0] == "viceroy" && (args[1] == "succeed" || args[1] == "checkin" || args[1] == "deploy"):
		return viceroyClient(args[1], args[2:], stdout)
	case len(args) >= 2 && args[0] == "viceroy" && args[1] == "run":
		return viceroyRun(args[2:], viceroy.ClaudeCLI{Dir: "/work", Timeout: viceroy.DefaultRunLimit})
	case len(args) >= 2 && args[0] == "line" && args[1] == "deployer":
		return lineDeployer(args[2:])
	case len(args) >= 2 && args[0] == "line" && args[1] == "andon":
		return lineAndon(args[2:])
	case len(args) >= 2 && args[0] == "line" && args[1] == "heijunka":
		return lineHeijunka(args[2:])
	case len(args) >= 2 && args[0] == "line" && args[1] == "maker":
		return lineMaker(args[2:])
	case len(args) == 3 && args[0] == "line" && args[1] == "evidence":
		c, err := lineClient()
		if err != nil {
			return err
		}
		data, err := c.Evidence(context.Background(), args[2])
		if err != nil {
			return err
		}
		_, err = stdout.Write(data)
		return err
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
	fs.StringVar(&opts.SovereignName, "sovereign-name", "", "the king's name (needs --sovereign-discord)")
	fs.StringVar(&opts.SovereignDiscord, "sovereign-discord", "", "the king's Discord user ID")
	fs.StringVar(&opts.Channel, "channel", "", "Discord channel ID for king/viceroy talk")
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

func lineStationAdd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("line station add", flag.ContinueOnError)
	dir := fs.String("dir", "", "line directory")
	name := fs.String("name", "", "new station's name")
	role := fs.String("role", "", "new station's role")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *dir == "" || *name == "" || *role == "" {
		return errors.New(usage)
	}
	s, err := linedir.AddStation(*dir, linedir.StationSpec{Name: *name, Role: *role})
	if err != nil {
		return err
	}
	pub, err := os.ReadFile(s.KeyFile + ".pub")
	if err != nil {
		return err
	}
	key, err := attest.ParsePublicKey(strings.TrimSpace(string(pub)))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "station %s role %s key_id %s\nrestart line serve to load it\n", s.Name, s.Role, attest.KeyID(key))
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *dir == "" {
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
	if *beads != "" {
		srv.Cards = station.BeadsCards{Dir: *beads}
	}
	if l.Deploy.Image != "" {
		srv.Releases = station.EngineReleaseBuilder{
			Repo: srv.Guard.Repo, Ref: srv.Guard.Ref, Image: l.Deploy.Image, Dockerfile: l.Deploy.Dockerfile,
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
		ch.Restart = func() {
			select {
			case reload <- struct{}{}:
			default:
			}
		}
	}
	sup := station.NewSupervisor(srv, l, station.CLIEngine{Bin: l.Engine, Log: log.New(os.Stderr, "viceroy: ", log.LstdFlags)})
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
	// No write timeout: attest and merge run the line's checks synchronously.
	hs := &http.Server{Addr: *addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-reload
		// Exit cleanly so the supervisor (launchd) restarts serve with the
		// approved constitution.
		log.Printf("constitution changed; exiting to reload")
		_ = hs.Shutdown(context.Background())
	}()
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *beads == "" {
		return errors.New(usage)
	}
	c, err := lineClient()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	w := &andon.Watcher{
		API: c, Cards: andon.BeadCards{Dir: *beads}, Interval: *interval,
		Log: log.New(os.Stderr, "andon: ", log.LstdFlags),
	}
	return w.Run(ctx)
}

// lineHeijunka runs the heijunka worker until interrupted. It wakes every
// interval and on SIGUSR1 (the manual trigger).
func lineHeijunka(args []string) error {
	fs := flag.NewFlagSet("line heijunka", flag.ContinueOnError)
	dir := fs.String("dir", "", "line directory; levels are published to heijunka.json in it")
	beads := fs.String("beads", "", "directory whose beads database holds the cards")
	interval := fs.Duration("interval", 5*time.Minute, "periodic wake")
	policy := heijunka.DefaultPolicy
	fs.IntVar(&policy.MinAgents, "min-agents", policy.MinAgents, "fewest agents")
	fs.IntVar(&policy.MaxAgents, "max-agents", policy.MaxAgents, "most agents")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *dir == "" || *beads == "" {
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
	logger := log.New(os.Stderr, "heijunka: ", log.LstdFlags)
	levels := heijunka.LevelsFile{Path: filepath.Join(*dir, "heijunka.json")}
	w := &heijunka.Worker{
		Needs:    &heijunka.NeedsTriager{Cards: heijunka.BeadsNeeds{Dir: *beads}, Timeout: needsTimeout, Log: logger},
		Observer: heijunka.BeadsObserver{API: c, Dir: *beads, Label: station.LabelLine, Levels: levels},
		Actuator: levels, Policy: policy, Interval: *interval, Poke: poke, Log: logger,
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
	url := fs.String("url", "", "station API URL as seen from the container (default http://host.containers.internal:<port>)")
	timeout := fs.Duration("timeout", maker.DefaultTimeout, "longest one card's container may run")
	maxAttempts := fs.Int("max-attempts", maker.DefaultMaxAttempts, "runs one card gets before it is labelled "+maker.GaveUpLabel)
	memory := fs.String("memory", constitution.DefaultMemory, "station container memory limit (engine --memory syntax)")
	pids := fs.Int("pids", constitution.DefaultPids, "station container process limit")
	cpus := fs.String("cpus", constitution.DefaultCPUs, "station container CPU limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *dir == "" || *name == "" || (*cardsMode == "beads") != (*beads != "") {
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New(usage)
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
		fmt.Fprintf(stdout, "station %s (%s) %s %s alive=%t\n", s.Name, s.Role, s.State, s.Card, s.Alive)
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

// lineCards implements `line cards`; stdin carries descriptions and comments.
func lineCards(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet("line cards "+cmd, flag.ContinueOnError)
	var f station.ListFilter
	var nc station.NewCard
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
	case "show", "comment", "offer", "close":
	default:
		return errors.New(usage)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New(usage)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *heartbeat <= 0 {
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
	url := fs.String("url", "", "station API URL as seen from the container (default http://host.containers.internal:<port>)")
	name := fs.String("station", "", "viceroy station (default: the line's only viceroy station)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *dir == "" || *commit == "" {
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New(usage)
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

// lineDeployer runs the actuator. It holds only the deployer token (the file
// <dir>/deploy.token) and the platform's own credentials; no model or Discord
// credential.
func lineDeployer(args []string) error {
	fs := flag.NewFlagSet("line deployer", flag.ContinueOnError)
	dir := fs.String("dir", "", "line directory")
	backend := fs.String("backend", "", "backend (default: [deploy] backend of line.toml)")
	once := fs.Bool("once", false, "handle the desired release once, then exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *dir == "" {
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
