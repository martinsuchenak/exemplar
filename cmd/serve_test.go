package cmd

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/paularlott/logger"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/testutil"
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

func TestRunServersShutsDownGracefully(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	started := make(chan struct{})
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("done"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runServers(ctx, logger.NewNullLogger(), []namedServer{{"HTTP", newHTTPServer(addr, slow)}}, 5*time.Second)
	}()

	var resp *http.Response
	got := make(chan error, 1)
	go func() {
		for i := 0; i < 50; i++ {
			if resp, err = http.Get("http://" + addr); err == nil {
				got <- nil
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		got <- err
	}()
	<-started
	cancel() // a signal arrives while the request is in flight
	if err := <-got; err != nil {
		t.Fatalf("request in flight failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "done" {
		t.Errorf("body = %q", body)
	}
	if err := <-done; err != nil {
		t.Errorf("runServers = %v", err)
	}
}

func TestRunServersFailsWhenAPortIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()
	free, _ := net.Listen("tcp", "127.0.0.1:0")
	freeAddr := free.Addr().String()
	_ = free.Close()
	servers := []namedServer{
		{"HTTP", newHTTPServer(taken.Addr().String(), http.NewServeMux())},
		{"extra", newHTTPServer(freeAddr, http.NewServeMux())},
	}
	err = runServers(context.Background(), logger.NewNullLogger(), servers, time.Second)
	if err == nil || !strings.Contains(err.Error(), "HTTP server") {
		t.Errorf("err = %v", err)
	}
}

func TestOneListenerServesRESTAndMCP(t *testing.T) {
	a := testutil.NewApp(t)
	_, issued, err := a.Accounts.Bootstrap(context.Background(), account.CreateAccountInput{Name: "root"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newMux(a, true))
	defer srv.Close()

	// MCP initialize on /mcp of the REST listener, with the same key.
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+issued.Secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(out), "exemplar-mcp") {
		t.Errorf("/mcp initialize = %d %s", resp.StatusCode, out)
	}
	for _, path := range []string{"/mcp", "/api/v1/datasets"} {
		r, err := http.Post(srv.URL+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		if r.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without a key = %d", path, r.StatusCode)
		}
	}

	off := httptest.NewServer(newMux(a, false))
	defer off.Close()
	r, err := http.Post(off.URL+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Errorf("/mcp with MCP off = %d, want 404", r.StatusCode)
	}
}
