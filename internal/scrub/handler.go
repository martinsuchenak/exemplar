package scrub

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/martinsuchenak/exemplar/internal/anonymise"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/rest"
)

// Handler serves the anonymise tool over HTTP. It only parses the request
// and writes the result; the service decides what the caller may do.
type Handler struct {
	service *Service
}

// NewHandler returns a Handler backed by s.
func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// ManifestName is the zip entry describing a multi-file run.
const ManifestName = "manifest.json"

// maxField caps a form field other than a file.
const maxField = 1024

// Anonymise takes either one file as the raw body, named by ?name= (or
// typed by ?format=), and answers with the anonymised file in the same
// format; or a multipart/form-data upload of "file" parts, anonymised as one
// batch sharing fakes, and answers with a zip of the files plus a manifest.
// Bodies are read into memory, never into temporary files. Nothing returned
// is original data, and nothing is cached.
func (h *Handler) Anonymise(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	rest.AllowLongUpload(w)
	if limit := h.service.MaxBytes(); limit > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	}
	q := r.URL.Query()
	req := Request{Profile: q.Get("profile")}
	if v := q.Get("link_registry"); v != "" {
		var err error
		if req.LinkRegistry, err = strconv.ParseBool(v); err != nil {
			rest.RespondError(w, http.StatusBadRequest, "link_registry must be true or false")
			return
		}
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "multipart/form-data" {
		h.many(w, r, p, req)
		return
	}

	name := q.Get("name")
	if f := q.Get("format"); f != "" {
		byFormat := anonymise.NameFor(anonymise.Format(f))
		switch {
		case byFormat == "":
			rest.RespondError(w, http.StatusBadRequest, "unknown format "+strconv.Quote(f))
			return
		case name == "":
			name = byFormat
		default:
			if got, _ := anonymise.FormatOf(name); string(got) != f {
				rest.RespondError(w, http.StatusBadRequest, "format and name disagree")
				return
			}
		}
	}
	if name == "" {
		rest.RespondError(w, http.StatusBadRequest, "give the file's name (?name=notes.md) or format (?format=md), or send multipart/form-data")
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		respondReadError(w, err)
		return
	}
	res, err := h.service.Anonymise(r.Context(), p, req, []anonymise.File{{Name: name, Data: data}})
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	f := res.Files[0]
	if f.Error != "" {
		rest.RespondError(w, http.StatusBadRequest, f.Error)
		return
	}
	hd := w.Header()
	setCommon(hd)
	hd.Set("Content-Type", anonymise.MediaType(f.Format))
	hd.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
	hd.Set("X-Exemplar-Format", string(f.Format))
	hd.Set("X-Exemplar-Replacements", strconv.Itoa(f.Replacements))
	hd.Set("X-Exemplar-Attachments", strconv.Itoa(f.Attachments))
	hd.Set("X-Exemplar-Dropped-Headers", strconv.Itoa(f.DroppedHeaders))
	hd.Set("Content-Length", strconv.Itoa(len(f.Data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(f.Data)
}

// many handles a multipart upload: "file" parts plus optional "profile" and
// "link_registry" fields, which override the query.
func (h *Handler) many(w http.ResponseWriter, r *http.Request, p principal.Principal, req Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		rest.RespondError(w, http.StatusBadRequest, "body must be multipart/form-data with file parts")
		return
	}
	var files []anonymise.File
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			respondReadError(w, err)
			return
		}
		switch part.FormName() {
		case "file":
			data, err := io.ReadAll(part)
			if err != nil {
				respondReadError(w, err)
				return
			}
			files = append(files, anonymise.File{Name: part.FileName(), Data: data})
		case "profile", "link_registry":
			v, err := io.ReadAll(io.LimitReader(part, maxField))
			if err != nil {
				respondReadError(w, err)
				return
			}
			if part.FormName() == "profile" {
				req.Profile = string(v)
			} else if req.LinkRegistry, err = strconv.ParseBool(string(v)); err != nil {
				rest.RespondError(w, http.StatusBadRequest, "link_registry must be true or false")
				return
			}
		}
	}
	res, err := h.service.Anonymise(r.Context(), p, req, files)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	hd := w.Header()
	setCommon(hd)
	hd.Set("Content-Type", "application/zip")
	hd.Set("Content-Disposition", `attachment; filename="anonymised.zip"`)
	w.WriteHeader(http.StatusOK)
	// The run is done before the first byte, so a failure here can only be
	// the connection; the client then sees a broken zip, not a short one.
	_ = WriteZip(w, res)
}

// WriteZip writes a run's files under their fake names, then the manifest.
func WriteZip(w io.Writer, res *Result) error {
	zw := zip.NewWriter(w)
	for _, f := range res.Files {
		if f.Error != "" {
			continue
		}
		fw, err := zw.Create(f.Name)
		if err != nil {
			return err
		}
		if _, err := fw.Write(f.Data); err != nil {
			return err
		}
	}
	fw, err := zw.Create(ManifestName)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(fw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		return err
	}
	return zw.Close()
}

// ReadZip reads a zip WriteZip wrote back into a Result with its files.
func ReadZip(data []byte) (*Result, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		entries[f.Name] = f
	}
	read := func(name string) ([]byte, error) {
		f, ok := entries[name]
		if !ok {
			return nil, errors.New("the archive has no " + name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		return io.ReadAll(rc)
	}
	raw, err := read(ManifestName)
	if err != nil {
		return nil, err
	}
	var res Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	for i := range res.Files {
		if res.Files[i].Error != "" {
			continue
		}
		// The CLI writes each file under this name: it must not reach
		// outside the output directory, whatever the server says.
		if !PlainName(res.Files[i].Name) {
			return nil, fmt.Errorf("the archive names a file %q, which is not a plain file name", res.Files[i].Name)
		}
		if res.Files[i].Data, err = read(res.Files[i].Name); err != nil {
			return nil, err
		}
	}
	return &res, nil
}

// PlainName reports whether name is a file name with no directory part, so
// it is safe to join to an output directory.
func PlainName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00") && path.Base(name) == name
}

// setCommon marks a response as unreviewed output that must not be cached
// or run in a browser.
func setCommon(h http.Header) {
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("X-Exemplar-Unreviewed", "true")
}

func respondReadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		rest.RespondError(w, http.StatusRequestEntityTooLarge, "upload too large")
		return
	}
	rest.RespondError(w, http.StatusBadRequest, "reading upload failed")
}
