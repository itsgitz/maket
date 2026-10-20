package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/itsgitz/maket/apps/api/internal/apitypes"
)

// noPositional fails when a command that takes no arguments got some.
func noPositional(cmd string, pos []string) error {
	if len(pos) > 0 {
		return usageError("%s takes no arguments, got %q (see `maket %s --help`)", cmd, pos[0], cmd)
	}
	return nil
}

func (r *runner) login(args []string) error {
	fs := newFlagSet("login")
	urlFlag := fs.String("url", "", "API address")
	pos, err := parseArgs(fs, "login", args)
	if err != nil {
		return err
	}
	if err := noPositional("login", pos); err != nil {
		return err
	}
	if *urlFlag != "" {
		if _, err := normalizeURL(*urlFlag); err != nil {
			return err
		}
	}
	s, err := r.resolveSettings(*urlFlag)
	if err != nil {
		return err
	}

	key, err := r.readKey()
	if err != nil {
		return err
	}
	s.Key = key
	var me apitypes.Me
	if err := r.do(context.Background(), s, request{method: http.MethodGet, path: "/api/me", want: http.StatusOK, out: &me}); err != nil {
		return err
	}

	path, err := r.configPath()
	if err != nil {
		return err
	}
	if err := writeConfig(path, configFile{APIURL: s.URL, APIKey: s.Key}); err != nil {
		return err
	}
	fmt.Fprintf(r.env.Stderr, "Signed in as %s (%s)\n", me.GitHubLogin, s.URL)
	return nil
}

// readKey reads the API Key: hidden from a terminal, else one line of stdin.
func (r *runner) readKey() (string, error) {
	var key string
	if r.env.StdinIsTTY() {
		fmt.Fprint(r.env.Stderr, "API Key: ")
		k, err := r.env.ReadPassword()
		fmt.Fprintln(r.env.Stderr)
		if err != nil {
			return "", localError("read the API Key: %v", err)
		}
		key = k
	} else {
		line, err := bufio.NewReader(r.env.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", localError("read the API Key from stdin: %v", err)
		}
		key = line
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", usageError("no API Key given: create one in the dashboard under API Keys")
	}
	return key, nil
}

func (r *runner) logout(args []string) error {
	fs := newFlagSet("logout")
	pos, err := parseArgs(fs, "logout", args)
	if err != nil {
		return err
	}
	if err := noPositional("logout", pos); err != nil {
		return err
	}
	path, err := r.configPath()
	if err != nil {
		return err
	}
	c, err := readConfig(path)
	if err != nil {
		return err
	}
	if c.APIKey != "" {
		c.APIKey = ""
		if err := writeConfig(path, c); err != nil {
			return err
		}
	}
	fmt.Fprintln(r.env.Stderr, "Signed out")
	return nil
}

func (r *runner) whoami(args []string) error {
	fs := newFlagSet("whoami")
	jsonOut := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, "whoami", args)
	if err != nil {
		return err
	}
	r.json = *jsonOut
	if err := noPositional("whoami", pos); err != nil {
		return err
	}
	s, err := r.signedIn()
	if err != nil {
		return err
	}
	var me apitypes.Me
	if err := r.do(context.Background(), s, request{method: http.MethodGet, path: "/api/me", want: http.StatusOK, out: &me}); err != nil {
		return err
	}
	if r.json {
		r.writeJSON(me)
	} else {
		fmt.Fprintln(r.env.Stdout, me.GitHubLogin)
	}
	return nil
}

// signedIn resolves the settings and requires an API Key.
func (r *runner) signedIn() (settings, error) {
	s, err := r.resolveSettings("")
	if err != nil {
		return s, err
	}
	return s, requireKey(s)
}

