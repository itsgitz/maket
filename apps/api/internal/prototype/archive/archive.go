// Package archive validates an Upload and extracts it into a temp directory.
//
// An Upload is either a single .html/.htm page, stored as index.html, or a
// .zip archive. Archives are checked before anything is written: junk such
// as __MACOSX/ and .DS_Store is dropped, every remaining entry must have a
// safe path and an allowlisted extension, a single top-level folder is
// stripped, and index.html must sit at the root. Extraction counts the real
// decompressed bytes, so headers that lie about sizes cannot get past the
// limits.
package archive

import (
	"archive/zip"
	"compress/flate"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits bound the size of one Upload. Both must be positive.
type Limits struct {
	MaxFiles          int
	MaxExtractedBytes int64
}

// File is one extracted file.
type File struct {
	Path string // relative, "/"-separated, e.g. "assets/app.js"
	Size int64
}

// Result is a validated upload extracted into Dir.
type Result struct {
	Dir       string // temp directory; file f lives at filepath.Join(Dir, filepath.FromSlash(f.Path))
	Files     []File // sorted by Path
	TotalSize int64
}

// Open opens f for reading. Symlinks and ".." cannot leave Dir.
func (r *Result) Open(f File) (*os.File, error) {
	return os.OpenInRoot(r.Dir, filepath.FromSlash(f.Path))
}

// Cleanup removes Dir and everything in it.
func (r *Result) Cleanup() error {
	return os.RemoveAll(r.Dir)
}

// Error codes, as found in Error.Code.
const (
	CodeInvalidFile     = "invalid_file"
	CodeMissingIndex    = "missing_index"
	CodeDisallowedFiles = "disallowed_files"
	CodeUnsafePaths     = "unsafe_paths"
	CodeTooManyFiles    = "too_many_files"
	CodeTooLarge        = "too_large"
)

// Error is a user-facing validation failure.
type Error struct {
	Code    string   // one of the Code constants
	Message string   // one sentence a person or agent can act on
	Paths   []string // offending paths, when relevant (sorted, capped)
	More    int      // how many further paths were not listed
}

func (e *Error) Error() string {
	s := "archive: " + e.Code + ": " + e.Message
	if len(e.Paths) > 0 {
		s += " [" + strings.Join(e.Paths, ", ")
		if e.More > 0 {
			s += fmt.Sprintf(", and %d more", e.More)
		}
		s += "]"
	}
	return s
}

// maxListed caps Error.Paths; the rest are counted in Error.More.
const maxListed = 50

// newPathsError returns an Error listing paths, sorted and capped.
func newPathsError(code, msg string, paths []string) *Error {
	slices.Sort(paths)
	paths = slices.Compact(paths)
	e := &Error{Code: code, Message: msg, Paths: paths}
	if len(paths) > maxListed {
		e.Paths, e.More = paths[:maxListed:maxListed], len(paths)-maxListed
	}
	return e
}

// Path limits keep "prototypes/<slug>/v<version>/<path>" under the 1024-byte
// S3 key limit and each segment under common file name limits.
const (
	maxSegmentBytes = 255
	maxPathBytes    = 900
	reservedPrefix  = ".tmp" // storage keeps bookkeeping under this name
)

// allowedExts is the extension allowlist, lowercase.
var allowedExts = map[string]bool{}

// allowedList is the allowlist in a readable order, for error messages.
const allowedList = ".html .htm .css .js .mjs .json .map .txt .xml .webmanifest " +
	".png .jpg .jpeg .gif .webp .avif .svg .ico " +
	".woff .woff2 .ttf .otf " +
	".mp4 .webm .mp3"

func init() {
	for _, ext := range strings.Fields(allowedList) {
		allowedExts[ext] = true
	}
}

// IsAllowedExt reports whether a file name's extension is on the allowlist.
// The comparison is case-insensitive; a name without an extension is not
// allowed.
func IsAllowedExt(name string) bool {
	return allowedExts[strings.ToLower(path.Ext(name))]
}

// Extract validates and extracts the upload. name is the client's filename;
// r/size are the uploaded bytes (the HTTP layer spools them to a temp file).
// The files are written to a new directory under tmpRoot, which the caller
// removes with Result.Cleanup; on error nothing is left behind.
// Validation errors are *Error; anything else is an internal failure.
func Extract(name string, r io.ReaderAt, size int64, limits Limits, tmpRoot string) (*Result, error) {
	if limits.MaxFiles <= 0 || limits.MaxExtractedBytes <= 0 {
		return nil, fmt.Errorf("archive: limits must be positive, got %+v", limits)
	}
	src := &sourceReader{r: r}
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm":
		return extractPage(src, size, limits, tmpRoot)
	case ".zip":
		return extractZip(src, size, limits, tmpRoot)
	}
	return nil, &Error{Code: CodeInvalidFile, Message: "Upload a single .html file or a .zip archive."}
}

