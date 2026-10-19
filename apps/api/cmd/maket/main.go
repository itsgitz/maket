// Command maket is the CLI for uploading and managing Prototypes.
// The commands arrive in milestone 6; for now it only reports its version.
package main

import (
	"fmt"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(version)
		return
	}
	fmt.Fprintln(os.Stderr, "usage: maket version")
	os.Exit(2)
}
