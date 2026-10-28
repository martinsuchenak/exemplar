package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/client"
	"github.com/martinsuchenak/exemplar/internal/fixture"
)

func init() {
	Register(reviewWalkCmd())
}

// openFile opens a downloaded fixture in the system's default app for its
// type. Tests replace it.
var openFile = func(path string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", path)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		c = exec.Command("xdg-open", path)
	}
	return c.Start()
}

// reviewWalkCmd walks a quarantine dataset's unreviewed fixtures one by one:
// each is downloaded to a private temporary directory, opened, and approved,
// rejected or skipped on the reviewer's word. Every decision is one fixture's,
// recorded by the server as approve and reject record it; local copies are
// deleted as the walk goes and when it ends. Admin key.
func reviewWalkCmd() *cli.Command {
	return &cli.Command{
		Name:      "review",
		Usage:     "Review a quarantine dataset's unreviewed fixtures one by one (admin key)",
		Arguments: []cli.Argument{&cli.StringArg{Name: "dataset", Usage: "Quarantine dataset (id or name)", Required: true}},
		Flags: withClientFlags(
			&cli.StringFlag{Name: "kind", Usage: "Only fixtures of this kind: email, file or record"},
			&cli.StringFlag{Name: "query", Usage: "Only fixtures with these words in their name"},
			&cli.BoolFlag{Name: "no-open", Usage: "Print each file's path instead of opening it"},
		),
		Run: func(ctx context.Context, cmd *cli.Command) error {
			id, err := requireArg(cmd, "dataset")
			if err != nil {
				return err
			}
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			if id, err = resolveDataset(ctx, c, id); err != nil {
				return err
			}
			todo, err := listAll(ctx, c, id, fixture.ListFilter{
				ReviewState: fixture.ReviewUnreviewed,
				Kind:        fixture.Kind(cmd.GetString("kind")),
				Query:       cmd.GetString("query"),
			})
			if err != nil {
				return err
			}
			if len(todo) == 0 {
				_, _ = fmt.Fprintln(stdout, "Nothing to review.")
				return nil
			}
			dir, err := os.MkdirTemp("", "exemplar-review-")
			if err != nil {
				return err
			}
			defer func() { _ = os.RemoveAll(dir) }()
			w := &reviewWalk{c: c, dir: dir, open: !cmd.GetBool("no-open"), in: bufio.NewReader(stdin)}
			err = w.run(ctx, todo)
			_, _ = fmt.Fprintf(stdout, "Approved %d, rejected %d, skipped %d, left %d.\n", w.approved, w.rejected, w.skipped, len(todo)-w.approved-w.rejected-w.skipped)
			return err
		},
	}
}

// reviewWalk is one review session and its tally.
type reviewWalk struct {
	c    *client.Client
	dir  string
	open bool
	in   *bufio.Reader

	approved, rejected, skipped int
}

// errQuit ends a walk on the reviewer's word; it is not a failure.
var errQuit = errors.New("quit")

func (w *reviewWalk) run(ctx context.Context, todo []fixture.Fixture) error {
	for i, f := range todo {
		_, _ = fmt.Fprintf(stdout, "\n[%d/%d] %s  (%s, %s, %d bytes, %s)\n", i+1, len(todo), f.Name, f.Kind, f.Format, f.Size, f.ID)
		err := w.one(ctx, f)
		if errors.Is(err, errQuit) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// one downloads, shows and decides one fixture, and removes the local copy.
func (w *reviewWalk) one(ctx context.Context, f fixture.Fixture) error {
	path, err := w.download(ctx, f)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(path) }()
	w.show(path)
	for {
		_, _ = fmt.Fprint(stdout, "[a]pprove, [r]eject, [s]kip, [o]pen again, [q]uit: ")
		line, err := w.in.ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || line == "") {
			// No more input: stop as if the reviewer quit.
			_, _ = fmt.Fprintln(stdout)
			return errQuit
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "a", "approve":
			return w.decide(ctx, f, true)
		case "r", "reject":
			return w.decide(ctx, f, false)
		case "s", "skip":
			w.skipped++
			return nil
		case "o", "open":
			w.show(path)
		case "q", "quit":
			return errQuit
		}
	}
}

func (w *reviewWalk) download(ctx context.Context, f fixture.Fixture) (string, error) {
	path := filepath.Join(w.dir, localName(f))
	return path, downloadTo(ctx, w.c, f.ID, path)
}

// show opens the file, or prints where it is with --no-open or when opening
// fails.
func (w *reviewWalk) show(path string) {
	if w.open {
		if err := openFile(path); err == nil {
			return
		}
		_, _ = fmt.Fprintln(stderr, "could not open it; open it yourself:")
	}
	_, _ = fmt.Fprintln(stdout, path)
}

func (w *reviewWalk) decide(ctx context.Context, f fixture.Fixture, approve bool) error {
	if _, err := w.c.Review(ctx, f.ID, approve); err != nil {
		return fmt.Errorf("%s: %w", f.Name, err)
	}
	if approve {
		w.approved++
		_, _ = fmt.Fprintln(stdout, "approved")
	} else {
		w.rejected++
		_, _ = fmt.Fprintln(stdout, "rejected")
	}
	return nil
}

// listAll pages through every fixture of a dataset matching f.
func listAll(ctx context.Context, c *client.Client, datasetID string, f fixture.ListFilter) ([]fixture.Fixture, error) {
	const page = 1000
	var out []fixture.Fixture
	for {
		f.Limit, f.Offset = page, len(out)
		list, err := c.ListFixtures(ctx, datasetID, f)
		if err != nil {
			return nil, err
		}
		out = append(out, list...)
		if len(list) < page {
			return out, nil
		}
	}
}
