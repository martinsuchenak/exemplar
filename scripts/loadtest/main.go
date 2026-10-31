// Command loadtest measures an exemplar server under concurrent clients:
// REST reads, MCP tool calls, record generation and anonymise runs. For
// each concurrency level it runs that many workers for a while and prints
// throughput, latency percentiles and, given the server's PID, its peak
// memory and CPU. Keys come from a file, one per line, never the command
// line; workers take them in turn, so anonymise (one run per key at a time)
// needs a key per worker.
//
//	go run ./scripts/loadtest -server http://127.0.0.1:18080 -keys keys.txt \
//	  -mode rest -dataset bench -c 1,4,16,64 -d 15s -pid 1234
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mcplib "github.com/paularlott/mcp"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/client"
	"github.com/martinsuchenak/exemplar/internal/scrub"
)

func main() {
	server := flag.String("server", "http://127.0.0.1:18080", "Server URL")
	keysFile := flag.String("keys", "", "File of API keys, one per line")
	mode := flag.String("mode", "rest", "rest, mcp, generate or anonymise")
	levels := flag.String("c", "1,4,16,64", "Concurrency levels, comma-separated")
	dur := flag.Duration("d", 15*time.Second, "How long each level runs")
	dataset := flag.String("dataset", "", "Dataset id (rest, mcp, generate)")
	files := flag.String("files", "", "Directory of files to anonymise (anonymise)")
	pid := flag.Int("pid", 0, "Server PID, to sample its memory and CPU")
	byOp := flag.Bool("by-op", false, "Also print latency per operation")
	flag.Parse()

	keys, err := readKeys(*keysFile)
	if err != nil {
		fail(err)
	}
	t := &target{server: strings.TrimRight(*server, "/"), keys: keys, dataset: *dataset}
	var op func(*target) (func(context.Context, int) (string, error), error)
	switch *mode {
	case "rest":
		op = restOp
	case "mcp":
		op = mcpOp
	case "generate":
		op = generateOp
	case "anonymise":
		t.files, err = readFiles(*files)
		if err != nil {
			fail(err)
		}
		op = anonymiseOp
	default:
		fail(fmt.Errorf("unknown mode %q", *mode))
	}
	fmt.Printf("%-10s %5s %8s %8s %6s %9s %8s %8s %8s %8s %9s %6s\n", "MODE", "C", "OPS", "ERRORS", "BUSY", "OPS/S", "P50", "P95", "P99", "MAX", "PEAK RSS", "CPU%")
	for _, s := range strings.Split(*levels, ",") {
		c, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || c < 1 {
			fail(fmt.Errorf("bad level %q", s))
		}
		run, err := op(t)
		if err != nil {
			fail(err)
		}
		r := measure(run, c, *dur, *pid)
		fmt.Printf("%-10s %5d %8d %8d %6d %9.1f %8s %8s %8s %8s %8.0fM %6.0f\n", *mode, c, r.ops, r.errs, r.busy, r.rate,
			ms(r.p(50)), ms(r.p(95)), ms(r.p(99)), ms(r.max()), float64(r.peakRSS)/1024, r.cpu)
		if *byOp {
			labels := make([]string, 0, len(r.byLabel))
			for l := range r.byLabel {
				labels = append(labels, l)
			}
			sort.Strings(labels)
			for _, l := range labels {
				ds := r.byLabel[l]
				sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
				fmt.Printf("  %-18s %8d ops  p50 %8s  p95 %8s  p99 %8s\n", l, len(ds), ms(ds[len(ds)/2]), ms(ds[min(len(ds)-1, len(ds)*95/100)]), ms(ds[min(len(ds)-1, len(ds)*99/100)]))
			}
		}
		for msg, n := range r.samples {
			fmt.Printf("           error x%d: %s\n", n, msg)
		}
	}
}

// target is what workers share.
type target struct {
	server, dataset string
	keys            []string
	files           []benchFile
	fixtureIDs      []string
}

type benchFile struct {
	name string
	data []byte
}

func (t *target) key(worker int) string { return t.keys[worker%len(t.keys)] }

// errBusy marks a refusal for load (429, or the server's busy error), which
// is counted apart from failures.
var errBusy = errors.New("busy")

// restOp reads like a client browsing fixtures: list a dataset's fixtures,
// read one's metadata or content, list datasets.
func restOp(t *target) (func(context.Context, int) (string, error), error) {
	hc := &http.Client{Transport: &http.Transport{MaxIdleConns: 4096, MaxIdleConnsPerHost: 4096}, Timeout: time.Minute}
	if err := t.loadFixtureIDs(hc); err != nil {
		return nil, err
	}
	return func(ctx context.Context, w int) (string, error) {
		var path, label string
		switch n := rand.IntN(100); {
		case n < 30:
			path, label = "/api/v1/datasets/"+t.dataset+"/fixtures?limit=50", "list fixtures"
		case n < 55:
			path, label = "/api/v1/fixtures/"+t.fixtureIDs[rand.IntN(len(t.fixtureIDs))], "get fixture"
		case n < 85:
			path, label = "/api/v1/fixtures/"+t.fixtureIDs[rand.IntN(len(t.fixtureIDs))]+"/content", "content"
		default:
			path, label = "/api/v1/datasets", "list datasets"
		}
		return label, get(ctx, hc, t.server+path, t.key(w))
	}, nil
}

