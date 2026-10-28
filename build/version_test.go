package build

import "testing"

func TestVersionDefined(t *testing.T) {
	if Version == "" {
		t.Error("Version should not be empty")
	}
}

func TestCommitDefined(t *testing.T) {
	if Commit == "" {
		t.Error("Commit should not be empty")
	}
}

func TestDateDefined(t *testing.T) {
	if Date == "" {
		t.Error("Date should not be empty")
	}
}

func TestString(t *testing.T) {
	defer func(v, c, d string) { Version, Commit, Date = v, c, d }(Version, Commit, Date)
	Version, Commit, Date = "v1.2.0", "abc1234", "2026-10-28T10:00:00Z"
	if got := String(); got != "v1.2.0 (abc1234, 2026-10-28T10:00:00Z)" {
		t.Errorf("String() = %q", got)
	}
}
