package cmd

import (
	"net/http"
	"testing"
)

func TestServeCmdExists(t *testing.T) {
	cmd := serveCmd()
	if cmd == nil {
		t.Fatal("serveCmd should not return nil")
	}
	if cmd.Name != "serve" {
		t.Errorf("expected command name 'serve', got %q", cmd.Name)
	}
}

func TestServeFailsWithoutStorage(t *testing.T) {
	if _, err := runCLI(t, "serve", "--db-path", "", "--blob-dir", ""); err == nil {
		t.Error("serve without storage paths succeeded")
	}
}

func TestNewHTTPServerSetsTimeouts(t *testing.T) {
	h := http.NewServeMux()
	srv := newHTTPServer("127.0.0.1:0", h)
	if srv.Addr != "127.0.0.1:0" || srv.Handler != h {
		t.Errorf("server = %+v", srv)
	}
	if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 || srv.WriteTimeout == 0 || srv.IdleTimeout == 0 {
		t.Errorf("a timeout is unset: %+v", srv)
	}
}
