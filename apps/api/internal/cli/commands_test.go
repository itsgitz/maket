package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/apitypes"
)

const testKey = "mk_testkey"

// seenRequest is what the fake API recorded about a request.
type seenRequest struct {
	Method, Path, Auth, UserAgent string
	Fields                        map[string]string // multipart form fields
	Filename                      string            // multipart file name
	File                          []byte
}

// fakeAPI mimics the maket API: handle answers each request, after it has
// been recorded.
type fakeAPI struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []seenRequest
	handle func(w http.ResponseWriter, r *http.Request)
}

func newFakeAPI(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *fakeAPI {
	f := &fakeAPI{t: t, handle: handle}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen := seenRequest{Method: r.Method, Path: r.URL.EscapedPath(), Auth: r.Header.Get("Authorization"), UserAgent: r.UserAgent()}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("parse multipart: %v", err)
			}
			seen.Fields = map[string]string{}
			for k, v := range r.MultipartForm.Value {
				seen.Fields[k] = v[0]
			}
			if fh := r.MultipartForm.File["file"]; len(fh) == 1 {
				seen.Filename = fh[0].Filename
				fr, _ := fh[0].Open()
				seen.File, _ = io.ReadAll(fr)
				fr.Close()
			}
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, seen)
		handle := f.handle
		f.mu.Unlock()
		handle(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) only() seenRequest {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) != 1 {
		f.t.Fatalf("got %d requests, want 1: %+v", len(f.reqs), f.reqs)
	}
	return f.reqs[0]
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func reply(status int, v any) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if v != nil {
			json.NewEncoder(w).Encode(v)
		}
	}
}

func apiError(status int, code, msg string, details *apitypes.ErrorDetails) func(http.ResponseWriter, *http.Request) {
	return reply(status, apitypes.ErrorResponse{Error: apitypes.Error{Code: code, Message: msg, Details: details}})
}

// harness runs the CLI with buffers for its streams.
type harness struct {
	t         *testing.T
	env       map[string]string
	configDir string
	stdin     string
	stdinTTY  bool
	stderrTTY bool
	password  string
	client    *http.Client
}

func newHarness(t *testing.T, apiURL string) *harness {
	return &harness{
		t:         t,
		env:       map[string]string{"MAKET_API_URL": apiURL, "MAKET_API_KEY": testKey},
		configDir: filepath.Join(t.TempDir(), "maket"),
	}
}

type result struct {
	code           int
	stdout, stderr string
}

func (h *harness) run(args ...string) result {
	h.t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, Env{
		Stdin:        strings.NewReader(h.stdin),
		Stdout:       &stdout,
		Stderr:       &stderr,
		Getenv:       func(k string) string { return h.env[k] },
		ConfigDir:    func() (string, error) { return h.configDir, nil },
		StdinIsTTY:   func() bool { return h.stdinTTY },
		StderrIsTTY:  func() bool { return h.stderrTTY },
		ReadPassword: func() (string, error) { return h.password, nil },
		HTTPClient:   cmp.Or(h.client, http.DefaultClient),
		Version:      "1.2.3",
	})
	return result{code, stdout.String(), stderr.String()}
}

func (res result) want(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	if res.code != code || res.stdout != stdout || res.stderr != stderr {
		t.Errorf("got exit %d\nstdout: %q\nstderr: %q\nwant exit %d\nstdout: %q\nstderr: %q",
			res.code, res.stdout, res.stderr, code, stdout, stderr)
	}
}

func checkRequest(t *testing.T, got seenRequest, method, path string) {
	t.Helper()
	if got.Method != method || got.Path != path {
		t.Errorf("request %s %s, want %s %s", got.Method, got.Path, method, path)
	}
	if got.Auth != "Bearer "+testKey {
		t.Errorf("Authorization %q", got.Auth)
	}
	if got.UserAgent != "maket-cli/1.2.3" {
		t.Errorf("User-Agent %q", got.UserAgent)
	}
}

var me = apitypes.Me{ID: 7, GitHubLogin: "octo", Name: "Octo Cat", AuthMethod: "api_key"}

