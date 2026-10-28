package cmd

import (
	"os"
	"path/filepath"
	"runtime"
)

// ConfigSearchPath returns the directories a bare config file name is looked
// for in, first match wins: the current directory, then the user's config
// directory (UserConfigDir), so the CLI on a laptop can keep client.server
// there instead of exporting EXEMPLAR_SERVER in every shell. A name with a
// directory in it (--config ./x.toml, /etc/exemplar/x.toml) is only read
// where it points.
func ConfigSearchPath(configFile *string) func() []string {
	return func() []string {
		if filepath.Base(*configFile) != *configFile {
			return []string{}
		}
		paths := []string{"."}
		if dir := UserConfigDir(); dir != "" {
			paths = append(paths, dir)
		}
		return paths
	}
}

// UserConfigDir is exemplar's directory in the user's configuration:
// $XDG_CONFIG_HOME/exemplar, or ~/.config/exemplar, on Linux and macOS, and
// %AppData%\exemplar on Windows. Empty when there is no home directory.
func UserConfigDir() string {
	if runtime.GOOS == "windows" {
		if dir, err := os.UserConfigDir(); err == nil {
			return filepath.Join(dir, "exemplar")
		}
		return ""
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "exemplar")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "exemplar")
	}
	return ""
}
