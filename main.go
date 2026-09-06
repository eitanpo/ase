// Command agentry renders a Claude Code session log to the terminal.
package main

import (
	"os"

	"github.com/eitanpo/agentry/internal/cli"
)

// Version is the base version. `make build`/`make install` override it via -ldflags with a
// UTC build timestamp appended as semver build metadata (e.g. 0.1.0+20260527T131005Z);
// release builds set it from the git tag. Plain `go build`/`go install` use this bare value.
var Version = "0.25.0"

func main() {
	os.Exit(cli.Execute(Version))
}
