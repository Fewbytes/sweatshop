// Package version holds the build-time version identity shared by agentsh
// and agentshd. Both binaries report the same values so a client can detect
// a stale daemon (built from a different tag) that speaks an older RPC
// protocol — rpc.Version already gates that daemon-side; this lets the
// client explain *why* before the protocol check even runs.
package version

// Version and Commit are overridden at build time via
// -ldflags "-X github.com/Fewbytes/sweatshop/agentsh/internal/version.Version=... -X .../version.Commit=..."
// (see .goreleaser.yml and justfile). Locally built binaries report "dev".
var (
	Version = "dev"
	Commit  = "none"
)

// String renders the version for --version output and doctor reports.
func String() string {
	if Commit == "" || Commit == "none" {
		return Version
	}
	return Version + " (" + Commit + ")"
}
