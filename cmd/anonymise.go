package cmd

import (
	"bytes"
	"context"
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
			return summarise(cmd, inputs, res)
		},
	}
}

// anonymiseInput is one file named on the command line, or stdin.
type anonymiseInput struct {
	name string // its base name, which picks the format
	path string // empty for stdin
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
func summarise(cmd *cli.Command, inputs []anonymiseInput, res *scrub.Result) error {
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
			if len(f.DroppedParts) > 0 {
				line += fmt.Sprintf(", %d parts dropped (%s)", len(f.DroppedParts), strings.Join(f.DroppedParts, ", "))
			}
			_, _ = fmt.Fprintln(stderr, line)
		}
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
