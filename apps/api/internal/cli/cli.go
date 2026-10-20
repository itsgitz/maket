// Package cli implements the maket command-line tool: signing in with an API
// Key, pushing a file or folder as a Prototype, listing and deleting
// Prototypes.
//
// Everything the commands touch outside the process (standard streams,
// environment, config directory, terminal, HTTP) comes in through Env, so
// commands run unchanged in tests.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/itsgitz/maket/apps/api/internal/apitypes"
)

// Exit codes. They are part of the CLI's documented interface.
const (
	exitOK          = 0
	exitFailure     = 1 // server error, network, rejected upload, quota, aborted
	exitUsage       = 2 // bad flags or arguments, unsupported file type
	exitAuth        = 3 // not signed in, or the API Key was rejected
	exitNotFound    = 4 // no such Prototype
	exitInterrupted = 130
)

// Codes of CLI-side errors, as printed in the ErrorResponse under --json.
// Server errors keep the server's code.
const (
	codeUsage           = "usage"
	codeNotSignedIn     = "not_signed_in"
	codeNetwork         = "network"
	codeInvalidResponse = "invalid_response"
	codeAborted         = "aborted"
	codeLocal           = "local_error" // reading a file or the config failed
)

// Env is the outside world as the CLI sees it.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(key string) string
	// ConfigDir returns the directory holding config.json.
	ConfigDir func() (string, error)
	// StdinIsTTY and StderrIsTTY report whether those streams are terminals.
	StdinIsTTY  func() bool
	StderrIsTTY func() bool
	// ReadPassword reads one line from the terminal without echoing it.
	ReadPassword func() (string, error)
	HTTPClient   *http.Client
	Version      string // reported by `maket version` and in the User-Agent
}

// exitError ends a command: a message for stderr, the exit code, and the
// API error shape printed on stdout under --json.
type exitError struct {
	exit int
	body apitypes.Error
	// json, when set, is printed under --json instead of body.
	json *apitypes.Error
	// bare prints the message without the "maket: " prefix.
	bare bool
}

func (e *exitError) Error() string { return e.body.Message }

func newError(exit int, code, format string, a ...any) *exitError {
	return &exitError{exit: exit, body: apitypes.Error{Code: code, Message: fmt.Sprintf(format, a...)}}
}

func usageError(format string, a ...any) *exitError {
	return newError(exitUsage, codeUsage, format, a...)
}

func localError(format string, a ...any) *exitError {
	return newError(exitFailure, codeLocal, format, a...)
}

// errHelp means a command's usage was printed on request.
var errHelp = errors.New("help requested")

// runner carries the state of one invocation.
type runner struct {
	env    Env
	json   bool // --json: errors are also printed as JSON on stdout
	warned bool // the plain-http warning was printed
}

// Run runs the CLI with args (without the program name) and returns the
// process exit code.
func Run(args []string, env Env) int {
	r := &runner{env: env}
	if len(args) == 0 {
		fmt.Fprint(env.Stderr, mainUsage)
		return exitUsage
	}
	name, rest := args[0], args[1:]
	switch name {
	case "-h", "-help", "--help":
		fmt.Fprint(env.Stdout, mainUsage)
		return exitOK
	case "help":
		if len(rest) > 0 {
			if c, ok := commands[rest[0]]; ok {
				fmt.Fprint(env.Stdout, c.usage)
				return exitOK
			}
			return r.finish(usageError("unknown command %q: run `maket help`", rest[0]))
		}
		fmt.Fprint(env.Stdout, mainUsage)
		return exitOK
	case "version", "-version", "--version":
		fmt.Fprintln(env.Stdout, env.Version)
		return exitOK
	}
	c, ok := commands[name]
	if !ok {
		return r.finish(usageError("unknown command %q: run `maket help`", name))
	}
	if c.json {
		r.json = argsWantJSON(rest)
	}
	err := c.run(r, rest)
	if errors.Is(err, errHelp) {
		fmt.Fprint(env.Stdout, c.usage)
		return exitOK
	}
	return r.finish(err)
}

