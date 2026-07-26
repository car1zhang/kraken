package util

// BuildSHA is stamped at link time by the Makefile:
//
//	-ldflags "-X kraken/internal/util.BuildSHA=<short sha>"
//
// It stays empty under a plain `go build` / `go test`, which is why Build()
// carries a fallback rather than surfacing an empty string to the UI.
var BuildSHA = ""

// Build returns the short commit the running binary was built from, or "dev"
// for an unstamped build.
func Build() string {
	if BuildSHA == "" {
		return "dev"
	}
	return BuildSHA
}
