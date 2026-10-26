package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/exchange"
)

func TestExchangeCommandsExist(t *testing.T) {
	for name, cmd := range map[string]func() string{
		"export":  func() string { return exportCmd().Name },
		"import":  func() string { return importCmd().Name },
		"backup":  func() string { return backupCmd().Name },
		"restore": func() string { return restoreCmd().Name },
	} {
		if cmd() != name {
			t.Errorf("command %s is named %s", name, cmd())
		}
	}
}

func TestExportAndImportCommands(t *testing.T) {
	src := startServer(t)
	alice := src.member(t, "alice")
	var d dataset.Dataset
	src.runJSON(t, alice, &d, "datasets", "create", "--name", "travel")
	if _, err := src.run(t, alice, "generate", d.ID, "--kind", "emails", "--count", "2", "--seed", "4"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "travel.tar.gz")
	out, err := src.run(t, alice, "export", d.ID, "--output", path)
	if err != nil || !strings.Contains(out, "Exported 1 datasets, 2 fixtures") {
		t.Fatalf("export = %q, %v", out, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("archive = %v, %v", info, err)
	}
	if _, err := os.Stat(path + ".partial"); err == nil {
		t.Error("partial file left behind")
	}
	if _, err := src.run(t, alice, "export", d.ID, "--output", path); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("overwrite err = %v", err)
	}
	if _, err := src.run(t, alice, "export", "missing", "--output", path+"2"); err == nil {
		t.Error("export of a missing dataset succeeded")
	}
	if _, err := os.Stat(path + "2"); err == nil {
		t.Error("a failed export left a file")
	}

	dst := startServer(t)
	bob := dst.member(t, "bob")
	out, err = dst.run(t, bob, "import", path)
	if err != nil || !strings.Contains(out, "travel") || !strings.Contains(out, "People:") {
		t.Fatalf("import = %q, %v", out, err)
	}
	var res exchange.ImportResult
	dst.runJSON(t, bob, &res, "import", path)
	if len(res.Datasets) != 1 || res.Datasets[0].Created || res.Datasets[0].Existing != 2 {
		t.Errorf("re-import = %+v", res)
	}
	if _, err := dst.run(t, bob, "import", filepath.Join(t.TempDir(), "missing.tar.gz")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestBackupAndRestoreCommands(t *testing.T) {
	dir := t.TempDir()
	if _, err := runCLI(t, append([]string{"bootstrap"}, storageArgs(dir)...)...); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "backup.tar.gz")
	out, err := runCLI(t, append([]string{"backup", "--output", path}, storageArgs(dir)...)...)
	if err != nil || !strings.Contains(out, "Backed up 0 fixtures") || !strings.Contains(out, "encrypted storage") {
		t.Fatalf("backup = %q, %v", out, err)
	}

	fresh := t.TempDir()
	out, err = runCLI(t, append([]string{"restore", path}, storageArgs(fresh)...)...)
	if err != nil || !strings.Contains(out, "Restored 0 fixtures") {
		t.Fatalf("restore = %q, %v", out, err)
	}
	if _, err := runCLI(t, append([]string{"restore", path}, storageArgs(fresh)...)...); err == nil || !strings.Contains(err.Error(), "--overwrite") {
		t.Errorf("restore over an instance err = %v", err)
	}
	out, err = runCLI(t, append([]string{"restore", path, "--overwrite"}, storageArgs(fresh)...)...)
	if err != nil || !strings.Contains(out, ".pre-restore-") {
		t.Errorf("overwrite = %q, %v", out, err)
	}
	// The restored instance already has its admin.
	if _, err := runCLI(t, append([]string{"bootstrap"}, storageArgs(fresh)...)...); err == nil {
		t.Error("bootstrap ran on a restored instance")
	}
	if _, err := runCLI(t, append([]string{"restore", filepath.Join(dir, "missing")}, storageArgs(t.TempDir())...)...); err == nil {
		t.Error("missing backup accepted")
	}
}