// extractPage stores a single page as index.html.
func extractPage(src *sourceReader, size int64, limits Limits, tmpRoot string) (*Result, error) {
	if size > limits.MaxExtractedBytes {
		return nil, tooLarge(limits)
	}
	x, err := newExtractor(tmpRoot, limits)
	if err != nil {
		return nil, err
	}
	n, err := x.write("index.html", io.NewSectionReader(src, 0, size), src)
	if err != nil {
		return nil, x.fail(err)
	}
	return x.result([]File{{Path: "index.html", Size: n}}), nil
}

// extractZip validates the archive's entries, then extracts them.
func extractZip(src *sourceReader, size int64, limits Limits, tmpRoot string) (*Result, error) {
	zr, err := zip.NewReader(src, size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) { // insecure paths are reported below
		if src.err != nil {
			return nil, fmt.Errorf("archive: read upload: %w", src.err)
		}
		return nil, &Error{Code: CodeInvalidFile, Message: "The file is not a valid zip archive; upload a .zip or a single .html file."}
	}

	var (
		files      []*zip.File
		unsafe     []string
		disallowed []string
		seen       = map[string]bool{}
	)
	for _, f := range zr.File {
		n := f.Name
		if isJunk(n) || strings.HasSuffix(n, "/") {
			continue
		}
		files = append(files, f)
		switch {
		case !safePath(n) || !f.Mode().IsRegular() || seen[n]:
			unsafe = append(unsafe, display(n))
		case !IsAllowedExt(n):
			disallowed = append(disallowed, n)
		}
		seen[n] = true
	}
	// A file whose path is also a parent directory of another file can't be
	// extracted.
	for n := range seen {
		for d := path.Dir(n); d != "." && d != "/"; d = path.Dir(d) {
			if seen[d] {
				unsafe = append(unsafe, display(d))
			}
		}
	}
	if len(unsafe) > 0 {
		return nil, newPathsError(CodeUnsafePaths, "These entries have unsafe paths (absolute, \"..\", \".\" or empty segments, "+
			"backslashes, control characters, symlinks, duplicates, or too long); fix them and upload again.", unsafe)
	}
	if len(disallowed) > 0 {
		return nil, newPathsError(CodeDisallowedFiles, "These files have types that are not allowed; remove them and upload again. "+
			"Allowed: "+allowedList+".", disallowed)
	}
	if len(files) > limits.MaxFiles {
		return nil, &Error{Code: CodeTooManyFiles,
			Message: fmt.Sprintf("The archive has %d files; the limit is %d.", len(files), limits.MaxFiles)}
	}

	paths := make([]string, len(files))
	var declared uint64
	for i, f := range files {
		paths[i] = f.Name
		declared += min(f.UncompressedSize64, uint64(limits.MaxExtractedBytes)+1) // no overflow
	}
	if prefix := topFolder(paths); prefix != "" {
		for i := range paths {
			paths[i] = strings.TrimPrefix(paths[i], prefix)
		}
	}
	for _, p := range paths {
		if strings.HasPrefix(p, reservedPrefix) {
			unsafe = append(unsafe, p)
		}
	}
	if len(unsafe) > 0 {
		return nil, newPathsError(CodeUnsafePaths, "Paths may not start with \""+reservedPrefix+"\"; rename these and upload again.", unsafe)
	}
	if !slices.Contains(paths, "index.html") {
		return nil, &Error{Code: CodeMissingIndex,
			Message: "The upload has no index.html at its root; add one (a single top-level folder is stripped automatically)."}
	}
	if declared > uint64(limits.MaxExtractedBytes) {
		return nil, tooLarge(limits)
	}

	x, err := newExtractor(tmpRoot, limits)
	if err != nil {
		return nil, err
	}
	out := make([]File, len(files))
	for i, f := range files {
		n, err := x.writeEntry(paths[i], f, src)
		if err != nil {
			return nil, x.fail(err)
		}
		out[i] = File{Path: paths[i], Size: n}
	}
	slices.SortFunc(out, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return x.result(out), nil
}

// isJunk reports whether an entry is operating-system clutter to drop
// silently: anything under a __MACOSX directory, .DS_Store and Thumbs.db.
func isJunk(name string) bool {
	segs := strings.Split(name, "/")
	if slices.Contains(segs[:len(segs)-1], "__MACOSX") {
		return true
	}
	base := segs[len(segs)-1]
	return strings.EqualFold(base, ".DS_Store") || strings.EqualFold(base, "Thumbs.db")
}

// safePath reports whether name is a clean relative "/"-separated path that
// fits the storage key limits. Nothing is normalized: "a/./b" and "a//b"
// are unsafe rather than rewritten.
func safePath(name string) bool {
	if name == "" || len(name) > maxPathBytes || !utf8.ValidString(name) || hasControl(name) ||
		strings.ContainsRune(name, '\\') || strings.HasPrefix(name, "/") || hasDrive(name) {
		return false
	}
	for seg := range strings.SplitSeq(name, "/") {
		if seg == "" || seg == "." || seg == ".." || len(seg) > maxSegmentBytes {
			return false
		}
	}
	return true
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(c rune) bool { return c < 0x20 || c == 0x7f })
}

