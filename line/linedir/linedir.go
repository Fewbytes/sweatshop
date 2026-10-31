// Package linedir creates and loads a line directory: the on-disk home of one
// assembly line (bare remote, constitution, per-station keys and tokens).
package linedir

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
)

type StationSpec struct{ Name, Role string }

type InitOptions struct {
	Dir    string // line directory; must not exist or be empty
	Source string // git repo whose master seeds the line
	Engine string // default "docker"
	Image  string // default "sweatshop-station:dev"
	Port   int    // default 7468
	Check  string // default "just ci"
	// Optional governance: written to the constitution when set.
	SovereignName    string        // the king's name
	SovereignDiscord string        // the king's Discord user ID
	Channel          string        // Discord channel ID for king/viceroy talk
	Stations         []StationSpec // default: {maker-1, maker}, {andon-1, andon}
}

type Line struct {
	Dir          string
	Port         int
	Remote       string // <Dir>/remote.git
	Constitution string // <Dir>/constitution.toml
	Engine       string
	Image        string
	Network      string // container network of this line's stations; empty means the engine default
	URL          string // station API as station containers see it; empty means StationURL's host-gateway default
	// ViceroyImage, when set, is a prebuilt image every viceroy generation
	// runs. A line whose master carries no station/Dockerfile (a project line)
	// needs it; without it each generation is built from the line's master.
	ViceroyImage string
	Stations     []Station
	Deploy       Deploy
	Storage      Storage
}

// Storage is line.toml's [storage] table: the S3-compatible store `line
// install` configures serve to keep run reports in. Its credential is not
// here; it comes from line/secret (S3AccessKeyCred, S3SecretKeyCred). Zero
// means reports stay in <Dir>/reports.
type Storage struct {
	Bucket   string `toml:"bucket"`
	Endpoint string `toml:"endpoint"`
	Region   string `toml:"region,omitempty"`
	Prefix   string `toml:"prefix,omitempty"`
}

// Deploy is line.toml's [deploy] table: what `line serve` builds for a
// release and how the deployer applies it. Zero means deploy is not set up.
type Deploy struct {
	Backend    string   `toml:"backend"`    // launchd or k8s
	URL        string   `toml:"url"`        // line API as the deployer reaches it
	Image      string   `toml:"image"`      // release image repository (container backends)
	Dockerfile string   `toml:"dockerfile"` // in the commit, default station/Dockerfile.line
	Dir        string   `toml:"dir"`        // launchd: deploy directory
	Domain     string   `toml:"domain"`     // launchd: e.g. gui/501
	Labels     []string `toml:"labels"`     // launchd: labels the deployer may kickstart
	API        string   `toml:"api"`        // k8s: API server URL
	TokenFile  string   `toml:"token_file"` // k8s: bearer token file
	Namespace  string   `toml:"namespace"`  // k8s
	Deployment string   `toml:"deployment"` // k8s
	Container  string   `toml:"container"`  // k8s
}

type Station struct {
	Name, Role, AgentID string
	KeyFile, TokenFile  string // <Dir>/stations/<name>/witness.key, token
	StateDir            string // <Dir>/stations/<name>/state
}

