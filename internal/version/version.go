package version

import "fmt"

// Set bằng ldflags: -X mind-runner/internal/version.Version=...
var Version = "dev"
var Commit = ""

func String() string {
	if Commit == "" {
		return fmt.Sprintf("mind-runner %s", Version)
	}
	return fmt.Sprintf("mind-runner %s (%s)", Version, Commit)
}
