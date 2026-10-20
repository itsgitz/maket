package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/apitypes"
	"github.com/itsgitz/maket/apps/api/internal/auth"
	"github.com/itsgitz/maket/apps/api/internal/cli"
	"github.com/itsgitz/maket/apps/api/internal/config"
	"github.com/itsgitz/maket/apps/api/internal/db"
	"github.com/itsgitz/maket/apps/api/internal/httpapi"
	"github.com/itsgitz/maket/apps/api/internal/preview"
	"github.com/itsgitz/maket/apps/api/internal/prototype"
	"github.com/itsgitz/maket/apps/api/internal/prototype/archive"
	"github.com/itsgitz/maket/apps/api/internal/storage"
)

// realServer runs the real API stack (database, storage, Prototype service,
// router) and returns its URL, an API Key, and the preview domain.
func realServer(t *testing.T) (apiURL, key, previewDomain string) {
	t.Helper()
	ctx := t.Context()
	srv := httptest.NewUnstartedServer(nil)
	cfg := config.Config{
		AppHost:       srv.Listener.Addr().String(),
		PreviewDomain: "preview.localhost:8080",
		PublicScheme:  "http",
	}
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "maket.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	allow, err := auth.NewAllowlist([]string{"octo"})
	if err != nil {
		t.Fatal(err)
	}
	authSvc := auth.NewService(database, allow, time.Now)
	store, err := storage.NewLocal(filepath.Join(t.TempDir(), "storage"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	tmp := t.TempDir()
	protos := prototype.NewService(database, store, allow, prototype.Config{
		Limits:      archive.Limits{MaxFiles: 20, MaxExtractedBytes: 1 << 20},
		QuotaBytes:  4 << 20,
		TmpDir:      tmp,
		PreviewHost: func(slug string) string { return slug + "." + cfg.PreviewDomain },
	}, nil, time.Now)
	srv.Config.Handler = httpapi.New(cfg, httpapi.Deps{
		Dashboard:      http.NotFoundHandler(),
		Previews:       &preview.Handler{Lookup: protos, Store: store},
		Auth:           authSvc,
		Prototypes:     protos,
		MaxUploadBytes: 1 << 20,
		TmpDir:         tmp,
	})
	srv.Start()
	t.Cleanup(srv.Close)

	user, err := authSvc.LoginGitHub(ctx, auth.Identity{GitHubID: 1, Login: "octo", Name: "Octo"})
	if err != nil {
		t.Fatal(err)
	}
	_, key, err = authSvc.CreateAPIKey(ctx, user.ID, "cli test")
	if err != nil {
		t.Fatal(err)
	}
	return srv.URL, key, cfg.PreviewDomain
}

type run struct {
	code           int
	stdout, stderr string
}

func runCLI(t *testing.T, env map[string]string, configDir, stdin string, args ...string) run {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Run(args, cli.Env{
		Stdin:        strings.NewReader(stdin),
		Stdout:       &stdout,
		Stderr:       &stderr,
		Getenv:       func(k string) string { return env[k] },
		ConfigDir:    func() (string, error) { return configDir, nil },
		StdinIsTTY:   func() bool { return false },
		StderrIsTTY:  func() bool { return false },
		ReadPassword: func() (string, error) { panic("not a terminal") },
		HTTPClient:   cli.NewHTTPClient(),
		Version:      "test",
	})
	return run{code, stdout.String(), stderr.String()}
}

// getPreview fetches path from the Preview at previewURL, sending the
// request to the API server with the Preview's Host header.
func getPreview(t *testing.T, apiURL, previewURL, path string) (int, string) {
	t.Helper()
	pu, err := url.Parse(previewURL)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("GET", apiURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = pu.Host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestEndToEnd(t *testing.T) {
	apiURL, key, previewDomain := realServer(t)
	configDir := filepath.Join(t.TempDir(), "maket")
	env := map[string]string{"MAKET_API_URL": apiURL}

	// Sign in with the key on stdin; later commands use the config file.
	if r := runCLI(t, env, configDir, key+"\n", "login"); r.code != 0 || r.stderr != "Signed in as octo ("+apiURL+")\n" {
		t.Fatalf("login: %+v", r)
	}
	if r := runCLI(t, env, configDir, "", "whoami"); r.code != 0 || r.stdout != "octo\n" {
		t.Fatalf("whoami: %+v", r)
	}

	site := filepath.Join(t.TempDir(), "my-site")
	for rel, body := range map[string]string{
		"index.html":       "<h1>v1</h1>",
		"about/index.html": "<p>about</p>",
		".env":             "SECRET=1",
		".git/HEAD":        "ref",
	} {
		p := filepath.Join(site, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	r := runCLI(t, env, configDir, "", "push", site, "--json")
	if r.code != 0 {
		t.Fatalf("push: %+v", r)
	}
	var p apitypes.Prototype
	if err := json.Unmarshal([]byte(r.stdout), &p); err != nil {
		t.Fatalf("push --json output %q: %v", r.stdout, err)
	}
	if p.Name != "my-site" || p.Version != 1 || p.FileCount != 2 || p.SPA || !strings.HasSuffix(p.URL, "."+previewDomain) {
		t.Errorf("pushed %+v", p)
	}
	if code, body := getPreview(t, apiURL, p.URL, "/"); code != 200 || body != "<h1>v1</h1>" {
		t.Errorf("preview /: %d %q", code, body)
	}
	if code, _ := getPreview(t, apiURL, p.URL, "/.env"); code != 404 {
		t.Errorf("preview /.env: %d, the dotfile was uploaded", code)
	}

	// A new Version keeps the name; --spa turns SPA mode on.
	single := filepath.Join(t.TempDir(), "page.html")
	os.WriteFile(single, []byte("<h1>v2</h1>"), 0o644)
	r = runCLI(t, env, configDir, "", "push", single, "--slug", p.Slug, "--spa")
	if r.code != 0 || r.stdout != p.URL+"\n" {
		t.Fatalf("push --slug: %+v", r)
	}
	if code, body := getPreview(t, apiURL, p.URL, "/some/route"); code != 200 || body != "<h1>v2</h1>" {
		t.Errorf("SPA preview: %d %q", code, body)
	}

	r = runCLI(t, env, configDir, "", "ls", "--json")
	var list apitypes.PrototypeList
	if err := json.Unmarshal([]byte(r.stdout), &list); err != nil || r.code != 0 {
		t.Fatalf("ls --json: %+v (%v)", r, err)
	}
	if len(list.Prototypes) != 1 || list.Prototypes[0].Name != "my-site" || list.Prototypes[0].Version != 2 || !list.Prototypes[0].SPA {
		t.Errorf("ls: %+v", list.Prototypes)
	}
	if r := runCLI(t, env, configDir, "", "ls"); r.code != 0 || !strings.Contains(r.stdout, p.Slug+"  my-site  2 ") {
		t.Errorf("ls table: %+v", r)
	}

	// A rejected Upload lists the offending files.
	bad := filepath.Join(t.TempDir(), "bad")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "index.html"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(bad, "run.sh"), []byte("x"), 0o644)
	if r := runCLI(t, env, configDir, "", "push", bad); r.code != 1 || !strings.HasSuffix(r.stderr, "\n  run.sh\n") {
		t.Errorf("rejected push: %+v", r)
	}

	if r := runCLI(t, env, configDir, "", "rm", p.Slug); r.code != 2 {
		t.Errorf("rm without --yes: %+v", r)
	}
	if r := runCLI(t, env, configDir, "", "rm", p.Slug, "--yes"); r.code != 0 || r.stderr != "Deleted "+p.Slug+"\n" {
		t.Errorf("rm: %+v", r)
	}
	if code, _ := getPreview(t, apiURL, p.URL, "/"); code != 404 {
		t.Errorf("preview after rm: %d", code)
	}
	if r := runCLI(t, env, configDir, "", "rm", p.Slug, "--yes"); r.code != 4 {
		t.Errorf("rm again: %+v", r)
	}
	if r := runCLI(t, env, configDir, "", "ls"); r.code != 0 || r.stdout != "" || r.stderr != "No prototypes yet\n" {
		t.Errorf("ls empty: %+v", r)
	}

	// A wrong key is exit 3; logging out leaves no key.
	if r := runCLI(t, map[string]string{"MAKET_API_URL": apiURL, "MAKET_API_KEY": "mk_" + strings.Repeat("A", 43)}, configDir, "", "whoami"); r.code != 3 {
		t.Errorf("wrong key: %+v", r)
	}
	if r := runCLI(t, env, configDir, "", "logout"); r.code != 0 {
		t.Errorf("logout: %+v", r)
	}
	if r := runCLI(t, env, configDir, "", "whoami"); r.code != 3 {
		t.Errorf("whoami after logout: %+v", r)
	}
}