// mcpOp calls the MCP tools an agent uses: search, get one (metadata, or
// with content), list datasets. Each worker keeps its own session.
func mcpOp(t *target) (func(context.Context, int) (string, error), error) {
	hc := &http.Client{Transport: &http.Transport{MaxIdleConns: 4096, MaxIdleConnsPerHost: 4096}, Timeout: time.Minute}
	if err := t.loadFixtureIDs(hc); err != nil {
		return nil, err
	}
	var mu sync.Mutex
	clients := map[int]*mcplib.Client{}
	return func(ctx context.Context, w int) (string, error) {
		mu.Lock()
		c := clients[w]
		if c == nil {
			c = mcplib.NewClient(t.server+"/mcp", mcplib.NewBearerTokenAuth(t.key(w)), "")
			clients[w] = c
		}
		mu.Unlock()
		var err error
		var label string
		switch n := rand.IntN(100); {
		case n < 35:
			label = "search_fixtures"
			_, err = c.CallTool(ctx, label, map[string]any{"dataset": t.dataset})
		case n < 60:
			label = "get_fixture none"
			_, err = c.CallTool(ctx, "get_fixture", map[string]any{"id": t.fixtureIDs[rand.IntN(len(t.fixtureIDs))], "content": "none"})
		case n < 85:
			label = "get_fixture auto"
			_, err = c.CallTool(ctx, "get_fixture", map[string]any{"id": t.fixtureIDs[rand.IntN(len(t.fixtureIDs))]})
		default:
			label = "list_datasets"
			_, err = c.CallTool(ctx, label, map[string]any{})
		}
		return label, err
	}, nil
}

// generateOp asks for 5 contact records at a time, each worker into a
// dataset of its own: writes, so it shows how SQLite's one writer copes.
func generateOp(t *target) (func(context.Context, int) (string, error), error) {
	hc := &http.Client{Transport: &http.Transport{MaxIdleConns: 4096, MaxIdleConnsPerHost: 4096}, Timeout: 5 * time.Minute}
	body := []byte(`{"kind":"records","count":5,"schemas":["contact"],"people":5}`)
	var mu sync.Mutex
	datasets := map[int]string{}
	return func(ctx context.Context, w int) (string, error) {
		mu.Lock()
		ds := datasets[w]
		mu.Unlock()
		if ds == "" {
			name := fmt.Sprintf("gen-%d-%d", w, rand.Uint32())
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.server+"/api/v1/datasets", strings.NewReader(`{"name":"`+name+`"}`))
			if err != nil {
				return "generate", err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+t.key(w))
			resp, err := hc.Do(req)
			if err != nil {
				return "generate", err
			}
			var d struct {
				ID string `json:"id"`
			}
			err = json.NewDecoder(resp.Body).Decode(&d)
			_ = resp.Body.Close()
			if err != nil || d.ID == "" {
				return "generate", fmt.Errorf("creating a dataset: %d", resp.StatusCode)
			}
			ds = d.ID
			mu.Lock()
			datasets[w] = ds
			mu.Unlock()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.server+"/api/v1/datasets/"+ds+"/generate", bytes.NewReader(body))
		if err != nil {
			return "generate", err
		}
		req.Header.Set("Content-Type", "application/json")
		return "generate", do(hc, req, t.key(w))
	}, nil
}

// anonymiseOp sends one file per call, in turn, through the client the CLI
// uses, discarding the output.
func anonymiseOp(t *target) (func(context.Context, int) (string, error), error) {
	var mu sync.Mutex
	clients := map[int]*client.Client{}
	var next atomic.Int64
	return func(ctx context.Context, w int) (string, error) {
		mu.Lock()
		c := clients[w]
		if c == nil {
			var err error
			if c, err = client.New(t.server, t.key(w)); err != nil {
				mu.Unlock()
				return "", err
			}
			clients[w] = c
		}
		mu.Unlock()
		f := t.files[int(next.Add(1))%len(t.files)]
		label := strings.TrimPrefix(filepath.Ext(f.name), ".")
		_, err := c.AnonymiseFile(ctx, scrub.Request{}, f.name, bytes.NewReader(f.data), io.Discard)
		if errors.Is(err, apperr.ErrBusy) {
			return label, errBusy
		}
		return label, err
	}, nil
}

