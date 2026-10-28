package cmd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/paularlott/cli"
	cli_toml "github.com/paularlott/cli/toml"
)

func TestConfigSearchPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG_CONFIG_HOME is not used on Windows")
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	user := filepath.Join(xdg, "exemplar")

	name := "exemplar-config.toml"
	search := ConfigSearchPath(&name)
	if got := search(); !reflect.DeepEqual(got, []string{".", user}) {
		t.Errorf("bare name searches %v, want . then %s", got, user)
	}
	// A name with a directory is read only where it points.
	for _, name = range []string{"./exemplar-config.toml", "/etc/exemplar/exemplar-config.toml", "conf/x.toml"} {
		if got := search(); len(got) != 0 {
			t.Errorf("%s searches %v, want nothing", name, got)
		}
	}
}

func TestUserConfigDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG_CONFIG_HOME is not used on Windows")
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got := UserConfigDir(); got != filepath.Join(xdg, "exemplar") {
		t.Errorf("with XDG_CONFIG_HOME = %q", got)
	}
	// A relative XDG_CONFIG_HOME is ignored, as the spec says.
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, v := range []string{"", "relative/dir"} {
		t.Setenv("XDG_CONFIG_HOME", v)
		if got := UserConfigDir(); got != filepath.Join(home, ".config", "exemplar") {
			t.Errorf("XDG_CONFIG_HOME %q: %q", v, got)
		}
	}
	t.Setenv("HOME", "")
	if got := UserConfigDir(); got != "" {
		t.Errorf("without a home = %q", got)
	}
}

// With the search path, the config loader reads client.server from the
// user's config directory when the current directory has no config file,
// and prefers the current directory's when it has one.
func TestConfigFileFallsBackToTheUserDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG_CONFIG_HOME is not used on Windows")
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "exemplar"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, server string) {
		if err := os.WriteFile(path, []byte("[client]\nserver = \""+server+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(xdg, "exemplar", "exemplar-config.toml"), "https://user.example")
	cwd := t.TempDir()
	t.Chdir(cwd)

	server := func() string {
		name := "exemplar-config.toml"
		var got string
		root := &cli.Command{
			Name:       "exemplar",
			ConfigFile: cli_toml.NewConfigFile(&name, ConfigSearchPath(&name)),
			Flags:      []cli.Flag{&cli.StringFlag{Name: "server", ConfigPath: []string{"client.server"}}},
			Run: func(_ context.Context, cmd *cli.Command) error {
				got = cmd.GetString("server")
				return nil
			},
		}
		old := os.Args
		os.Args = []string{"exemplar"}
		defer func() { os.Args = old }()
		if err := root.Execute(context.Background()); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := server(); got != "https://user.example" {
		t.Errorf("server from the user directory = %q", got)
	}
	write(filepath.Join(cwd, "exemplar-config.toml"), "https://local.example")
	if got := server(); got != "https://local.example" {
		t.Errorf("server with a local config = %q", got)
	}
}
