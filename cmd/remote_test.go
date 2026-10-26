package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/cmd/routes"
	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

// liveServer runs the real routes on a test app for CLI tests.
type liveServer struct {
	url      string
	adminKey string
	app      *app.App
}

func startServer(t *testing.T) *liveServer {
	t.Helper()
	return startServerWith(t, nil)
}

// startServerWith is startServer with a chance to change the app config.
func startServerWith(t *testing.T, configure func(*app.Config)) *liveServer {
	t.Helper()
	a := testutil.NewAppWith(t, configure)
	_, issued, err := a.Accounts.Bootstrap(context.Background(), account.CreateAccountInput{Name: "root"})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	mux := http.NewServeMux()
	routes.RegisterRoutes(mux, a)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &liveServer{url: srv.URL, adminKey: issued.Secret, app: a}
}

// run executes a remote command against the server with key.
func (s *liveServer) run(t *testing.T, key string, args ...string) (string, error) {
	t.Helper()
	return runCLI(t, append(args, "--server", s.url, "--api-key", key)...)
}

// runJSON executes a remote command with --json and decodes its output.
func (s *liveServer) runJSON(t *testing.T, key string, v interface{}, args ...string) {
	t.Helper()
	out, err := s.run(t, key, append(args, "--json")...)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("%v: decoding %q: %v", args, out, err)
	}
}

// member creates a member account and returns its key secret.
func (s *liveServer) member(t *testing.T, name string) string {
	t.Helper()
	var acc account.Account
	s.runJSON(t, s.adminKey, &acc, "accounts", "create", "--name", name)
	var issued account.IssuedKey
	s.runJSON(t, s.adminKey, &issued, "keys", "create", "--name", "cli", "--account", acc.ID)
	return issued.Secret
}

func TestRemoteCommandsNeedAKey(t *testing.T) {
	s := startServer(t)
	if _, err := runCLI(t, "datasets", "list", "--server", s.url, "--api-key", ""); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("no key err = %v", err)
	}
	if _, err := runCLI(t, "datasets", "list", "--server", "not a url", "--api-key", "exm_x"); err == nil {
		t.Error("bad server URL accepted")
	}
	if _, err := s.run(t, "exm_wrong", "datasets", "list"); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("wrong key err = %v", err)
	}
}

func TestSplitListAndHelpers(t *testing.T) {
	if got := splitList(" a, ,b ,"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("splitList = %v", got)
	}
	if splitList("") != nil {
		t.Error("splitList of empty should be nil")
	}
	if formatTime(nil) != "-" || orDash("") != "-" || orDash("x") != "x" {
		t.Error("formatTime/orDash placeholders")
	}
}
