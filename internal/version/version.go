// Package version holds the agent's version, stamped in at build time.
package version

// Version is the package version. Release builds set it from the git tag:
//
//	go build -ldflags "-X github.com/RCRaceHub/EventScreenDisplay/internal/version.Version=0.7.1"
//
// packaging/build-deb.sh does this, so the default here is only what a plain
// "go build" or a branch build reports.
var Version = "0.8.0"

// API is the HTTP API's major version (the "v1" in /api/v1).
const API = "1"
