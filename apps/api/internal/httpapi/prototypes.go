package httpapi

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/auth"
	"github.com/itsgitz/maket/apps/api/internal/jsonapi"
	"github.com/itsgitz/maket/apps/api/internal/prototype"
	"github.com/itsgitz/maket/apps/api/internal/prototype/archive"
)

type prototypeHandlers struct {
	svc            *prototype.Service
	maxUploadBytes int64
	tmpDir         string
	previewURL     func(slug string) string
}

type prototypeResponse struct {
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	SPA       bool      `json:"spa"`
	Version   int       `json:"version"`
	SizeBytes int64     `json:"size_bytes"`
	FileCount int       `json:"file_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (h prototypeHandlers) toResponse(p prototype.Prototype) prototypeResponse {
	return prototypeResponse{
		Slug: p.Slug, Name: p.Name, URL: h.previewURL(p.Slug), SPA: p.SPA, Version: p.Version,
		SizeBytes: p.SizeBytes, FileCount: p.FileCount, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func (h prototypeHandlers) list(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	protos, err := h.svc.List(r.Context(), p.User.ID)
	if err != nil {
		internalError(w, "list prototypes", err)
		return
	}
	used, err := h.svc.Usage(r.Context(), p.User.ID)
	if err != nil {
		internalError(w, "usage", err)
		return
	}
	out := make([]prototypeResponse, len(protos))
	for i, proto := range protos {
		out[i] = h.toResponse(proto)
	}
	jsonapi.Write(w, http.StatusOK, map[string]any{
		"prototypes": out,
		"usage":      map[string]int64{"used_bytes": used, "quota_bytes": h.svc.QuotaBytes()},
	})
}

func (h prototypeHandlers) get(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	proto, err := h.svc.Get(r.Context(), p.User.ID, r.PathValue("slug"))
	if err != nil {
		h.writeError(w, "get prototype", err)
		return
	}
	jsonapi.Write(w, http.StatusOK, h.toResponse(proto))
}

func (h prototypeHandlers) create(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	up, done, ok := h.readUpload(w, r)
	if !ok {
		return
	}
	defer done()
	proto, err := h.svc.Create(r.Context(), p.User.ID, up)
	if err != nil {
		h.writeError(w, "create prototype", err)
		return
	}
	jsonapi.Write(w, http.StatusCreated, h.toResponse(proto))
}

func (h prototypeHandlers) replace(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	up, done, ok := h.readUpload(w, r)
	if !ok {
		return
	}
	defer done()
	proto, err := h.svc.Replace(r.Context(), p.User.ID, r.PathValue("slug"), up)
	if err != nil {
		h.writeError(w, "replace prototype", err)
		return
	}
	jsonapi.Write(w, http.StatusOK, h.toResponse(proto))
}

func (h prototypeHandlers) delete(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if err := h.svc.Delete(r.Context(), p.User.ID, r.PathValue("slug")); err != nil {
		h.writeError(w, "delete prototype", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// readUpload streams the multipart body: the "file" part is spooled to a
// temp file (never held in memory), "name" and "spa" are read as fields.
// done removes the temp file. On failure the response is already written.
func (h prototypeHandlers) readUpload(w http.ResponseWriter, r *http.Request) (up prototype.Upload, done func(), ok bool) {
	done = func() {}
	// Room for the file plus the other parts and multipart framing.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUploadBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		jsonapi.Error(w, http.StatusBadRequest, "invalid_body", `expected multipart/form-data with a "file" field`)
		return up, done, false
	}

	var spool *os.File
	cleanup := func() {
		if spool != nil {
			spool.Close()
			os.Remove(spool.Name())
		}
	}
	fail := func(status int, code, msg string) (prototype.Upload, func(), bool) {
		cleanup()
		jsonapi.Error(w, status, code, msg)
		return prototype.Upload{}, func() {}, false
	}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return fail(http.StatusRequestEntityTooLarge, "too_large", tooLargeMessage(h.maxUploadBytes))
		}
		if err != nil {
			return fail(http.StatusBadRequest, "invalid_body", "malformed multipart body")
		}

		switch part.FormName() {
		case "file":
			if spool != nil {
				return fail(http.StatusBadRequest, "invalid_body", `send exactly one "file"`)
			}
			if spool, err = os.CreateTemp(h.tmpDir, "body-*"); err != nil {
				internalError(w, "spool upload", err)
				cleanup()
				return prototype.Upload{}, func() {}, false
			}
			n, err := io.Copy(spool, io.LimitReader(part, h.maxUploadBytes+1))
			if errors.As(err, &tooBig) || n > h.maxUploadBytes {
				return fail(http.StatusRequestEntityTooLarge, "too_large", tooLargeMessage(h.maxUploadBytes))
			}
			if err != nil {
				return fail(http.StatusBadRequest, "invalid_body", "the upload was interrupted")
			}
			up.Filename, up.File, up.Size = part.FileName(), spool, n
		case "name":
			v, err := readField(part)
			if err != nil {
				return fail(http.StatusBadRequest, "invalid_body", `"name" is too long`)
			}
			if v = strings.TrimSpace(v); v != "" {
				up.Name = &v
			}
		case "spa":
			v, err := readField(part)
			if err != nil {
				return fail(http.StatusBadRequest, "invalid_body", `"spa" must be true or false`)
			}
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true", "1", "on":
				t := true
				up.SPA = &t
			case "false", "0", "off":
				f := false
				up.SPA = &f
			case "":
			default:
				return fail(http.StatusBadRequest, "invalid_body", `"spa" must be true or false`)
			}
		}
		part.Close()
	}

	if spool == nil {
		return fail(http.StatusBadRequest, "invalid_file", `no "file" field: attach an .html file or a .zip`)
	}
	return up, cleanup, true
}

// readField reads a small form field.
func readField(part *multipart.Part) (string, error) {
	b, err := io.ReadAll(io.LimitReader(part, 1<<10+1))
	if err != nil || len(b) > 1<<10 {
		return "", errors.New("field too long")
	}
	return string(b), nil
}

func tooLargeMessage(max int64) string {
	return "the upload is larger than the " + humanMB(max) + " limit"
}

func humanMB(n int64) string {
	return strconv.FormatFloat(float64(n)/(1<<20), 'f', -1, 64) + " MB"
}

func (h prototypeHandlers) writeError(w http.ResponseWriter, what string, err error) {
	var ae *archive.Error
	switch {
	case errors.As(err, &ae):
		status := http.StatusBadRequest
		if ae.Code == "too_large" {
			status = http.StatusRequestEntityTooLarge
		}
		var details any
		if len(ae.Paths) > 0 {
			details = map[string]any{"paths": ae.Paths, "more": ae.More}
		}
		jsonapi.ErrorDetails(w, status, ae.Code, ae.Message, details)
	case errors.Is(err, prototype.ErrNotFound):
		jsonapi.Error(w, http.StatusNotFound, "not_found", "no such prototype")
	case errors.Is(err, prototype.ErrQuotaExceeded):
		jsonapi.Error(w, http.StatusRequestEntityTooLarge, "quota_exceeded", "this upload would take you over your storage quota of "+humanMB(h.svc.QuotaBytes())+"; delete a prototype first")
	case errors.Is(err, prototype.ErrInvalidName):
		jsonapi.Error(w, http.StatusBadRequest, "invalid_name", "name must be 1 to 100 characters")
	default:
		internalError(w, what, err)
	}
}
