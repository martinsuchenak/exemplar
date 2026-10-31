package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/anonymise"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/client"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/scrub"
)

func init() {
	Register(anonymiseCmd())
}

// unreviewedWarning ends every anonymise run.
const unreviewedWarning = "exemplar: the output is anonymised but not reviewed; check it before sharing it."

// anonymiseCmd is the anonymise tool: files in, anonymised files out in the
// same format, nothing stored. It uses the server (any key), or with --local
// runs the anonymiser in this process, with no server or key at all.
func anonymiseCmd() *cli.Command {
	return &cli.Command{
		Name:      "anonymise",
		Usage:     "Anonymise files and print or write them, storing nothing (any key, or --local)",
		Arguments: []cli.Argument{&cli.StringArg{Name: "file", Usage: "Files or directories, or - for stdin", Required: true}},
		MaxArgs:   cli.UnlimitedArgs,
		Flags: withClientFlags(append([]cli.Flag{
			&cli.BoolFlag{Name: "local", Usage: "Run the anonymiser here, without a server (needs --presidio-url or --patterns-only)"},
			&cli.StringFlag{Name: "profile", Usage: "Anonymisation profile (default: default)"},
			&cli.StringFlag{Name: "output", Usage: "File to write one file's output to (default: stdout); never overwritten"},
			&cli.StringFlag{Name: "out-dir", Usage: "Directory for several files' output, under their fake names; must be empty or new"},
			&cli.StringFlag{Name: "format", Usage: "Format of stdin (md, csv, json, …)"},
			&cli.StringFlag{Name: "name", Usage: "File name of stdin, which picks its format"},
			&cli.BoolFlag{Name: "report", Usage: "Print every replacement to stderr (--local only; prints original data)"},
			&cli.BoolFlag{Name: "quiet", Usage: "Leave out the per-file summary on stderr"},
			&cli.BoolFlag{Name: "check", Usage: "List on stderr what came through unchanged (addresses, long numbers, names): values in the output only"},
			&cli.StringFlag{Name: "check-json", Usage: "Write that list as JSON to a new file, or - for stdout when the files go to --output or --out-dir"},
			&cli.BoolFlag{Name: "patterns-only", Usage: "With --local, run without Presidio: names in prose are missed"},
			&cli.BoolFlag{Name: "link-registry", Usage: "Fakes from the server's key and registry, as ingest makes (admin key)"},
		}, anonymiserFlags()...)...),
		Run: func(ctx context.Context, cmd *cli.Command) error {
			inputs, err := anonymiseInputs(cmd)
			if err != nil {
				return err
			}
			outDir, output := cmd.GetString("out-dir"), cmd.GetString("output")
			switch {
			case outDir != "" && output != "":
				return errors.New("give --output or --out-dir, not both")
			case len(inputs) > 1 && outDir == "":
				return fmt.Errorf("%d files need --out-dir <dir>", len(inputs))
			}
			if outDir != "" {
				if err := emptyDir(outDir); err != nil {
					return err
				}
			}
			checkJSON := cmd.GetString("check-json")
			checking := cmd.GetBool("check") || checkJSON != ""
			if err := checkJSONPath(checkJSON, outDir, output); err != nil {
				return err
			}
			if checking {
				// The check compares each output with its original, which a
				// pipe cannot give twice.
				for i := range inputs {
					if err := inputs[i].load(); err != nil {
						return err
					}
				}
			}
			req := scrub.Request{Profile: cmd.GetString("profile"), LinkRegistry: cmd.GetBool("link-registry"), Report: cmd.GetBool("report")}

			var res *scrub.Result
			if cmd.GetBool("local") {
				res, err = anonymiseLocally(ctx, cmd, req, inputs)
			} else {
				if req.Report {
					return errors.New("--report needs --local: the server never returns original data (ingest --dry-run shows a report)")
				}
				res, err = anonymiseOnServer(ctx, cmd, req, inputs, outDir == "")
			}
			if err != nil {
				return err
			}
			if err := writeAnonymised(res, outDir, output); err != nil {
				return err
			}
			var checks []checkFile
			if checking {
				checks = check(ctx, app.Checker(), inputs, res)
				if checkJSON != "" {
					if err := writeCheckJSON(checkJSON, checks); err != nil {
						return err
					}
				}
			}
			return summarise(cmd, inputs, res, checks)
		},
	}
}

// anonymiseInput is one file named on the command line, or stdin.
type anonymiseInput struct {
	name string // its base name, which picks the format
	path string // empty for stdin
	data []byte // its content, once loaded
}

// load reads the input into memory, so it can be read again.
func (in *anonymiseInput) load() error {
	r, err := in.open()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if in.data, err = io.ReadAll(r); err != nil {
		return fmt.Errorf("reading %s: %w", in.name, err)
	}
	return nil
}