// hasDrive reports whether name starts with a Windows drive such as "C:".
func hasDrive(name string) bool {
	return len(name) >= 2 && name[1] == ':' &&
		('a' <= name[0] && name[0] <= 'z' || 'A' <= name[0] && name[0] <= 'Z')
}

// display returns name as it should be listed in an Error: unchanged when
// printable, quoted when it has control characters or invalid UTF-8.
func display(name string) string {
	if utf8.ValidString(name) && !hasControl(name) {
		return name
	}
	return strconv.Quote(name)
}

// topFolder returns "dir/" when every path is inside the same top-level
// directory dir, else "".
func topFolder(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	dir, _, ok := strings.Cut(paths[0], "/")
	if !ok {
		return ""
	}
	prefix := dir + "/"
	for _, p := range paths {
		if !strings.HasPrefix(p, prefix) {
			return ""
		}
	}
	return prefix
}

func tooLarge(limits Limits) *Error {
	return &Error{Code: CodeTooLarge,
		Message: fmt.Sprintf("The extracted files are larger than the %d-byte limit; upload fewer or smaller files.", limits.MaxExtractedBytes)}
}

func corrupt(name string) *Error {
	return &Error{Code: CodeInvalidFile,
		Message: "The zip archive is corrupt or uses an unsupported feature (only stored and deflated entries without encryption are supported).",
		Paths:   []string{display(name)}}
}

// sourceReader remembers the first real read error from the upload, so a
// failing disk is not reported to the user as a corrupt archive.
type sourceReader struct {
	r   io.ReaderAt
	err error
}

