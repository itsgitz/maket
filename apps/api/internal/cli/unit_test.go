package cli

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestConfigDir(t *testing.T) {
	home := func() (string, error) { return "/home/ann", nil }
	noHome := func() (string, error) { return "", errors.New("no home") }
	tests := []struct {
		name string
		env  map[string]string
		goos string
		home func() (string, error)
		want string // "" means an error
	}{
		{"xdg wins", map[string]string{"XDG_CONFIG_HOME": "/xdg", "AppData": `C:\AppData`}, "linux", home, filepath.Join("/xdg", "maket")},
		{"xdg on windows", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, "windows", home, filepath.Join("/xdg", "maket")},
		{"linux default", nil, "linux", home, filepath.Join("/home/ann", ".config", "maket")},
		{"darwin uses ~/.config too", nil, "darwin", home, filepath.Join("/home/ann", ".config", "maket")},
		{"windows appdata", map[string]string{"AppData": `C:\Users\ann\AppData\Roaming`}, "windows", home, filepath.Join(`C:\Users\ann\AppData\Roaming`, "maket")},
		{"windows without appdata", nil, "windows", home, ""},
		{"no home", nil, "linux", noHome, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := configDir(func(k string) string { return tt.env[k] }, tt.goos, tt.home)
			if tt.want == "" {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestResolveSettings(t *testing.T) {
	tests := []struct {
		name    string
		file    string // config.json contents; "" for no file
		env     map[string]string
		urlFlag string
		want    settings
		wantErr bool
	}{
		{name: "defaults", want: settings{URL: defaultAPIURL}},
		{name: "file", file: `{"api_url":"https://maket.example/","api_key":"mk_file"}`,
			want: settings{URL: "https://maket.example", Key: "mk_file"}},
		{name: "env key overrides only the key", file: `{"api_url":"https://maket.example","api_key":"mk_file"}`,
			env:  map[string]string{"MAKET_API_KEY": "mk_env"},
			want: settings{URL: "https://maket.example", Key: "mk_env"}},
		{name: "env url overrides only the url", file: `{"api_url":"https://maket.example","api_key":"mk_file"}`,
			env:  map[string]string{"MAKET_API_URL": "http://localhost:8080//"},
			want: settings{URL: "http://localhost:8080", Key: "mk_file"}},
		{name: "env both, broken file never read", file: `{broken`,
			env:  map[string]string{"MAKET_API_URL": "https://a.example", "MAKET_API_KEY": "mk_env"},
			want: settings{URL: "https://a.example", Key: "mk_env"}},
		{name: "flag beats env", env: map[string]string{"MAKET_API_URL": "https://a.example", "MAKET_API_KEY": "k"},
			urlFlag: "https://flag.example/", want: settings{URL: "https://flag.example", Key: "k"}},
		{name: "broken file", file: `{broken`, wantErr: true},
		{name: "bad scheme", env: map[string]string{"MAKET_API_URL": "ftp://x"}, wantErr: true},
		{name: "no host", env: map[string]string{"MAKET_API_URL": "https://"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.file != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tt.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			r := &runner{env: Env{
				Getenv:    func(k string) string { return tt.env[k] },
				ConfigDir: func() (string, error) { return dir, nil },
			}}
			got, err := r.resolveSettings(tt.urlFlag)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("got %+v, want an error", got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestWriteConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "maket")
	path := filepath.Join(dir, "config.json")
	if err := writeConfig(path, configFile{APIURL: "https://a.example", APIKey: "mk_1"}); err != nil {
		t.Fatal(err)
	}
	if err := writeConfig(path, configFile{APIURL: "https://b.example"}); err != nil { // replaces
		t.Fatal(err)
	}
	got, err := readConfig(path)
	if err != nil || got != (configFile{APIURL: "https://b.example"}) {
		t.Fatalf("read back %+v, %v", got, err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "api_key") {
		t.Errorf("empty key still written: %s", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != want {
				t.Errorf("%s: mode %o, want %o", p, got, want)
			}
		}
	}
}

func TestSendsKeyInClear(t *testing.T) {
	tests := map[string]bool{
		"https://app.kotakserver.my.id": false,
		"https://example.com":           false,
		"http://example.com":            true,
		"http://192.168.1.5:8080":       true,
		"http://127.0.0.2":              true,
		"http://localhost:8080":         false,
		"http://LOCALHOST":              false,
		"http://app.localhost:5173":     false,
		"http://127.0.0.1:8080":         false,
		"http://[::1]:8080":             false,
		"http://notlocalhost":           true,
	}
	for u, want := range tests {
		if got := sendsKeyInClear(u); got != want {
			t.Errorf("sendsKeyInClear(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		args    []string
		pos     []string
		name    string
		spa     bool
		wantErr error // errHelp, or nil; "usage" checked separately
		usage   bool
	}{
		{args: []string{"./dist", "--spa"}, pos: []string{"./dist"}, spa: true},
		{args: []string{"--name", "Demo", "./dist", "-spa=false"}, pos: []string{"./dist"}, name: "Demo"},
		{args: []string{"a", "--name=x", "b"}, pos: []string{"a", "b"}, name: "x"},
		{args: []string{"--spa", "--", "--name", "-x"}, pos: []string{"--name", "-x"}, spa: true},
		{args: []string{"dist", "--help"}, wantErr: errHelp},
		{args: []string{"-h"}, wantErr: errHelp},
		{args: []string{"dist", "--nope"}, usage: true},
		{args: []string{"dist", "--name"}, usage: true},
		{args: nil, pos: nil},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			fs := newFlagSet("push")
			name := fs.String("name", "", "")
			spa := fs.Bool("spa", false, "")
			pos, err := parseArgs(fs, "push", tt.args)
			if tt.usage {
				var e *exitError
				if !errors.As(err, &e) || e.exit != exitUsage {
					t.Fatalf("err = %v, want a usage error", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if !slices.Equal(pos, tt.pos) || *name != tt.name || *spa != tt.spa {
				t.Errorf("got pos %q name %q spa %v", pos, *name, *spa)
			}
		})
	}
}

func TestFlagGiven(t *testing.T) {
	fs := newFlagSet("push")
	fs.Bool("spa", false, "")
	fs.String("name", "", "")
	if _, err := parseArgs(fs, "push", []string{"x", "--spa=false"}); err != nil {
		t.Fatal(err)
	}
	if !flagGiven(fs, "spa") || flagGiven(fs, "name") {
		t.Errorf("flagGiven: spa %v name %v", flagGiven(fs, "spa"), flagGiven(fs, "name"))
	}
}

func TestArgsWantJSON(t *testing.T) {
	tests := map[string]bool{
		"--json":         true,
		"x -json":        true,
		"--json=true":    true,
		"--json=false":   false,
		"json":           false,
		"-- --json":      false,
		"--name x --spa": false,
	}
	for args, want := range tests {
		if got := argsWantJSON(strings.Fields(args)); got != want {
			t.Errorf("argsWantJSON(%q) = %v, want %v", args, got, want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	tests := map[int64]string{
		0:                      "0 B",
		820:                    "820 B",
		1023:                   "1023 B",
		1024:                   "1 KB",
		15 * 1024:              "15 KB",
		15*1024 + 300:          "15.3 KB",
		1258291:                "1.2 MB",
		1024*1024 - 1:          "1 MB",
		1 << 30:                "1 GB",
		100 << 20:              "100 MB",
		5 << 40:                "5 TB",
		int64(2.5 * (1 << 30)): "2.5 GB",
	}
	for n, want := range tests {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestPushableFile(t *testing.T) {
	for name, want := range map[string]bool{
		"index.html": true, "A.HTM": true, "site.Zip": true,
		"app.js": false, "README": false, "page.html.txt": false, "archive.tar.gz": false,
	} {
		if got := pushableFile(name); got != want {
			t.Errorf("pushableFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestCheckPushPath(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "page.HTML", "<p>")
	write(t, dir, "notes.md", "x")
	tests := map[string]int{ // path -> exit code, 0 for ok
		dir:                               0,
		filepath.Join(dir, "page.HTML"):   0,
		filepath.Join(dir, "notes.md"):    exitUsage,
		filepath.Join(dir, "missing.zip"): exitUsage,
	}
	for p, want := range tests {
		_, err := checkPushPath(p)
		got := 0
		var e *exitError
		if errors.As(err, &e) {
			got = e.exit
		} else if err != nil {
			got = -1
		}
		if got != want {
			t.Errorf("checkPushPath(%s): %v (exit %d), want exit %d", p, err, got, want)
		}
	}
}

// write creates dir/rel with contents, making parent folders.
func write(t *testing.T, dir, rel, contents string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// zipEntries returns name -> contents of a zip archive.
func zipEntries(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(data)
	}
	return out
}

func TestZipFolder(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "index.html", "home")
	write(t, dir, "assets/app.js", "js")
	write(t, dir, "assets/img/deep/logo.svg", "svg")
	write(t, dir, ".env", "secret")
	write(t, dir, "assets/.DS_Store", "junk")
	write(t, dir, ".git/config", "git")
	write(t, dir, "assets/.cache/x.js", "cache")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(dir, "index.html"), filepath.Join(dir, "link.html")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "assets"), filepath.Join(dir, "linkdir")); err != nil {
			t.Fatal(err)
		}
	}
	b, err := zipFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := zipEntries(t, b)
	want := map[string]string{"index.html": "home", "assets/app.js": "js", "assets/img/deep/logo.svg": "svg"}
	if len(got) != len(want) {
		t.Fatalf("entries %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

func TestZipFolderSymlinkedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	dir := t.TempDir()
	write(t, dir, "site/index.html", "home")
	link := filepath.Join(dir, "current")
	if err := os.Symlink(filepath.Join(dir, "site"), link); err != nil {
		t.Fatal(err)
	}
	b, err := zipFolder(link)
	if err != nil {
		t.Fatal(err)
	}
	if got := zipEntries(t, b); got["index.html"] != "home" || len(got) != 1 {
		t.Errorf("entries %v", got)
	}
}

func TestZipFolderEmpty(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".hidden/index.html", "x")
	write(t, dir, ".env", "x")
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := zipFolder(dir)
	var e *exitError
	if !errors.As(err, &e) || e.exit != exitUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
}

func TestZipName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-site")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	tests := map[string]string{
		".":                              "my-site.zip",
		"./":                             "my-site.zip",
		dir:                              "my-site.zip",
		dir + string(filepath.Separator): "my-site.zip",
		"dist":                           "dist.zip",
		filepath.Join("a", "build"):      "build.zip",
		"..":                             filepath.Base(filepath.Dir(dir)) + ".zip",
	}
	for in, want := range tests {
		if got := zipName(in); got != want {
			t.Errorf("zipName(%q) = %q, want %q", in, got, want)
		}
	}
}
