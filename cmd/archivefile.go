package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// writeArchive writes an archive to path through path.partial, renaming it
// only once complete, so an interrupted run never leaves a file that looks
// whole. It refuses to overwrite path. The file is readable by its owner only.
func writeArchive(path string, write func(io.Writer) error) error {
	if path == "" {
		return errors.New("--output is required")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	partial := path + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = write(f)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(partial, path)
	}
	if err != nil {
		_ = os.Remove(partial)
	}
	return err
}
