package anonymise

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

// Every format the engine reads has a handler that finds the text in a file
// and writes the file back with the fakes in it. Handlers live in this
// package and use the engine's detection, fakes and registry, so the same
// person gets the same fake in every format. Email has its own handler;
// every document format shares docHandler (document.go). See
// docs/architecture.md and docs/anonymisation.md.

// Format names a file format by its usual extension.
type Format string

// The formats the engine reads.
const (
	FormatEmail    Format = "eml"
	FormatText     Format = "txt"
	FormatMarkdown Format = "md"
	FormatCSV      Format = "csv"
	FormatTSV      Format = "tsv"
	FormatJSON     Format = "json"
	FormatJSONL    Format = "jsonl"
	FormatHTML     Format = "html"
	FormatTwig     Format = "twig"
	FormatJinja    Format = "jinja"
	FormatDOCX     Format = "docx"
	FormatRTF      Format = "rtf"
	FormatXLSX     Format = "xlsx"
)

// File is one file to anonymise, under the name it arrived with. The name
// picks the format, and is original data like the content.
type File struct {
	Name string
	Data []byte
}

// handler reads and rewrites one format.
type handler interface {
	// prepare learns what a file says about people before any file of
	// the batch is anonymised.
	prepare(ctx context.Context, e *Engine, f File) error
	// anonymise rewrites one file. It leaves Output.Format and Name to
	// the engine.
	anonymise(ctx context.Context, e *Engine, f File) (*Output, error)
}

type formatDef struct {
	format Format
	// exts are the lower-case extensions that select the format.
	exts []string
	// prefix starts the file's fake name.
	prefix string
	// mediaType is what the file is served as. Text keeps the encoding
	// it came in, so no charset is claimed.
	mediaType string
	handler   handler
}

var formats = []formatDef{
	{FormatEmail, []string{".eml"}, "email", "message/rfc822", emailHandler{}},
	{FormatText, []string{".txt", ".text", ".log"}, "document", "text/plain", docHandler{parse: parsePlain}},
	{FormatMarkdown, []string{".md", ".markdown"}, "document", "text/markdown", docHandler{parse: parsePlain}},
	{FormatCSV, []string{".csv"}, "document", "text/csv", docHandler{parse: parseCSV(',')}},
	{FormatTSV, []string{".tsv"}, "document", "text/tab-separated-values", docHandler{parse: parseCSV('\t')}},
	{FormatJSON, []string{".json"}, "document", "application/json", docHandler{parse: parseJSON(false)}},
	{FormatJSONL, []string{".jsonl", ".ndjson"}, "document", "application/x-ndjson", docHandler{parse: parseJSON(true)}},
	{FormatHTML, []string{".html", ".htm"}, "document", "text/html", docHandler{parse: parseHTML}},
	{FormatTwig, []string{".twig"}, "document", "text/plain", docHandler{parse: parseTemplate}},
	{FormatJinja, []string{".j2", ".jinja", ".jinja2"}, "document", "text/plain", docHandler{parse: parseTemplate}},
	{FormatDOCX, []string{".docx"}, "document", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ooxmlHandler{docxFormat}},
	{FormatRTF, []string{".rtf"}, "document", "application/rtf", rtfHandler{}},
	{FormatXLSX, []string{".xlsx"}, "document", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ooxmlHandler{xlsxFormat}},
}

// notYet explains the extensions people try that cannot be read yet.
var notYet = map[string]string{
	".msg": "MSG files are not supported yet; convert to .eml first",
}

// Formats lists the formats the engine reads, in the order they were added.
func Formats() []Format {
	out := make([]Format, len(formats))
	for i, f := range formats {
		out[i] = f.format
	}
	return out
}

// NameFor returns a file name that selects format f ("input.md"), for
// content that arrives without one.
func NameFor(f Format) string {
	for _, def := range formats {
		if def.format == f {
			return "input" + def.exts[0]
		}
	}
	return ""
}

// FormatOf returns the format a file name's extension selects, or an error
// that says which extensions are supported.
func FormatOf(name string) (Format, error) {
	def, err := findFormat(name)
	if err != nil {
		return "", err
	}
	return def.format, nil
}

func findFormat(name string) (*formatDef, error) {
	ext := strings.ToLower(path.Ext(name))
	for i, f := range formats {
		for _, x := range f.exts {
			if ext == x {
				return &formats[i], nil
			}
		}
	}
	if msg, ok := notYet[ext]; ok {
		return nil, errors.New(msg)
	}
	var exts []string
	for _, f := range formats {
		exts = append(exts, f.exts...)
	}
	if ext == "" || ext == "." {
		ext = "a file without an extension"
	}
	return nil, fmt.Errorf("cannot anonymise %s: the supported files are %s", ext, strings.Join(exts, ", "))
}

// Extension returns a file name's extension as anonymised files keep it:
// lower-cased, and for a template also the extension of what it renders
// (".html.twig"), which says how its text is read.
func Extension(name string) string {
	name = strings.ToLower(name)
	ext := path.Ext(name)
	if def, err := findFormat(name); err == nil && (def.format == FormatTwig || def.format == FormatJinja) {
		inner := path.Ext(strings.TrimSuffix(name, ext))
		if _, ok := templateRenders[inner]; ok {
			return inner + ext
		}
	}
	return ext
}

// Prepare reads every file of the batch before any is anonymised, so that a
// person named in one file is recognised in all of them: for email, the
// people in its address headers; for a document, the names detection finds
// in it. It returns one error per file, nil for the ones it read: a file
// that cannot be read fails on its own, not the batch.
func (e *Engine) Prepare(ctx context.Context, files []File) []error {
	errs := make([]error, len(files))
	for i, f := range files {
		def, err := findFormat(f.Name)
		if err == nil {
			err = def.handler.prepare(ctx, e, f)
		}
		errs[i] = err
	}
	return errs
}

// Anonymise rewrites one file. Prepare must have seen it.
func (e *Engine) Anonymise(ctx context.Context, f File) (*Output, error) {
	def, err := findFormat(f.Name)
	if err != nil {
		return nil, err
	}
	out, err := def.handler.anonymise(ctx, e, f)
	if err != nil {
		return nil, err
	}
	out.Format, out.Name = def.format, e.fakeFileName(def.prefix, f.Name)
	return out, nil
}

// fakeFileName names a file as attachments are named: the same original
// name always gets the same fake, so references between files still match.
// The extension is kept (see Extension).
func (e *Engine) fakeFileName(prefix, name string) string {
	return prefix + "-" + e.key.Hex("file", name, 6) + Extension(name)
}
