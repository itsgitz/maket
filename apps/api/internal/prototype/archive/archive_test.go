package archive_test

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/itsgitz/maket/apps/api/internal/prototype/archive"
)

var limits = archive.Limits{MaxFiles: 100, MaxExtractedBytes: 1 << 20}

// entry is one zip entry; a zero mode means a regular file.
type entry struct {
	name string
	body string
	mode fs.FileMode
}

func makeZip(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		if strings.HasSuffix(e.name, "/") {
			h.Method = zip.Store
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func files(names ...string) []entry {
	es := make([]entry, len(names))
	for i, n := range names {
		es[i] = entry{name: n, body: "body of " + n}
	}
	return es
}

// extract runs Extract with a fresh tmpRoot. On error it checks tmpRoot was
// left empty; on success it registers Cleanup.
func extract(t *testing.T, name string, data []byte, l archive.Limits) (*archive.Result, error) {
	t.Helper()
	tmpRoot := t.TempDir()
	res, err := archive.Extract(name, bytes.NewReader(data), int64(len(data)), l, tmpRoot)
	if err != nil {
		if des, _ := os.ReadDir(tmpRoot); len(des) > 0 {
			t.Errorf("tmpRoot not empty after error: %v", des)
		}
		return nil, err
	}
	t.Cleanup(func() {
		if err := res.Cleanup(); err != nil {
			t.Error(err)
		}
	})
	return res, nil
}

func wantError(t *testing.T, err error, code string) *archive.Error {
	t.Helper()
	var ae *archive.Error
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *archive.Error %s", err, code)
	}
	if ae.Code != code {
		t.Fatalf("code = %s (%v), want %s", ae.Code, ae, code)
	}
	if ae.Message == "" {
		t.Error("empty message")
	}
	return ae
}

func paths(res *archive.Result) []string {
	var ps []string
	for _, f := range res.Files {
		ps = append(ps, f.Path)
	}
	return ps
}

func readFile(t *testing.T, res *archive.Result, p string) string {
	t.Helper()
	i := slices.IndexFunc(res.Files, func(f archive.File) bool { return f.Path == p })
	if i < 0 {
		t.Fatalf("no file %s in %v", p, paths(res))
	}
	f, err := res.Open(res.Files[i])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(b)) != res.Files[i].Size {
		t.Errorf("%s: size %d, read %d bytes", p, res.Files[i].Size, len(b))
	}
	return string(b)
}

func TestSinglePage(t *testing.T) {
	for _, name := range []string{"page.html", "Page.HTM", "x.Html"} {
		t.Run(name, func(t *testing.T) {
			res, err := extract(t, name, []byte("<h1>hi</h1>"), limits)
			if err != nil {
				t.Fatal(err)
			}
			if got := paths(res); !slices.Equal(got, []string{"index.html"}) {
				t.Errorf("paths = %v", got)
			}
			if res.TotalSize != 11 {
				t.Errorf("TotalSize = %d", res.TotalSize)
			}
			if got := readFile(t, res, "index.html"); got != "<h1>hi</h1>" {
				t.Errorf("index.html = %q", got)
			}
			if b, err := os.ReadFile(filepath.Join(res.Dir, "index.html")); err != nil || string(b) != "<h1>hi</h1>" {
				t.Errorf("on disk = %q, %v", b, err)
			}
		})
	}
}

func TestSinglePageTooLarge(t *testing.T) {
	_, err := extract(t, "a.html", make([]byte, 101), archive.Limits{MaxFiles: 1, MaxExtractedBytes: 100})
	wantError(t, err, archive.CodeTooLarge)
}

func TestInvalidFile(t *testing.T) {
	for name, data := range map[string][]byte{
		"notes.txt":     []byte("hi"),
		"site.tar.gz":   []byte("hi"),
		"noext":         []byte("hi"),
		"fake.zip":      []byte("<html>not a zip</html>"),
		"empty.zip":     nil,
		"index.html.gz": []byte("hi"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := extract(t, name, data, limits)
			wantError(t, err, archive.CodeInvalidFile)
		})
	}
}

