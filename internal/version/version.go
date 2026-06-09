// Package version exposes build-time version metadata.
//
// Values are overridable via -ldflags at build time, e.g.:
//
//	go build -ldflags "-X github.com/gobobr/veriproc/internal/version.Version=v0.1.0 \
//	                  -X github.com/gobobr/veriproc/internal/version.Commit=abcdef \
//	                  -X github.com/gobobr/veriproc/internal/version.BuildDate=2026-05-05T00:00:00Z" \
//	  ./cmd/veriprocd
package version

var (
	// Version is the semantic version of the build.
	Version = "0.1.0-dev"
	// Commit is the VCS commit hash for the build.
	Commit = "unknown"
	// BuildDate is the RFC3339 timestamp of the build.
	BuildDate = "unknown"
	// APIVersion identifies the REST API contract version served by this build.
	APIVersion = "v1"
)

// Info bundles version metadata for serialization.
type Info struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	BuildDate  string `json:"build_date"`
	APIVersion string `json:"api_version"`
}

// Get returns the current build's version info.
func Get() Info {
	return Info{
		Version:    Version,
		Commit:     Commit,
		BuildDate:  BuildDate,
		APIVersion: APIVersion,
	}
}