func (r *runner) push(args []string) error {
	fs := newFlagSet("push")
	name := fs.String("name", "", "Prototype name")
	spa := fs.Bool("spa", false, "SPA mode")
	slug := fs.String("slug", "", "existing Prototype")
	jsonOut := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, "push", args)
	if err != nil {
		return err
	}
	r.json = *jsonOut
	if len(pos) != 1 {
		return usageError("push takes one path: a folder, an .html file or a .zip (see `maket push --help`)")
	}
	if flagGiven(fs, "slug") && strings.TrimSpace(*slug) == "" {
		return usageError("--slug needs a Slug")
	}
	info, err := checkPushPath(pos[0])
	if err != nil {
		return err
	}
	s, err := r.signedIn()
	if err != nil {
		return err
	}
	src, err := newUploadSource(pos[0], info)
	if err != nil {
		return err
	}

	var fields uploadFields
	if flagGiven(fs, "name") {
		fields.name = name
	}
	if flagGiven(fs, "spa") {
		fields.spa = spa
	}
	body, contentType, err := multipartBody(src, fields)
	if err != nil {
		return err
	}
	req := request{method: http.MethodPost, path: "/api/prototypes", body: body, contentType: contentType, want: http.StatusCreated}
	if flagGiven(fs, "slug") {
		req.method, req.path, req.want = http.MethodPut, "/api/prototypes/"+url.PathEscape(strings.TrimSpace(*slug)), http.StatusOK
	}
	if r.env.StderrIsTTY() {
		fmt.Fprintf(r.env.Stderr, "Uploading %s (%s)...\n", src.filename, humanSize(src.size))
	}
	var p apitypes.Prototype
	req.out = &p
	if err := r.do(context.Background(), s, req); err != nil {
		return err
	}
	if r.json {
		r.writeJSON(p)
	} else {
		fmt.Fprintln(r.env.Stdout, p.URL)
	}
	return nil
}

func (r *runner) ls(args []string) error {
	fs := newFlagSet("ls")
	jsonOut := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, "ls", args)
	if err != nil {
		return err
	}
	r.json = *jsonOut
	if err := noPositional("ls", pos); err != nil {
		return err
	}
	s, err := r.signedIn()
	if err != nil {
		return err
	}
	var list apitypes.PrototypeList
	if err := r.do(context.Background(), s, request{method: http.MethodGet, path: "/api/prototypes", want: http.StatusOK, out: &list}); err != nil {
		return err
	}
	if list.Prototypes == nil {
		list.Prototypes = []apitypes.Prototype{}
	}
	if r.json {
		r.writeJSON(list)
		return nil
	}
	if len(list.Prototypes) == 0 {
		fmt.Fprintln(r.env.Stderr, "No prototypes yet")
		return nil
	}
	writeTable(r.env.Stdout, list.Prototypes)
	fmt.Fprintf(r.env.Stderr, "%s of %s used\n", humanSize(list.Usage.UsedBytes), humanSize(list.Usage.QuotaBytes))
	return nil
}

func (r *runner) rm(args []string) error {
	fs := newFlagSet("rm")
	yes := fs.Bool("yes", false, "delete without asking")
	jsonOut := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, "rm", args)
	if err != nil {
		return err
	}
	r.json = *jsonOut
	if len(pos) != 1 || strings.TrimSpace(pos[0]) == "" {
		return usageError("rm takes one Slug (see `maket rm --help`)")
	}
	slug := strings.TrimSpace(pos[0])
	s, err := r.signedIn()
	if err != nil {
		return err
	}
	if !*yes {
		if !r.env.StdinIsTTY() || !r.env.StderrIsTTY() {
			return usageError("refusing to delete %s without confirmation: pass --yes", slug)
		}
		fmt.Fprintf(r.env.Stderr, "Delete %s? [y/N] ", slug)
		answer, _ := bufio.NewReader(r.env.Stdin).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
		default:
			e := newError(exitFailure, codeAborted, "Aborted")
			e.bare = true
			return e
		}
	}
	if err := r.do(context.Background(), s, request{method: http.MethodDelete, path: "/api/prototypes/" + url.PathEscape(slug), want: http.StatusNoContent}); err != nil {
		return err
	}
	if r.json {
		r.writeJSON(struct {
			Slug    string `json:"slug"`
			Deleted bool   `json:"deleted"`
		}{slug, true})
	}
	fmt.Fprintf(r.env.Stderr, "Deleted %s\n", slug)
	return nil
}