func TestZipLayouts(t *testing.T) {
	tests := []struct {
		name     string
		entries  []entry
		want     []string
		stripped bool // whether "dist/" was stripped
	}{
		{"flat", files("index.html", "app.js"), []string{"app.js", "index.html"}, false},
		{"nested", files("index.html", "assets/css/site.css", "assets/img/Logo.PNG", ".well-known/x.json"),
			[]string{".well-known/x.json", "assets/css/site.css", "assets/img/Logo.PNG", "index.html"}, false},
		{"top folder stripped", files("dist/index.html", "dist/assets/a.js"), []string{"assets/a.js", "index.html"}, true},
		{"with dir entries", append([]entry{{name: "dist/"}, {name: "dist/assets/"}}, files("dist/index.html", "dist/assets/a.js")...),
			[]string{"assets/a.js", "index.html"}, true},
		{"one level only", files("dist/site/index.html", "dist/index.html"), []string{"index.html", "site/index.html"}, true},
		{"two top dirs kept", files("index.html", "a/x.js", "b/y.js"), []string{"a/x.js", "b/y.js", "index.html"}, false},
		{"junk dropped", append(files("index.html", "__MACOSX/._index.html", "sub/__MACOSX/x.js", ".DS_Store", "sub/thumbs.DB", "sub/a.css"),
			entry{name: "__MACOSX/"}), []string{"index.html", "sub/a.css"}, false},
		{"junk does not block stripping", files("dist/index.html", "dist/.DS_Store", "__MACOSX/dist/._index.html", "Thumbs.db"),
			[]string{"index.html"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := extract(t, "up.ZIP", makeZip(t, tt.entries...), limits)
			if err != nil {
				t.Fatal(err)
			}
			if got := paths(res); !slices.Equal(got, tt.want) {
				t.Errorf("paths = %v, want %v", got, tt.want)
			}
			var total int64
			for _, f := range res.Files {
				total += f.Size
				orig := f.Path
				if tt.stripped {
					orig = "dist/" + orig
				}
				if got := readFile(t, res, f.Path); got != "body of "+orig {
					t.Errorf("%s = %q", f.Path, got)
				}
			}
			if total != res.TotalSize {
				t.Errorf("TotalSize = %d, sum = %d", res.TotalSize, total)
			}
		})
	}
}

func TestUnsafePaths(t *testing.T) {
	entries := append(files(
		"index.html",
		"../x.html",
		"a/../../x.js",
		"/etc/x.css",
		"C:/x.js",
		`a\b.js`,
		"nul\x00.js",
		"bad\xff.js",
		"a//b.js",
		"./a.js",
		"a/./b.js",
		"dup.js",
		"dup.js",
		"conflict.js",
		"conflict.js/inner.js",
		strings.Repeat("s", 256)+".js",
		strings.Repeat(strings.Repeat("d", 99)+"/", 10)+"x.js",
		"Case.js", // case-only difference from case.js is allowed
		"case.js",
		"bad.php", // disallowed, but unsafe is reported first
	), entry{name: "link.js", body: "/etc/passwd", mode: fs.ModeSymlink | 0o777},
		entry{name: "fifo.js", mode: fs.ModeNamedPipe | 0o644})

	_, err := extract(t, "x.zip", makeZip(t, entries...), limits)
	ae := wantError(t, err, archive.CodeUnsafePaths)
	want := []string{
		"../x.html",
		"a/../../x.js",
		"/etc/x.css",
		"C:/x.js",
		`a\b.js`,
		`"nul\x00.js"`,
		`"bad\xff.js"`,
		"a//b.js",
		"./a.js",
		"a/./b.js",
		"dup.js",
		"conflict.js",
		strings.Repeat("s", 256) + ".js",
		strings.Repeat(strings.Repeat("d", 99)+"/", 10) + "x.js",
		"link.js",
		"fifo.js",
	}
	slices.Sort(want)
	if !slices.Equal(ae.Paths, want) {
		t.Errorf("paths =\n%q\nwant\n%q", ae.Paths, want)
	}
	if ae.More != 0 {
		t.Errorf("More = %d", ae.More)
	}
}