func anonymiseInputs(cmd *cli.Command) ([]anonymiseInput, error) {
	first, err := requireArg(cmd, "file")
	if err != nil {
		return nil, err
	}
	args := append([]string{first}, cmd.GetArgs()...)
	name, format := cmd.GetString("name"), cmd.GetString("format")
	if len(args) == 1 && args[0] == "-" {
		if name == "" && format != "" {
			if name = anonymise.NameFor(anonymise.Format(format)); name == "" {
				return nil, fmt.Errorf("unknown format %q", format)
			}
		}
		if name == "" {
			return nil, errors.New("reading stdin needs --format or --name to say what it is")
		}
		return []anonymiseInput{{name: filepath.Base(name)}}, nil
	}
	if name != "" || format != "" {
		return nil, errors.New("--name and --format are for stdin (-); a file's name gives its format")
	}
	paths, err := expandPaths(args)
	if err != nil {
		return nil, err
	}
	out := make([]anonymiseInput, len(paths))
	for i, p := range paths {
		out[i] = anonymiseInput{name: filepath.Base(p), path: p}
	}
	return out, nil
}

func (in anonymiseInput) open() (io.ReadCloser, error) {
	if in.data != nil {
		return io.NopCloser(bytes.NewReader(in.data)), nil
	}
	if in.path == "" {
		return io.NopCloser(stdin), nil
	}
	return os.Open(in.path)
}

// anonymiseLocally reads the files and runs the anonymiser in this process.
func anonymiseLocally(ctx context.Context, cmd *cli.Command, req scrub.Request, inputs []anonymiseInput) (*scrub.Result, error) {
	key, err := anonymiserKey(cmd.GetString("anon-key-file"), os.Getenv("EXEMPLAR_ANON_KEY"))
	if err != nil {
		return nil, err
	}
	svc, err := app.LocalAnonymiser(app.LocalConfig{
		Key:          key,
		PresidioURL:  cmd.GetString("presidio-url"),
		PatternsOnly: cmd.GetBool("patterns-only"),
		ProfilesDir:  cmd.GetString("anon-profiles-dir"),
	})
	if err != nil {
		return nil, err
	}
	files := make([]anonymise.File, len(inputs))
	for i, in := range inputs {
		r, err := in.open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", in.name, err)
		}
		files[i] = anonymise.File{Name: in.name, Data: data}
	}
	return svc.Anonymise(ctx, principal.Principal{}, req, files)
}

// anonymiseOnServer sends one file raw, or several as one batch.
func anonymiseOnServer(ctx context.Context, cmd *cli.Command, req scrub.Request, inputs []anonymiseInput, one bool) (*scrub.Result, error) {
	if cmd.GetBool("patterns-only") {
		return nil, errors.New("--patterns-only is for --local; the server uses its own detector")
	}
	c, err := newClient(cmd)
	if err != nil {
		return nil, err
	}
	if one {
		r, err := inputs[0].open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = r.Close() }()
		var out bytes.Buffer
		f, err := c.AnonymiseFile(ctx, req, inputs[0].name, r, &out)
		if err != nil {
			return nil, err
		}
		f.Data = out.Bytes()
		return &scrub.Result{Profile: req.Profile, Linked: req.LinkRegistry, Files: []scrub.FileResult{*f}}, nil
	}
	files := make([]client.IngestFile, len(inputs))
	for i, in := range inputs {
		files[i] = client.IngestFile{Name: in.name, Open: in.open}
	}
	return c.AnonymiseFiles(ctx, req, files)
}

// writeAnonymised writes one file to --output or stdout, or every file to
// --out-dir under its fake name. Nothing existing is overwritten.
func writeAnonymised(res *scrub.Result, outDir, output string) error {
	if outDir == "" {
		f := res.Files[0]
		if f.Error != "" {
			return nil // reported by summarise
		}
		if output == "" {
			_, err := stdout.Write(f.Data)
			return err
		}
		return writeNew(output, f.Data)
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	for _, f := range res.Files {
		if f.Error != "" {
			continue
		}
		if !scrub.PlainName(f.Name) {
			return fmt.Errorf("refusing to write %q: not a plain file name", f.Name)
		}
		if err := writeNew(filepath.Join(outDir, f.Name), f.Data); err != nil {
			return err
		}
	}
	return nil
}

// writeNew creates path, which must not exist, readable by its owner only:
// the output is not reviewed yet.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// emptyDir checks dir is empty or does not exist yet.
func emptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("--out-dir %s is not empty", dir)
	}
	return nil
}

