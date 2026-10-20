package cli

import (
	"errors"
	"flag"
	"io"
)

// newFlagSet returns a FlagSet that reports errors to the caller instead of
// printing or exiting.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// parseArgs parses args with fs, allowing flags before, between and after
// positional arguments, and returns the positional ones. Everything after
// "--" is positional. -h and --help return errHelp; other bad flags a usage
// error.
func parseArgs(fs *flag.FlagSet, cmd string, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, errHelp
			}
			return nil, usageError("%v (see `maket %s --help`)", err, cmd)
		}
		rest := fs.Args()
		if consumed := args[:len(args)-len(rest)]; len(consumed) > 0 && consumed[len(consumed)-1] == "--" {
			return append(pos, rest...), nil
		}
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// flagGiven reports whether the flag called name was set on the command line.
func flagGiven(fs *flag.FlagSet, name string) bool {
	given := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}