func (t *target) loadFixtureIDs(hc *http.Client) error {
	if t.fixtureIDs != nil {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, t.server+"/api/v1/datasets/"+t.dataset+"/fixtures?limit=1000", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.keys[0])
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var page struct {
		Fixtures []struct {
			ID string `json:"id"`
		} `json:"fixtures"`
	}
	var list []struct {
		ID string `json:"id"`
	}
	data, _ := io.ReadAll(resp.Body)
	if json.Unmarshal(data, &page) == nil && len(page.Fixtures) > 0 {
		list = page.Fixtures
	} else if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("listing fixtures: %s", data)
	}
	for _, f := range list {
		t.fixtureIDs = append(t.fixtureIDs, f.ID)
	}
	if len(t.fixtureIDs) == 0 {
		return fmt.Errorf("dataset %s has no fixtures", t.dataset)
	}
	return nil
}

func get(ctx context.Context, hc *http.Client, url, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	return do(hc, req, key)
}

func do(hc *http.Client, req *http.Request, key string) error {
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable:
		return errBusy
	case resp.StatusCode >= 300:
		return fmt.Errorf("%d %s", resp.StatusCode, firstLine(body))
	}
	return nil
}

// result is one level's measurements.
type result struct {
	ops, errs, busy int
	rate            float64
	lat             []time.Duration
	byLabel         map[string][]time.Duration
	samples         map[string]int
	peakRSS         int64 // KiB
	cpu             float64
}

func (r *result) p(q int) time.Duration {
	if len(r.lat) == 0 {
		return 0
	}
	return r.lat[min(len(r.lat)-1, len(r.lat)*q/100)]
}

func (r *result) max() time.Duration {
	if len(r.lat) == 0 {
		return 0
	}
	return r.lat[len(r.lat)-1]
}

// measure runs c workers calling run until d has passed, timing every
// call that succeeds, and samples the server process meanwhile.
func measure(run func(context.Context, int) (string, error), c int, d time.Duration, pid int) *result {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	stopSampling := sample(pid)
	start := time.Now()
	var mu sync.Mutex
	r := &result{samples: map[string]int{}, byLabel: map[string][]time.Duration{}}
	var wg sync.WaitGroup
	for w := range c {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var lat []time.Duration
			byLabel := map[string][]time.Duration{}
			ops, errs, busy := 0, 0, 0
			msgs := map[string]int{}
			for ctx.Err() == nil {
				t0 := time.Now()
				label, err := run(ctx, w)
				if ctx.Err() != nil {
					break // cut off by the deadline: not counted
				}
				switch {
				case errors.Is(err, errBusy):
					busy++
				case err != nil:
					errs++
					msgs[firstLine([]byte(err.Error()))]++
				default:
					ops++
					d := time.Since(t0)
					lat = append(lat, d)
					byLabel[label] = append(byLabel[label], d)
				}
			}
			mu.Lock()
			r.ops, r.errs, r.busy = r.ops+ops, r.errs+errs, r.busy+busy
			r.lat = append(r.lat, lat...)
			for l, ds := range byLabel {
				r.byLabel[l] = append(r.byLabel[l], ds...)
			}
			for m, n := range msgs {
				if len(r.samples) < 3 || r.samples[m] > 0 {
					r.samples[m] += n
				}
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	r.rate = float64(r.ops) / time.Since(start).Seconds()
	sort.Slice(r.lat, func(i, j int) bool { return r.lat[i] < r.lat[j] })
	r.peakRSS, r.cpu = stopSampling()
	return r
}

// sample polls ps for the server's resident memory and CPU every half
// second; the stop function returns the peak RSS (KiB) and mean CPU%.
func sample(pid int) func() (int64, float64) {
	if pid == 0 {
		return func() (int64, float64) { return 0, 0 }
	}
	done := make(chan struct{})
	var peak int64
	var cpuSum float64
	var n int
	var mu sync.Mutex
	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				out, err := exec.Command("ps", "-o", "rss=,%cpu=", "-p", strconv.Itoa(pid)).Output()
				if err != nil {
					continue
				}
				f := strings.Fields(string(out))
				if len(f) != 2 {
					continue
				}
				rss, _ := strconv.ParseInt(f[0], 10, 64)
				cpu, _ := strconv.ParseFloat(f[1], 64)
				mu.Lock()
				peak = max(peak, rss)
				cpuSum += cpu
				n++
				mu.Unlock()
			}
		}
	}()
	return func() (int64, float64) {
		close(done)
		mu.Lock()
		defer mu.Unlock()
		if n == 0 {
			return peak, 0
		}
		return peak, cpuSum / float64(n)
	}
}

func readKeys(path string) ([]string, error) {
	if path == "" {
		return nil, errors.New("-keys is required: a file of API keys, one per line")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			keys = append(keys, l)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%s holds no keys", path)
	}
	return keys, nil
}

func readFiles(dir string) ([]benchFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []benchFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, benchFile{name: e.Name(), data: data})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s holds no files", dir)
	}
	return out, nil
}

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(string(b), "\n")
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "loadtest:", err)
	os.Exit(1)
}