// summarise reports each file on stderr, so stdout carries only the file:
// the original name (from this command line, never from the server), its
// fake, the count, or why it failed. --report adds every replacement. The
// warning that the output is unreviewed always comes last.
func summarise(cmd *cli.Command, inputs []anonymiseInput, res *scrub.Result, checks []checkFile) error {
	failed := 0
	for i, f := range res.Files {
		switch {
		case f.Error != "":
			failed++
			if len(res.Files) > 1 { // one file's error is the command's
				_, _ = fmt.Fprintf(stderr, "%d %s failed: %s\n", i+1, inputs[i].name, f.Error)
			}
		case !cmd.GetBool("quiet"):
			line := fmt.Sprintf("%d %s -> %s, %d replacements", i+1, inputs[i].name, f.Name, f.Replacements)
			if f.AttachmentsRewritten > 0 {
				line += fmt.Sprintf(", %d attachments rewritten", f.AttachmentsRewritten)
			}
			if len(f.DroppedParts) > 0 {
				line += fmt.Sprintf(", %d parts dropped (%s)", len(f.DroppedParts), strings.Join(f.DroppedParts, ", "))
			}
			_, _ = fmt.Fprintln(stderr, line)
		}
	}
	if cmd.GetBool("check") {
		printChecks(checks)
	}
	if cmd.GetBool("report") {
		_, _ = fmt.Fprintln(stderr, "The report below contains ORIGINAL personal data: do not paste, share or keep it.")
		for i, f := range res.Files {
			if len(f.Report) == 0 {
				continue
			}
			_, _ = fmt.Fprintf(stderr, "\n%s\n", inputs[i].name)
			for _, l := range f.Report {
				_, _ = fmt.Fprintf(stderr, "  %-15s %q -> %q\n", l.Entity, l.Original, l.Fake)
			}
		}
	}
	_, _ = fmt.Fprintln(stderr, unreviewedWarning)
	if failed > 0 {
		if len(res.Files) == 1 {
			return errors.New(res.Files[0].Error)
		}
		return fmt.Errorf("%d of %d files failed", failed, len(res.Files))
	}
	return nil
}

// checkFile is one file's check, as --check-json writes it.
type checkFile struct {
	Input     string           `json:"input"`
	Output    string           `json:"output,omitempty"`
	Unchanged []anonymise.Kept `json:"unchanged"`
	Error     string           `json:"error,omitempty"`
}

// check lists, for each file anonymised, what its output holds exactly as
// its input did. A file that failed, or that cannot be read back, says why.
func check(ctx context.Context, checker *scrub.Checker, inputs []anonymiseInput, res *scrub.Result) []checkFile {
	out := make([]checkFile, len(res.Files))
	for i, f := range res.Files {
		c := checkFile{Input: inputs[i].name, Output: f.Name, Unchanged: []anonymise.Kept{}}
		if f.Error != "" {
			c.Error = f.Error
		} else if kept, err := checker.Unchanged(ctx, principal.Principal{}, anonymise.File{Name: inputs[i].name, Data: inputs[i].data}, anonymise.File{Name: f.Name, Data: f.Data}); err != nil {
			c.Error = "checking: " + err.Error()
		} else {
			c.Unchanged = kept
		}
		out[i] = c
	}
	return out
}

// checkJSONPath checks --check-json's path before anything is anonymised:
// - needs the files to go elsewhere than stdout, and a file must be new, in
// a directory that exists, and not the --output file.
func checkJSONPath(path, outDir, output string) error {
	switch {
	case path == "":
		return nil
	case path == "-":
		if outDir == "" && output == "" {
			return errors.New("--check-json - needs --output or --out-dir: stdout carries the file")
		}
		return nil
	case output != "" && filepath.Clean(path) == filepath.Clean(output):
		return errors.New("--check-json and --output name the same file")
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("--check-json %s exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || !fi.IsDir() {
		return fmt.Errorf("--check-json %s: its directory does not exist", path)
	}
	return nil
}

// writeCheckJSON writes the checks to path, a new file, or to stdout for -.
func writeCheckJSON(path string, checks []checkFile) error {
	data, err := json.MarshalIndent(struct {
		Files []checkFile `json:"files"`
	}{checks}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if path == "-" {
		_, err = stdout.Write(data)
		return err
	}
	return writeNew(path, data)
}

// printChecks lists on stderr what each file kept: every kind but words in
// full, words on one line.
func printChecks(checks []checkFile) {
	_, _ = fmt.Fprintln(stderr, "Unchanged: in the output exactly as in the original. Check each before sharing.")
	for i, c := range checks {
		switch {
		case c.Error != "":
			_, _ = fmt.Fprintf(stderr, "%d %s: not checked: %s\n", i+1, c.Input, c.Error)
			continue
		case len(c.Unchanged) == 0:
			_, _ = fmt.Fprintf(stderr, "%d %s -> %s: nothing\n", i+1, c.Input, c.Output)
			continue
		}
		_, _ = fmt.Fprintf(stderr, "%d %s -> %s:\n", i+1, c.Input, c.Output)
		var words []string
		for _, k := range c.Unchanged {
			v := k.Value
			if k.Count > 1 {
				v += fmt.Sprintf(" ×%d", k.Count)
			}
			if k.Kind == anonymise.KeptWord {
				words = append(words, v)
				continue
			}
			_, _ = fmt.Fprintf(stderr, "  %-7s %s\n", k.Kind, v)
		}
		if len(words) > 0 {
			_, _ = fmt.Fprintf(stderr, "  %-7s %s\n", "words", strings.Join(words, ", "))
		}
	}
}