func TestReservedPrefixAfterStrip(t *testing.T) {
	for name, names := range map[string][]string{
		"root":     {"index.html", ".tmp/a.js"},
		"stripped": {"dist/index.html", "dist/.tmpx/a.js"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := extract(t, "x.zip", makeZip(t, files(names...)...), limits)
			ae := wantError(t, err, archive.CodeUnsafePaths)
			if len(ae.Paths) != 1 || !strings.HasPrefix(ae.Paths[0], ".tmp") {
				t.Errorf("paths = %q", ae.Paths)
			}
		})
	}
}

func TestDisallowedFiles(t *testing.T) {
	data := makeZip(t, files("index.html", "img/a.PNG", "x.php", "bin/tool.exe", "README", ".htaccess", "sub/Makefile")...)
	_, err := extract(t, "x.zip", data, limits)
	ae := wantError(t, err, archive.CodeDisallowedFiles)
	want := []string{".htaccess", "README", "bin/tool.exe", "sub/Makefile", "x.php"}
	if !slices.Equal(ae.Paths, want) {
		t.Errorf("paths = %q, want %q", ae.Paths, want)
	}
}

func TestPathListCapped(t *testing.T) {
	names := []string{"index.html"}
	for i := range 60 {
		names = append(names, "f"+strings.Repeat("x", i)+".php")
	}
	_, err := extract(t, "x.zip", makeZip(t, files(names...)...), limits)
	ae := wantError(t, err, archive.CodeDisallowedFiles)
	if len(ae.Paths) != 50 || ae.More != 10 {
		t.Errorf("listed %d, More %d", len(ae.Paths), ae.More)
	}
	if !slices.IsSorted(ae.Paths) {
		t.Error("paths not sorted")
	}
	if !strings.Contains(ae.Error(), "and 10 more") {
		t.Errorf("Error() = %s", ae.Error())
	}
}

func TestMissingIndex(t *testing.T) {
	for name, names := range map[string][]string{
		"only about":    {"about.html"},
		"wrong case":    {"Index.html", "a.css"},
		"in subfolder":  {"site/index.html", "other/a.js"},
		"nested deeper": {"dist/site/index.html"},
		"junk only":     {".DS_Store", "__MACOSX/index.html"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := extract(t, "x.zip", makeZip(t, files(names...)...), limits)
			wantError(t, err, archive.CodeMissingIndex)
		})
	}
}

func TestTooManyFiles(t *testing.T) {
	data := makeZip(t, files("index.html", "a.js", "b.js", ".DS_Store", "__MACOSX/c.js")...)
	if _, err := extract(t, "x.zip", data, archive.Limits{MaxFiles: 3, MaxExtractedBytes: 1 << 20}); err != nil {
		t.Fatalf("junk counted against the limit: %v", err)
	}
	_, err := extract(t, "x.zip", data, archive.Limits{MaxFiles: 2, MaxExtractedBytes: 1 << 20})
	wantError(t, err, archive.CodeTooManyFiles)
}

func TestTooLargeHonestHeaders(t *testing.T) {
	data := makeZip(t,
		entry{name: "index.html", body: strings.Repeat("a", 600)},
		entry{name: "b.js", body: strings.Repeat("b", 600)},
	)
	_, err := extract(t, "x.zip", data, archive.Limits{MaxFiles: 10, MaxExtractedBytes: 1000})
	wantError(t, err, archive.CodeTooLarge)
	if _, err := extract(t, "x.zip", data, archive.Limits{MaxFiles: 10, MaxExtractedBytes: 1200}); err != nil {
		t.Errorf("exactly at the limit: %v", err)
	}
}

