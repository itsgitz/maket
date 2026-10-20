package cli

import (
	"fmt"
	"os"
	"os/signal"

	"golang.org/x/term"
)

// OSEnv is the Env of the running process.
func OSEnv(version string) Env {
	return Env{
		Stdin:        os.Stdin,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		Getenv:       os.Getenv,
		ConfigDir:    DefaultConfigDir,
		StdinIsTTY:   func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		StderrIsTTY:  func() bool { return term.IsTerminal(int(os.Stderr.Fd())) },
		ReadPassword: readPassword,
		HTTPClient:   NewHTTPClient(),
		Version:      version,
	}
}

// readPassword reads a line from the terminal on stdin without echo. Ctrl-C
// restores the terminal before exiting, so echo is not left off.
func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return "", err
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	done := make(chan struct{})
	defer func() {
		signal.Stop(sig)
		close(done)
	}()
	go func() {
		select {
		case <-sig:
			term.Restore(fd, state)
			fmt.Fprintln(os.Stderr)
			os.Exit(exitInterrupted)
		case <-done:
		}
	}()
	b, err := term.ReadPassword(fd)
	return string(b), err
}