func (s *sourceReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := s.r.ReadAt(p, off)
	if err != nil && err != io.EOF && s.err == nil {
		s.err = err
	}
	return n, err
}

// extractor writes files into a fresh temp directory, counting the bytes
// actually written against the limit.
type extractor struct {
	dir       string
	root      *os.Root
	limits    Limits
	remaining int64
}

func newExtractor(tmpRoot string, limits Limits) (*extractor, error) {
	dir, err := os.MkdirTemp(tmpRoot, "upload-*")
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("archive: %w", err)
	}
	return &extractor{dir: dir, root: root, limits: limits, remaining: limits.MaxExtractedBytes}, nil
}

// fail removes the temp directory and returns err.
func (x *extractor) fail(err error) error {
	x.root.Close()
	os.RemoveAll(x.dir)
	return err
}

func (x *extractor) result(files []File) *Result {
	x.root.Close()
	var total int64
	for _, f := range files {
		total += f.Size
	}
	return &Result{Dir: x.dir, Files: files, TotalSize: total}
}

// writeEntry decompresses f to p. It decompresses the raw data itself
// rather than using f.Open, so the limit applies to the real stream and a
// size mismatch is caught only after it, not before.
func (x *extractor) writeEntry(p string, f *zip.File, src *sourceReader) (int64, error) {
	if f.Flags&0x1 != 0 { // encrypted
		return 0, corrupt(f.Name)
	}
	raw, err := f.OpenRaw()
	if err != nil {
		if src.err != nil {
			return 0, fmt.Errorf("archive: read upload: %w", src.err)
		}
		return 0, corrupt(f.Name)
	}
	var r io.Reader
	switch f.Method {
	case zip.Store:
		r = raw
	case zip.Deflate:
		fr := flate.NewReader(raw)
		defer fr.Close()
		r = fr
	default:
		return 0, corrupt(f.Name)
	}
	h := crc32.NewIEEE()
	n, err := x.write(p, io.TeeReader(r, h), src)
	if err != nil {
		var ve *Error
		if errors.As(err, &ve) && ve.Code == CodeInvalidFile {
			return 0, corrupt(f.Name)
		}
		return 0, err
	}
	if uint64(n) != f.UncompressedSize64 || f.CRC32 != 0 && h.Sum32() != f.CRC32 {
		return 0, corrupt(f.Name)
	}
	return n, nil
}

// write copies r to p inside the temp directory, failing with too_large as
// soon as the total written would pass the limit.
func (x *extractor) write(p string, r io.Reader, src *sourceReader) (int64, error) {
	p = filepath.FromSlash(p)
	if d := filepath.Dir(p); d != "." {
		if err := x.root.MkdirAll(d, 0o755); err != nil {
			return 0, fmt.Errorf("archive: create %s: %w", d, err)
		}
	}
	w, err := x.root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, fmt.Errorf("archive: create %s: %w", p, err)
	}
	cr := &errReader{r: io.LimitReader(r, x.remaining+1)}
	n, err := io.Copy(w, cr)
	if cerr := w.Close(); err == nil && cerr != nil {
		err = cerr
	}
	switch {
	case cr.err != nil && src.err != nil:
		return 0, fmt.Errorf("archive: read upload: %w", src.err)
	case cr.err != nil:
		return 0, &Error{Code: CodeInvalidFile, Message: "The upload could not be read."}
	case err != nil:
		return 0, fmt.Errorf("archive: write %s: %w", p, err)
	case n > x.remaining:
		return 0, tooLarge(x.limits)
	}
	x.remaining -= n
	return n, nil
}

// errReader records the error from its reader, other than io.EOF, so a
// failed read can be told apart from a failed write after io.Copy.
type errReader struct {
	r   io.Reader
	err error
}

func (e *errReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF {
		e.err = err
	}
	return n, err
}