// Token reads the station's bearer token.
func (s Station) Token() (string, error) {
	b, err := os.ReadFile(s.TokenFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// lineFile is the on-disk shape of line.toml.
type lineFile struct {
	Port         int           `toml:"port"`
	Engine       string        `toml:"engine"`
	Image        string        `toml:"image"`
	Network      string        `toml:"network,omitempty"`
	URL          string        `toml:"url,omitempty"`
	ViceroyImage string        `toml:"viceroy_image,omitempty"`
	Stations     []stationFile `toml:"stations"`
	Deploy       *Deploy       `toml:"deploy,omitempty"`
	Storage      *Storage      `toml:"storage,omitempty"`
}

type stationFile struct {
	Name    string `toml:"name"`
	Role    string `toml:"role"`
	AgentID string `toml:"agent_id"`
}

// Names become path elements and TOML strings, so keep them boring.
const (
	// ConstitutionRef holds the line's constitution; only an approved
	// proposal or the owner's `station add` moves it, and the pre-receive
	// guard rejects pushes to it.
	ConstitutionRef = "refs/line/constitution"
	// ConstitutionBlob is the file name inside the ConstitutionRef tree.
	ConstitutionBlob = "constitution"
)

// StationURL is the station API address station containers use: line.toml's
// url when set, else the engine's host gateway on the line's port, which
// reaches only a serve published on the host, not one on a line network.
func (l Line) StationURL() string {
	if l.URL != "" {
		return l.URL
	}
	return fmt.Sprintf("http://host.containers.internal:%d", l.Port)
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func newLine(dir string, lf lineFile) Line {
	l := Line{
		Dir: dir, Port: lf.Port, Engine: lf.Engine, Image: lf.Image, Network: lf.Network, URL: lf.URL, ViceroyImage: lf.ViceroyImage,
		Remote:       filepath.Join(dir, "remote.git"),
		Constitution: filepath.Join(dir, "constitution.toml"),
	}
	if lf.Deploy != nil {
		l.Deploy = *lf.Deploy
	}
	if lf.Storage != nil {
		l.Storage = *lf.Storage
	}
	for _, s := range lf.Stations {
		sd := filepath.Join(dir, "stations", s.Name)
		l.Stations = append(l.Stations, Station{
			Name: s.Name, Role: s.Role, AgentID: s.AgentID,
			KeyFile: filepath.Join(sd, "witness.key"), TokenFile: filepath.Join(sd, "token"),
			StateDir: filepath.Join(sd, "state"),
		})
	}
	return l
}

// Init creates a line directory seeded from opts.Source's master.
func Init(ctx context.Context, opts InitOptions) (Line, error) {
	if opts.Dir == "" || opts.Source == "" {
		return Line{}, errors.New("line init: dir and source are required")
	}
	if opts.Engine == "" {
		opts.Engine = "docker"
	}
	if opts.Image == "" {
		opts.Image = "sweatshop-station:dev"
	}
	if opts.Port == 0 {
		opts.Port = 7468
	}
	if opts.Check == "" {
		opts.Check = "just ci"
	}
	if len(opts.Stations) == 0 {
		opts.Stations = []StationSpec{{"maker-1", "maker"}, {"andon-1", "andon"}}
	}
	if (opts.SovereignName == "") != (opts.SovereignDiscord == "") {
		return Line{}, errors.New("line init: sovereign name and discord ID go together")
	}
	if strings.ContainsFunc(opts.SovereignName, unicode.IsControl) {
		return Line{}, fmt.Errorf("line init: bad sovereign name %q", opts.SovereignName)
	}
	seen := map[string]bool{}
	for _, s := range opts.Stations {
		if !safeName.MatchString(s.Name) || !safeName.MatchString(s.Role) || seen[s.Name] {
			return Line{}, fmt.Errorf("line init: bad or duplicate station %q (role %q)", s.Name, s.Role)
		}
		seen[s.Name] = true
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return Line{}, err
	}
	source, err := filepath.Abs(opts.Source)
	if err != nil {
		return Line{}, err
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return Line{}, fmt.Errorf("line init: %s is not empty", dir)
	} else if err != nil && !os.IsNotExist(err) {
		return Line{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Line{}, err
	}

	lf := lineFile{Port: opts.Port, Engine: opts.Engine, Image: opts.Image}
	for _, s := range opts.Stations {
		lf.Stations = append(lf.Stations, stationFile{Name: s.Name, Role: s.Role, AgentID: s.Name})
	}
	l := newLine(dir, lf)

	var cons strings.Builder
	cons.WriteString("[trust]\nlevel = \"attest\"\n")
	for _, s := range l.Stations {
		pub, err := createStation(s)
		if err != nil {
			return Line{}, err
		}
		fmt.Fprintf(&cons, "\n[[trust.keys]]\nname = %q\npublic_key = %q\nroles = [%q]\n", s.Name, pub, s.Role)
	}
	fmt.Fprintf(&cons, "\n[runner]\nkind = \"container\"\nengine = %q\nimage = %q\n", opts.Engine, opts.Image)
	fmt.Fprintf(&cons, "\n[[checks.required]]\nname = \"ci\"\ncommand = %q\n", opts.Check)
	// The required check is defined by the candidate itself (justfile, and the
	// station image it runs in). Gate those paths so a maker cannot weaken
	// the check in the same commit it is judged by. M1 has no gemba station,
	// so in practice only the owner changes them, directly on the host.
	// acceptance/** holds the black-box tests that define "works"; only gemba
	// changes them, so makers cannot edit a test to match their code.
	cons.WriteString("\n[[guards.paths]]\npattern = \"justfile\"\nroles = [\"gemba\"]\n")
	cons.WriteString("\n[[guards.paths]]\npattern = \"station/**\"\nroles = [\"gemba\"]\n")
	cons.WriteString("\n[[guards.paths]]\npattern = \"acceptance/**\"\nroles = [\"gemba\"]\n")
	if opts.SovereignName != "" {
		fmt.Fprintf(&cons, "\n[sovereign]\nname = %q\ndiscord = %q\n", opts.SovereignName, opts.SovereignDiscord)
	}
	if opts.Channel != "" {
		fmt.Fprintf(&cons, "\n[channel]\nkind = \"discord\"\nchannel = %q\n", opts.Channel)
	}
	if err := os.WriteFile(l.Constitution, []byte(cons.String()), 0o644); err != nil {
		return Line{}, err
	}
	if _, _, err := constitution.Load(l.Constitution); err != nil {
		return Line{}, fmt.Errorf("line init: generated constitution is invalid: %w", err)
	}

	if err := (gitx.Repo{Dir: l.Remote}).CloneBranchBare(ctx, gitx.Repo{Dir: source}, "refs/heads/master"); err != nil {
		return Line{}, err
	}
	if err := guard.InstallHooks(l.Remote); err != nil {
		return Line{}, err
	}

	data, err := toml.Marshal(lf)
	if err != nil {
		return Line{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "line.toml"), data, 0o644); err != nil {
		return Line{}, err
	}
	return l, nil
}

// createStation makes the station's directory, key (0600), token (0600) and
// station.toml, and returns its encoded public key.
func createStation(s Station) (string, error) {
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(filepath.Dir(s.KeyFile), 0o700); err != nil {
		return "", err
	}
	pub, err := attest.GenerateKeyFile(s.KeyFile)
	if err != nil {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	if err := os.WriteFile(s.TokenFile, []byte(hex.EncodeToString(raw)+"\n"), 0o600); err != nil {
		return "", err
	}
	data, err := toml.Marshal(stationFile{Name: s.Name, Role: s.Role, AgentID: s.AgentID})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(s.KeyFile), "station.toml"), data, 0o644); err != nil {
		return "", err
	}
	return attest.EncodePublicKey(pub), nil
}

// replaceFile writes data to path through a temporary file and a rename.
func replaceFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// AddStation adds a station to an existing line directory: its key and token,
// station.toml, a line.toml entry and a trusted key bound to role in the
// constitution. Once refs/line/constitution exists the key is committed there
// (the owner path) and constitution.toml is rewritten as an export of the ref;
// before that only the file changes. A running `line serve` picks it up on
// restart.
func AddStation(dir string, spec StationSpec) (Station, error) {
	if !safeName.MatchString(spec.Name) {
		return Station{}, fmt.Errorf("station add: bad station name %q", spec.Name)
	}
	if !slices.Contains(constitution.Roles, spec.Role) {
		return Station{}, fmt.Errorf("station add: unknown role %q (want one of %s)", spec.Role, strings.Join(constitution.Roles, ", "))
	}
	l, err := Load(dir)
	if err != nil {
		return Station{}, err
	}
	for _, s := range l.Stations {
		if s.Name == spec.Name {
			return Station{}, fmt.Errorf("station add: station %q already exists", spec.Name)
		}
	}
	lineToml := filepath.Join(l.Dir, "line.toml")
	data, err := os.ReadFile(lineToml)
	if err != nil {
		return Station{}, err
	}
	var lf lineFile
	if err := toml.Unmarshal(data, &lf); err != nil {
		return Station{}, fmt.Errorf("%s: %w", lineToml, err)
	}
	repo := gitx.Repo{Dir: l.Remote}
	ctx := context.Background()
	onRef, err := repo.RefExists(ctx, ConstitutionRef)
	if err != nil {
		return Station{}, err
	}
	var consData []byte
	var head string
	if onRef {
		if head, err = repo.Ref(ctx, ConstitutionRef); err != nil {
			return Station{}, err
		}
		consData, err = repo.ReadBlob(ctx, head+":"+ConstitutionBlob)
	} else {
		consData, err = os.ReadFile(l.Constitution)
	}
	if err != nil {
		return Station{}, err
	}
	sd := filepath.Join(l.Dir, "stations", spec.Name)
	if _, err := os.Lstat(sd); err == nil {
		return Station{}, fmt.Errorf("station add: %s already exists", sd)
	}
	lf.Stations = append(lf.Stations, stationFile{Name: spec.Name, Role: spec.Role, AgentID: spec.Name})
	st := newLine(l.Dir, lf).Stations[len(lf.Stations)-1]

	pub, err := createStation(st)
	if err == nil {
		err = l.addTrust(ctx, repo, consData, head, st, pub, lf, data)
	}
	if err != nil {
		os.RemoveAll(sd)
		return Station{}, err
	}
	return st, nil
}

// addTrust appends st's trusted key to the constitution cons (the ref's
// content at head, or the file's when head is "") and the station to
// line.toml, checking that the new constitution still parses. oldLineToml is
// restored if the constitution cannot be stored.
func (l Line) addTrust(ctx context.Context, repo gitx.Repo, cons []byte, head string, st Station, pub string, lf lineFile, oldLineToml []byte) error {
	var b strings.Builder
	b.Write(cons)
	if len(cons) > 0 && cons[len(cons)-1] != '\n' {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "\n[[trust.keys]]\nname = %q\npublic_key = %q\nroles = [%q]\n", st.Name, pub, st.Role)
	if _, err := constitution.Parse([]byte(b.String())); err != nil {
		return fmt.Errorf("station add: new constitution is invalid: %w", err)
	}
	out, err := toml.Marshal(lf)
	if err != nil {
		return err
	}
	lineToml := filepath.Join(l.Dir, "line.toml")
	if err := replaceFile(lineToml, out, 0o644); err != nil {
		return err
	}
	if head != "" {
		msg := fmt.Sprintf("constitution: station add %s (%s), by the owner", st.Name, st.Role)
		_, err = CommitConstitution(ctx, repo, []byte(b.String()), head, msg, "owner")
	}
	if err == nil {
		err = replaceFile(l.Constitution, []byte(b.String()), 0o644)
	}
	if err != nil {
		// Put line.toml back so the line is not half-edited.
		replaceFile(lineToml, oldLineToml, 0o644)
		return err
	}
	return nil
}

// CommitConstitution commits data on ref's chain and moves ConstitutionRef
// from old ("" when it does not exist yet).
func CommitConstitution(ctx context.Context, repo gitx.Repo, data []byte, old, msg, who string) (string, error) {
	blob, err := repo.WriteBlob(ctx, data)
	if err != nil {
		return "", err
	}
	tree, err := repo.WriteTreeWithBlob(ctx, ConstitutionBlob, blob)
	if err != nil {
		return "", err
	}
	commit, err := repo.CommitTree(ctx, tree, old, msg, who)
	if err != nil {
		return "", err
	}
	return commit, repo.UpdateRef(ctx, ConstitutionRef, commit, old)
}

// Load reads a line directory and checks that every station's secrets exist
// and are private to the owner.
func Load(dir string) (Line, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Line{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "line.toml"))
	if err != nil {
		return Line{}, err
	}
	var lf lineFile
	d := toml.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(&lf); err != nil {
		return Line{}, fmt.Errorf("%s/line.toml: %w", dir, err)
	}
	l := newLine(dir, lf)
	if l.Network != "" && !safeName.MatchString(l.Network) {
		return Line{}, fmt.Errorf("line.toml: bad network name %q", l.Network)
	}
	if l.URL != "" {
		u, err := url.Parse(l.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Line{}, fmt.Errorf("line.toml: bad url %q (want http://host:port)", l.URL)
		}
	}
	if l.Storage != (Storage{}) && (l.Storage.Bucket == "" || l.Storage.Endpoint == "") {
		return Line{}, errors.New("line.toml: [storage] needs bucket and endpoint")
	}
	for _, s := range l.Stations {
		if !safeName.MatchString(s.Name) {
			return Line{}, fmt.Errorf("line.toml: bad station name %q", s.Name)
		}
		for _, f := range []string{s.KeyFile, s.TokenFile} {
			fi, err := os.Stat(f)
			if err != nil {
				return Line{}, err
			}
			if fi.Mode().Perm() != 0o600 {
				return Line{}, fmt.Errorf("%s: mode %o, want 0600", f, fi.Mode().Perm())
			}
		}
	}
	return l, nil
}
