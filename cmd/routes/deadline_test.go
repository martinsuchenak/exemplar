package routes

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

// slowBody sends n chunks with a pause before each.
type slowBody struct {
	n     int
	pause time.Duration
}

func (b *slowBody) Read(p []byte) (int, error) {
	if b.n == 0 {
		return 0, io.EOF
	}
	time.Sleep(b.pause)
	b.n--
	return copy(p, "chunk "), nil
}

func TestLargeUploadsOutliveTheServerReadTimeout(t *testing.T) {
	a := testutil.NewApp(t)
	_, issued, err := a.Accounts.Bootstrap(context.Background(), account.CreateAccountInput{Name: "root"})
	if err != nil {
		t.Fatal(err)
	}
	admin := testutil.Authenticate(t, a, issued.Secret)
	d, err := a.Datasets.Create(context.Background(), admin, dataset.CreateInput{Name: "slow"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, a)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Ordinary requests get 200ms to arrive; uploads extend their own deadline.
	srv := &http.Server{Handler: mux, ReadTimeout: 200 * time.Millisecond, WriteTimeout: 2 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	base := "http://" + ln.Addr().String()

	post := func(path string) (int, string) {
		req, _ := http.NewRequest("POST", base+path, &slowBody{n: 8, pause: 80 * time.Millisecond})
		req.Header.Set("X-API-Key", issued.Secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := post("/api/v1/datasets/" + d.ID + "/fixtures?name=slow.txt"); code != http.StatusCreated {
		t.Errorf("slow fixture upload = %d %s", code, body)
	}
	// A JSON endpoint keeps the short timeout.
	if code, body := post("/api/v1/datasets"); code == http.StatusCreated || strings.Contains(body, "slow") {
		t.Errorf("slow JSON request = %d %s; want it cut off", code, body)
	}
}