func sampleProto(slug string) apitypes.Prototype {
	at := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)
	return apitypes.Prototype{Slug: slug, Name: "Demo", URL: "https://" + slug + ".cloudshare.my.id", Version: 2,
		SizeBytes: 15 * 1024, FileCount: 3, CreatedAt: at, UpdatedAt: at}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func TestHelpAndVersion(t *testing.T) {
	h := newHarness(t, "http://unused.localhost")
	h.run().want(t, exitUsage, "", mainUsage)
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		h.run(args...).want(t, exitOK, mainUsage, "")
	}
	h.run("help", "push").want(t, exitOK, pushUsage, "")
	for _, c := range []string{"login", "logout", "whoami", "push", "ls", "rm"} {
		h.run(c, "-h").want(t, exitOK, commands[c].usage, "")
		h.run(c, "x", "--help").want(t, exitOK, commands[c].usage, "")
	}
	h.run("version").want(t, exitOK, "1.2.3\n", "")
	h.run("--version").want(t, exitOK, "1.2.3\n", "")
	res := h.run("frobnicate")
	if res.code != exitUsage || !strings.HasPrefix(res.stderr, "maket: unknown command") {
		t.Errorf("unknown command: %+v", res)
	}
}

func TestLoginPipedStdin(t *testing.T) {
	api := newFakeAPI(t, reply(http.StatusOK, me))
	h := newHarness(t, "")
	h.env = map[string]string{}
	h.stdin = "  " + testKey + "  \nignored\n"
	h.run("login", "--url", api.srv.URL+"/").want(t, exitOK, "", "Signed in as octo ("+api.srv.URL+")\n")
	checkRequest(t, api.only(), "GET", "/api/me")

	c, err := readConfig(filepath.Join(h.configDir, "config.json"))
	if err != nil || c != (configFile{APIURL: api.srv.URL, APIKey: testKey}) {
		t.Errorf("config %+v, %v", c, err)
	}

	// Now signed in from the file alone.
	h.run("whoami").want(t, exitOK, "octo\n", "")
}

func TestLoginTTYUsesHiddenPrompt(t *testing.T) {
	api := newFakeAPI(t, reply(http.StatusOK, me))
	h := newHarness(t, api.srv.URL)
	h.env = map[string]string{"MAKET_API_URL": api.srv.URL}
	h.stdinTTY, h.password = true, testKey
	h.run("login").want(t, exitOK, "", "API Key: \nSigned in as octo ("+api.srv.URL+")\n")
	checkRequest(t, api.only(), "GET", "/api/me")
}

func TestLoginRejected(t *testing.T) {
	api := newFakeAPI(t, apiError(http.StatusUnauthorized, "unauthorized", "missing or invalid credentials", nil))
	h := newHarness(t, api.srv.URL)
	h.stdin = "mk_wrong\n"
	h.run("login").want(t, exitAuth, "", "maket: the API Key was rejected (revoked or wrong): run `maket login`\n")
	if _, err := os.Stat(filepath.Join(h.configDir, "config.json")); !os.IsNotExist(err) {
		t.Errorf("config written after a rejected key: %v", err)
	}
}

func TestLoginEmptyKey(t *testing.T) {
	api := newFakeAPI(t, reply(http.StatusOK, me))
	h := newHarness(t, api.srv.URL)
	if res := h.run("login"); res.code != exitUsage || api.count() != 0 {
		t.Errorf("empty stdin: %+v, %d requests", res, api.count())
	}
}

func TestLoginInsecureWarning(t *testing.T) {
	api := newFakeAPI(t, reply(http.StatusOK, me))
	h := newHarness(t, "")
	h.env = map[string]string{}
	h.stdin = testKey + "\n"
	// Every host resolves to the fake API.
	h.client = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, api.srv.Listener.Addr().String())
		},
	}}
	h.run("login", "--url", "http://maket.example").want(t, exitOK, "",
		"maket: warning: http://maket.example is plain http, so the API Key is sent unencrypted\n"+
			"Signed in as octo (http://maket.example)\n")
	h.run("whoami").want(t, exitOK, "octo\n", "maket: warning: http://maket.example is plain http, so the API Key is sent unencrypted\n")
	// *.localhost is this machine: no warning.
	h.env["MAKET_API_URL"] = "http://app.localhost:8080"
	h.run("whoami").want(t, exitOK, "octo\n", "")
}

func TestLogout(t *testing.T) {
	h := newHarness(t, "")
	path := filepath.Join(h.configDir, "config.json")
	h.run("logout").want(t, exitOK, "", "Signed out\n") // not signed in: still fine
	if err := writeConfig(path, configFile{APIURL: "https://a.example", APIKey: testKey}); err != nil {
		t.Fatal(err)
	}
	h.run("logout").want(t, exitOK, "", "Signed out\n")
	c, err := readConfig(path)
	if err != nil || c != (configFile{APIURL: "https://a.example"}) {
		t.Errorf("config after logout %+v, %v", c, err)
	}
}

