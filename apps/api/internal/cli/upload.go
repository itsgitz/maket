package cli

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// uploadSource is what push sends: a file as is, or a zipped folder.
type uploadSource struct {
	filename string // the multipart file name; the server derives the default name from it
	size     int64
	open     func() (io.ReadCloser, error)
}

// pushableFile reports whether a file name has an extension the server
// accepts as an Upload.
func pushableFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html", ".htm", ".zip":
		return true
	}
	return false
}

// checkPushPath validates path before anything is read or sent: it must be
// a folder or an .html/.htm/.zip file.
func checkPushPath(path string) (fs.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, usageError("%s: no such file or folder", path)
		}
		return nil, localError("%v", err)
	}
	if info.IsDir() {
		return info, nil
	}
	if !info.Mode().IsRegular() || !pushableFile(path) {
		return nil, usageError("%s: unsupported file type: push an .html or .htm file, a .zip, or a folder", path)
	}
	return info, nil
}

// newUploadSource prepares path, already checked by checkPushPath. A folder
// is zipped in memory.
func newUploadSource(path string, info fs.FileInfo) (uploadSource, error) {
	if !info.IsDir() {
		return uploadSource{
			filename: filepath.Base(path),
			size:     info.Size(),
			open:     func() (io.ReadCloser, error) { return os.Open(path) },
		}, nil
	}
	b, err := zipFolder(path)
	if err != nil {
		return uploadSource{}, err
	}
	return uploadSource{
		filename: zipName(path),
		size:     int64(len(b)),
		open:     func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil },
	}, nil
}

// zipName is "<folder name>.zip", using the absolute path so "." becomes the
// current folder's name.
func zipName(dir string) string {
	base := filepath.Base(dir)
	if abs, err := filepath.Abs(dir); err == nil {
		base = filepath.Base(abs)
	}
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "upload"
	}
	return base + ".zip"
}

// zipFolder zips every regular file under dir, with "/"-separated paths
// relative to dir. Files and folders whose name starts with "." are left
// out, as are symlinks and other non-regular files. An empty result is an
// error.
func zipFolder(dir string) ([]byte, error) {
	root, err := filepath.EvalSymlinks(dir) // a symlinked folder is still pushed
	if err != nil {
		return nil, localError("%v", err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	n := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(w, f); err != nil {
			return err
		}
		n++
		return nil
	})
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		return nil, localError("zip %s: %v", dir, err)
	}
	if n == 0 {
		return nil, usageError("%s: the folder has no files to upload (names starting with \".\" are left out)", dir)
	}
	return buf.Bytes(), nil
}

// uploadFields are the optional form fields of an Upload; nil is omitted, so
// a new Version keeps the current value.
type uploadFields struct {
	name *string
	spa  *bool
}

// multipartBody streams src and fields as multipart/form-data. It returns
// the body and its content type.
func multipartBody(src uploadSource, fields uploadFields) (io.Reader, string, error) {
	f, err := src.open()
	if err != nil {
		return nil, "", localError("%v", err)
	}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		defer f.Close()
		pw.CloseWithError(writeMultipart(mw, src.filename, f, fields))
	}()
	return pr, mw.FormDataContentType(), nil
}

func writeMultipart(mw *multipart.Writer, filename string, f io.Reader, fields uploadFields) error {
	if fields.name != nil {
		if err := mw.WriteField("name", *fields.name); err != nil {
			return err
		}
	}
	if fields.spa != nil {
		if err := mw.WriteField("spa", strconv.FormatBool(*fields.spa)); err != nil {
			return err
		}
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, f); err != nil {
		return err
	}
	return mw.Close()
}