// finish reports err and returns the exit code.
func (r *runner) finish(err error) int {
	if err == nil {
		return exitOK
	}
	var e *exitError
	if !errors.As(err, &e) {
		e = localError("%v", err)
	}
	if e.bare {
		fmt.Fprintln(r.env.Stderr, e.body.Message)
	} else {
		fmt.Fprintln(r.env.Stderr, "maket: "+e.body.Message)
	}
	if d := e.body.Details; d != nil {
		for _, p := range d.Paths {
			fmt.Fprintln(r.env.Stderr, "  "+p)
		}
		if d.More > 0 {
			fmt.Fprintf(r.env.Stderr, "  ...and %d more\n", d.More)
		}
	}
	if r.json {
		body := e.body
		if e.json != nil {
			body = *e.json
		}
		r.writeJSON(apitypes.ErrorResponse{Error: body})
	}
	return e.exit
}

// writeJSON prints v on stdout, indented.
func (r *runner) writeJSON(v any) {
	enc := json.NewEncoder(r.env.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// argsWantJSON reports whether --json is among args, for errors raised
// before or while the flags are parsed.
func argsWantJSON(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		switch strings.TrimLeft(a, "-") {
		case "json", "json=true", "json=1":
			if strings.HasPrefix(a, "-") {
				return true
			}
		}
	}
	return false
}

type command struct {
	usage string
	json  bool // takes --json
	run   func(r *runner, args []string) error
}

var commands map[string]command

func init() {
	commands = map[string]command{
		"login":  {usage: loginUsage, run: (*runner).login},
		"logout": {usage: logoutUsage, run: (*runner).logout},
		"whoami": {usage: whoamiUsage, json: true, run: (*runner).whoami},
		"push":   {usage: pushUsage, json: true, run: (*runner).push},
		"ls":     {usage: lsUsage, json: true, run: (*runner).ls},
		"rm":     {usage: rmUsage, json: true, run: (*runner).rm},
	}
}

const mainUsage = `maket uploads static frontends as Prototypes and prints their Preview URL.

Usage:
  maket <command> [flags] [args]

Commands:
  login     Sign in with an API Key
  logout    Remove the saved API Key
  whoami    Print the GitHub login you are signed in as
  push      Upload an .html file, a .zip or a folder as a Prototype
  ls        List your Prototypes
  rm        Delete a Prototype
  version   Print the CLI version
  help      Print help for a command

Environment:
  MAKET_API_KEY   API Key to use instead of the saved one
  MAKET_API_URL   API address to use instead of the saved one
                  (default ` + defaultAPIURL + `)

Run 'maket <command> --help' for a command's flags.
`

const loginUsage = `Usage: maket login [--url URL]

Sign in with an API Key, created in the dashboard under API Keys. On a
terminal the key is asked for without echoing it; otherwise one line is read
from stdin:

  echo "$MAKET_KEY" | maket login

The key is checked against the API, then saved with the API address in the
config file.

Flags:
  --url URL   API address (default: MAKET_API_URL, the saved address, or
              ` + defaultAPIURL + `)
`

const logoutUsage = `Usage: maket logout

Remove the saved API Key from the config file. The saved API address is kept.
`

const whoamiUsage = `Usage: maket whoami [--json]

Print the GitHub login of the account the API Key belongs to.

Flags:
  --json   Print the account as JSON
`

const pushUsage = `Usage: maket push <path> [--name NAME] [--spa] [--slug SLUG] [--json]

Upload a Prototype and print its Preview URL. <path> is an .html or .htm
file, a .zip archive, or a folder; a folder is zipped first, leaving out
files and folders whose name starts with "." and symlinks.

Flags:
  --name NAME   Prototype name (default for a new Prototype: the file or
                folder name)
  --spa         Turn on SPA mode: unknown pages serve index.html
                (--spa=false turns it off)
  --slug SLUG   Upload a new Version of an existing Prototype instead of
                creating one; name and SPA mode are kept unless given
  --json        Print the Prototype as JSON
`

const lsUsage = `Usage: maket ls [--json]

List your Prototypes, most recently updated first, and your storage use.

Flags:
  --json   Print the list as JSON
`

const rmUsage = `Usage: maket rm <slug> [--yes] [--json]

Delete a Prototype and its Preview. Asks for confirmation on a terminal;
elsewhere --yes is required.

Flags:
  --yes    Delete without asking
  --json   Print the result as JSON
`