func TestWhoami(t *testing.T) {
	api := newFakeAPI(t, reply(http.StatusOK, me))
	h := newHarness(t, api.srv.URL)
	h.run("whoami").want(t, exitOK, "octo\n", "")
	h.run("whoami", "--json").want(t, exitOK, jsonOf(t, me), "")
}

func TestNotSignedIn(t *testing.T) {
	api := newFakeAPI(t, reply(http.StatusOK, me))
	h := newHarness(t, api.srv.URL)
	delete(h.env, "MAKET_API_KEY")
	msg := "maket: not signed in: run `maket login` or set MAKET_API_KEY\n"
	h.run("whoami").want(t, exitAuth, "", msg)
	h.run("ls", "--json").want(t, exitAuth, jsonOf(t, apitypes.ErrorResponse{Error: apitypes.Error{
		Code: "not_signed_in", Message: "not signed in: run `maket login` or set MAKET_API_KEY"}}), msg)
	if api.count() != 0 {
		t.Errorf("%d requests sent without a key", api.count())
	}
}

func TestUnauthorized(t *testing.T) {
	serverErr := apitypes.ErrorResponse{Error: apitypes.Error{Code: "unauthorized", Message: "missing or invalid credentials"}}
	api := newFakeAPI(t, reply(http.StatusUnauthorized, serverErr))
	h := newHarness(t, api.srv.URL)
	msg := "maket: the API Key was rejected (revoked or wrong): run `maket login`\n"
	h.run("ls").want(t, exitAuth, "", msg)
	h.run("ls", "--json").want(t, exitAuth, jsonOf(t, serverErr), msg) // server error passed through
}

func TestNotAllowed(t *testing.T) {
	api := newFakeAPI(t, apiError(http.StatusForbidden, "not_allowed", "your GitHub account is no longer allowed to use maket", nil))
	h := newHarness(t, api.srv.URL)
	h.run("ls").want(t, exitAuth, "", "maket: your GitHub account is no longer allowed to use maket\n")

	other := newFakeAPI(t, apiError(http.StatusForbidden, "session_required", "sign in on the dashboard", nil))
	newHarness(t, other.srv.URL).run("ls").want(t, exitFailure, "", "maket: sign in on the dashboard\n")
}

func writeSite(t *testing.T) string {
	dir := filepath.Join(t.TempDir(), "dist")
	write(t, dir, "index.html", "<h1>hi</h1>")
	write(t, dir, "assets/app.js", "1")
	write(t, dir, ".env", "secret")
	return dir
}

func TestPushFolder(t *testing.T) {
	p := sampleProto("ab12cd34")
	api := newFakeAPI(t, reply(http.StatusCreated, p))
	h := newHarness(t, api.srv.URL)
	dir := writeSite(t)
	h.run("push", dir).want(t, exitOK, p.URL+"\n", "")
	got := api.only()
	checkRequest(t, got, "POST", "/api/prototypes")
	if got.Filename != "dist.zip" || len(got.Fields) != 0 {
		t.Errorf("filename %q fields %v", got.Filename, got.Fields)
	}
	entries := zipEntries(t, got.File)
	if len(entries) != 2 || entries["index.html"] != "<h1>hi</h1>" || entries["assets/app.js"] != "1" {
		t.Errorf("zip entries %v", entries)
	}
}

