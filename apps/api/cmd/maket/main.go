// Command maket is the CLI for uploading and managing Prototypes. The
// commands live in internal/cli; run `maket help` for the list.
package main

import (
	"os"

	"github.com/itsgitz/maket/apps/api/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.OSEnv(version)))
}