// rawEntry writes a deflated entry whose header claims claimedSize bytes
// while the payload really inflates to body.
func rawEntry(t *testing.T, zw *zip.Writer, name string, body []byte, claimedSize uint64) {
	t.Helper()
	var comp bytes.Buffer
	fw, err := flate.NewWriter(&comp, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(body)
	fw.Close()
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               name,
		Method:             zip.Deflate,
		CRC32:              crc32.ChecksumIEEE(body),
		CompressedSize64:   uint64(comp.Len()),
		UncompressedSize64: claimedSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(comp.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestZipBombLyingHeaders(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	rawEntry(t, zw, "index.html", []byte("<p>hi</p>"), 9)
	rawEntry(t, zw, "bomb.js", make([]byte, 8<<20), 10) // 8 MiB of zeros claiming 10 bytes
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() > 1<<20 {
		t.Fatalf("bomb is %d bytes, expected it to compress well", buf.Len())
	}
	_, err := extract(t, "bomb.zip", buf.Bytes(), archive.Limits{MaxFiles: 10, MaxExtractedBytes: 1 << 20})
	wantError(t, err, archive.CodeTooLarge)
}

// An entry that inflates to more than its header claims, but within the
// limit, is corrupt rather than too large.
func TestLyingHeaderWithinLimit(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	rawEntry(t, zw, "index.html", []byte("<p>hi</p>"), 9)
	rawEntry(t, zw, "a.js", make([]byte, 1000), 10)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := extract(t, "x.zip", buf.Bytes(), limits)
	ae := wantError(t, err, archive.CodeInvalidFile)
	if !slices.Equal(ae.Paths, []string{"a.js"}) {
		t.Errorf("paths = %q", ae.Paths)
	}
}

func TestBadChecksum(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{Name: "index.html", Method: zip.Store, CRC32: 1, CompressedSize64: 2, UncompressedSize64: 2})
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("hi"))
	zw.Close()
	_, err = extract(t, "x.zip", buf.Bytes(), limits)
	wantError(t, err, archive.CodeInvalidFile)
}

func TestInvalidLimits(t *testing.T) {
	_, err := archive.Extract("a.html", bytes.NewReader(nil), 0, archive.Limits{}, t.TempDir())
	var ae *archive.Error
	if err == nil || errors.As(err, &ae) {
		t.Errorf("err = %v, want an internal error", err)
	}
}

func TestIsAllowedExt(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"index.html", true},
		{"a.HTM", true},
		{"site.css", true},
		{"app.js", true},
		{"mod.mjs", true},
		{"data.json", true},
		{"app.js.map", true},
		{"robots.txt", true},
		{"feed.xml", true},
		{"site.webmanifest", true},
		{"a.png", true},
		{"a.PNG", true},
		{"a.jpg", true},
		{"a.jpeg", true},
		{"a.gif", true},
		{"a.webp", true},
		{"a.avif", true},
		{"a.svg", true},
		{"favicon.ico", true},
		{"f.woff", true},
		{"f.woff2", true},
		{"f.ttf", true},
		{"f.otf", true},
		{"v.mp4", true},
		{"v.webm", true},
		{"s.mp3", true},
		{"dir/.well-known/x.json", true},
		{"x.php", false},
		{"x.exe", false},
		{"README", false},
		{".htaccess", false},
		{"x.html.bak", false},
		{"x.", false},
		{"", false},
		{"dir.js/file", false},
		{"a.tar.gz", false},
		{"a.zip", false},
		{"a.svgz", false},
	}
	for _, tt := range tests {
		if got := archive.IsAllowedExt(tt.name); got != tt.want {
			t.Errorf("IsAllowedExt(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