func TestPushFileWithFlagsAfterPath(t *testing.T) {
	p := sampleProto("ab12cd34")
	api := newFakeAPI(t, reply(http.StatusCreated, p))
	h := newHarness(t, api.srv.URL)
	h.stderrTTY = true
	file := filepath.Join(t.TempDir(), "Page.HTML")
	if err := os.WriteFile(file, []byte("<p>page</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.run("push", file, "--spa", "--name", "My page", "--json").want(t, exitOK, jsonOf(t, p), "Uploading Page.HTML (11 B)...\n")
	got := api.only()
	checkRequest(t, got, "POST", "/api/prototypes")
	if got.Filename != "Page.HTML" || string(got.File) != "<p>page</p>" {
		t.Errorf("file %q %q", got.Filename, got.File)
	}
	if got.Fields["name"] != "My page" || got.Fields["spa"] != "true" || len(got.Fields) != 2 {
		t.Errorf("fields %v", got.Fields)
	}
}

func TestPushSlug(t *testing.T) {
	p := sampleProto("ab12cd34")
	api := newFakeAPI(t, reply(http.StatusOK, p))
	h := newHarness(t, api.srv.URL)
	dir := writeSite(t)
	h.run("push", "--slug", "ab12cd34", dir, "--spa=false").want(t, exitOK, p.URL+"\n", "")
	got := api.only()
	checkRequest(t, got, "PUT", "/api/prototypes/ab12cd34")
	if got.Fields["spa"] != "false" || len(got.Fields) != 1 {
		t.Errorf("fields %v: only spa should be sent", got.Fields)
	}
}

func TestPushNotFound(t *testing.T) {
	api := newFakeAPI(t, apiError(http.StatusNotFound, "not_found", "no such prototype", nil))
	h := newHarness(t, api.srv.URL)
	h.run("push", writeSite(t), "--slug", "nope1234").want(t, exitNotFound, "", "maket: no such prototype\n")
}

func TestPushUsageErrors(t *testing.T) {
	api := newFakeAPI(t, reply(http.StatusCreated, sampleProto("x")))
	h := newHarness(t, api.srv.URL)
	tmp := t.TempDir()
	write(t, tmp, "notes.md", "x")
	if err := os.Mkdir(filepath.Join(tmp, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"push"},
		{"push", "a", "b"},
		{"push", filepath.Join(tmp, "notes.md")},
		{"push", filepath.Join(tmp, "missing")},
		{"push", filepath.Join(tmp, "empty")},
		{"push", tmp, "--bogus"},
		{"push", tmp, "--slug="},
	} {
		res := h.run(args...)
		if res.code != exitUsage || res.stdout != "" || !strings.HasPrefix(res.stderr, "maket: ") || strings.Count(res.stderr, "\n") != 1 {
			t.Errorf("%q: %+v", args, res)
		}
	}
	// --json usage errors are JSON too.
	res := h.run("push", filepath.Join(tmp, "notes.md"), "--json")
	var er apitypes.ErrorResponse
	if err := json.Unmarshal([]byte(res.stdout), &er); err != nil || er.Error.Code != "usage" || res.code != exitUsage {
		t.Errorf("json usage error: %+v (%v)", res, err)
	}
	if res := h.run("push", "--json", "--bogus"); !strings.Contains(res.stdout, `"code": "usage"`) {
		t.Errorf("bad flag under --json: %+v", res)
	}
	if api.count() != 0 {
		t.Errorf("%d requests sent", api.count())
	}
}

func TestPushRejected(t *testing.T) {
	serverErr := apitypes.ErrorResponse{Error: apitypes.Error{
		Code: "disallowed_files", Message: "These files have types maket does not serve.",
		Details: &apitypes.ErrorDetails{Paths: []string{"a.exe", "b/c.sh"}, More: 3},
	}}
	api := newFakeAPI(t, reply(http.StatusBadRequest, serverErr))
	h := newHarness(t, api.srv.URL)
	wantErr := "maket: These files have types maket does not serve.\n  a.exe\n  b/c.sh\n  ...and 3 more\n"
	h.run("push", writeSite(t)).want(t, exitFailure, "", wantErr)
	h.run("push", writeSite(t), "--json").want(t, exitFailure, jsonOf(t, serverErr), wantErr)

	serverErr.Error.Details.More = 0
	h.run("push", writeSite(t)).want(t, exitFailure, "", "maket: These files have types maket does not serve.\n  a.exe\n  b/c.sh\n")
}

func TestPushQuota(t *testing.T) {
	api := newFakeAPI(t, apiError(http.StatusRequestEntityTooLarge, "quota_exceeded", "over quota", nil))
	h := newHarness(t, api.srv.URL)
	h.run("push", writeSite(t)).want(t, exitFailure, "", "maket: over quota\n")
}

func TestLs(t *testing.T) {
	list := apitypes.PrototypeList{
		Prototypes: []apitypes.Prototype{sampleProto("ab12cd34"), sampleProto("zz99yy88")},
		Usage:      apitypes.Usage{UsedBytes: 1258291, QuotaBytes: 1 << 30},
	}
	list.Prototypes[1].Name = "Tab\there"
	list.Prototypes[1].SizeBytes = 820
	api := newFakeAPI(t, reply(http.StatusOK, list))
	h := newHarness(t, api.srv.URL)
	when := list.Prototypes[0].UpdatedAt.Local().Format("2006-01-02 15:04")
	wantOut := "" +
		"SLUG      NAME      VERSION  SIZE   UPDATED           URL\n" +
		"ab12cd34  Demo      2        15 KB  " + when + "  https://ab12cd34.cloudshare.my.id\n" +
		"zz99yy88  Tab here  2        820 B  " + when + "  https://zz99yy88.cloudshare.my.id\n"
	h.run("ls").want(t, exitOK, wantOut, "1.2 MB of 1 GB used\n")
	checkRequest(t, api.reqs[0], "GET", "/api/prototypes")
	h.run("ls", "--json").want(t, exitOK, jsonOf(t, list), "")
}

func TestLsEmpty(t *testing.T) {
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"prototypes":null,"usage":{"used_bytes":0,"quota_bytes":1073741824}}`))
	})
	h := newHarness(t, api.srv.URL)
	h.run("ls").want(t, exitOK, "", "No prototypes yet\n")
	res := h.run("ls", "--json")
	if res.code != exitOK || !strings.Contains(res.stdout, `"prototypes": []`) {
		t.Errorf("ls --json empty: %+v", res)
	}
}

func TestRm(t *testing.T) {
	api := newFakeAPI(t, reply(http.StatusNoContent, nil))
	h := newHarness(t, api.srv.URL)
	h.run("rm", "ab12cd34", "--yes").want(t, exitOK, "", "Deleted ab12cd34\n")
	checkRequest(t, api.only(), "DELETE", "/api/prototypes/ab12cd34")
	h.run("rm", "--json", "-yes", "ab12cd34").want(t, exitOK, "{\n  \"slug\": \"ab12cd34\",\n  \"deleted\": true\n}\n", "Deleted ab12cd34\n")
}

func TestRmConfirmation(t *testing.T) {
	api := newFakeAPI(t, reply(http.StatusNoContent, nil))
	h := newHarness(t, api.srv.URL)

	// Off a terminal --yes is required.
	res := h.run("rm", "ab12cd34")
	res.want(t, exitUsage, "", "maket: refusing to delete ab12cd34 without confirmation: pass --yes\n")
	h.stdinTTY = true // stderr still not a terminal
	if res := h.run("rm", "ab12cd34"); res.code != exitUsage {
		t.Errorf("stdin-only TTY: %+v", res)
	}

	h.stderrTTY = true
	h.stdin = "n\n"
	h.run("rm", "ab12cd34").want(t, exitFailure, "", "Delete ab12cd34? [y/N] Aborted\n")
	h.stdin = ""
	h.run("rm", "ab12cd34", "--json").want(t, exitFailure,
		jsonOf(t, apitypes.ErrorResponse{Error: apitypes.Error{Code: "aborted", Message: "Aborted"}}),
		"Delete ab12cd34? [y/N] Aborted\n")
	if api.count() != 0 {
		t.Fatalf("%d requests before confirming", api.count())
	}
	h.stdin = " YES \n"
	h.run("rm", "ab12cd34").want(t, exitOK, "", "Delete ab12cd34? [y/N] Deleted ab12cd34\n")
	checkRequest(t, api.only(), "DELETE", "/api/prototypes/ab12cd34")
}

func TestRmNotFound(t *testing.T) {
	serverErr := apitypes.ErrorResponse{Error: apitypes.Error{Code: "not_found", Message: "no such prototype"}}
	api := newFakeAPI(t, reply(http.StatusNotFound, serverErr))
	h := newHarness(t, api.srv.URL)
	h.run("rm", "nope1234", "--yes").want(t, exitNotFound, "", "maket: no such prototype\n")
	h.run("rm", "nope1234", "--yes", "--json").want(t, exitNotFound, jsonOf(t, serverErr), "maket: no such prototype\n")
}

func TestInvalidResponse(t *testing.T) {
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("<html>bad gateway</html>"))
	})
	h := newHarness(t, api.srv.URL)
	msg := "unexpected response from " + api.srv.URL + " (HTTP 502): is this a maket server?"
	h.run("whoami", "--json").want(t, exitFailure,
		jsonOf(t, apitypes.ErrorResponse{Error: apitypes.Error{Code: "invalid_response", Message: msg}}), "maket: "+msg+"\n")

	// A 200 that is not the expected JSON is invalid too.
	api.mu.Lock()
	api.handle = func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) }
	api.mu.Unlock()
	if res := h.run("ls"); res.code != exitFailure || !strings.Contains(res.stderr, "(HTTP 200)") {
		t.Errorf("non-JSON 200: %+v", res)
	}
}

func TestNetworkError(t *testing.T) {
	api := newFakeAPI(t, reply(http.StatusOK, me))
	url := api.srv.URL
	api.srv.Close()
	h := newHarness(t, url)
	res := h.run("whoami", "--json")
	var er apitypes.ErrorResponse
	if err := json.Unmarshal([]byte(res.stdout), &er); err != nil || er.Error.Code != "network" {
		t.Errorf("stdout %q (%v)", res.stdout, err)
	}
	if res.code != exitFailure || !strings.HasPrefix(res.stderr, "maket: cannot reach "+url+": ") {
		t.Errorf("%+v", res)
	}
}
