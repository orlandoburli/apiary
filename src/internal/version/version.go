// Package version holds the single source of truth for the Apiary version string.
// The Version variable is overwritten at build time via -ldflags:
//
//	go build -ldflags "-X github.com/orlandoburli/apiary/internal/version.Version=1.2.3"
package version

// Version is the current release. Overridden by the Makefile at build time.
var Version = "0.1.0-dev"
