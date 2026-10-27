package anonymise

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/synth"
)

// update rewrites testdata/emails-golden.json from the current engine:
// go test ./internal/anonymise -run TestEmailGolden -update. Only do it for
// a deliberate change in output, and say so in the commit.
var update = flag.Bool("update", false, "rewrite the golden email output")

const goldenFile = "testdata/emails-golden.json"

// goldenBatch is one batch through the engine: every email prepared, then
// each anonymised. Inputs are stored too, so the test does not depend on the
// generator staying the same.
type goldenBatch struct {
	Name   string        `json:"name"`
	Emails []goldenEmail `json:"emails"`
}

type goldenEmail struct {
	Name string `json:"name"`
	// Input and Output are bytes (base64 in the file): real email is not
	// always UTF-8.
	Input          []byte       `json:"input"`
	Output         []byte       `json:"output"`
	Replacements   int          `json:"replacements"`
	Attachments    int          `json:"attachments"`
	DroppedHeaders []string     `json:"dropped_headers"`
	People         []PersonRole `json:"people"`
}

// TestEmailGolden pins the engine's email output byte for byte, so moving
// code around (such as behind the format handlers) cannot change it.
func TestEmailGolden(t *testing.T) {
	if *update {
		writeGolden(t)
	}
	raw, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatal(err)
	}
	var batches []goldenBatch
	if err := json.Unmarshal(raw, &batches); err != nil {
		t.Fatal(err)
	}
	emails := 0
	for _, b := range batches {
		got := anonymiseGolden(t, b)
		for i, want := range b.Emails {
			g := got.Emails[i]
			if !bytes.Equal(g.Output, want.Output) {
				t.Errorf("%s/%s: output differs at byte %d", b.Name, want.Name, firstDiff(g.Output, want.Output))
				continue
			}
			if g.Replacements != want.Replacements || g.Attachments != want.Attachments ||
				!reflect.DeepEqual(g.DroppedHeaders, want.DroppedHeaders) || !reflect.DeepEqual(g.People, want.People) {
				t.Errorf("%s/%s: got %d replacements, %d attachments, dropped %v, people %v; want %d, %d, %v, %v",
					b.Name, want.Name, g.Replacements, g.Attachments, g.DroppedHeaders, g.People,
					want.Replacements, want.Attachments, want.DroppedHeaders, want.People)
			}
		}
		emails += len(b.Emails)
	}
	if len(batches) != 5 || emails < 50 {
		t.Errorf("golden file holds %d batches and %d emails, want 5 and at least 50", len(batches), emails)
	}
}

// anonymiseGolden runs one batch through a fresh engine and returns it with
// the outputs filled in.
func anonymiseGolden(t *testing.T, b goldenBatch) goldenBatch {
	t.Helper()
	e := newEngine(t, newMemRegistry())
	files := make([]File, len(b.Emails))
	for i, m := range b.Emails {
		files[i] = File{Name: m.Name + ".eml", Data: m.Input}
	}
	for i, err := range e.Prepare(ctx, files) {
		if err != nil {
			t.Fatalf("%s/%s: preparing: %v", b.Name, b.Emails[i].Name, err)
		}
	}
	out := goldenBatch{Name: b.Name}
	for i, m := range b.Emails {
		o, err := e.Anonymise(ctx, files[i])
		if err != nil {
			t.Fatalf("%s/%s: %v", b.Name, m.Name, err)
		}
		m.Output, m.Replacements, m.Attachments, m.DroppedHeaders, m.People = o.Data, len(o.Replacements), o.Attachments, o.DroppedHeaders, o.People
		out.Emails = append(out.Emails, m)
	}
	return out
}

// writeGolden builds the batches from the hand-made emails in these tests
// and from generated emails of every shape, and writes their output.
func writeGolden(t *testing.T) {
	t.Helper()
	hand := goldenBatch{Name: "hand-made"}
	for _, m := range []struct{ name, data string }{
		{"original", original}, {"reply", reply}, {"forwarded", forwarded}, {"bounce", bounce},
		{"newsletter", newsletter}, {"own-company", ownCompany}, {"dmarc-report", dmarcReport}, {"part-headers", partHeaderMail},
	} {
		hand.Emails = append(hand.Emails, goldenEmail{Name: m.name, Input: []byte(m.data)})
	}
	batches := []goldenBatch{hand}
	for _, profile := range synth.Profiles {
		for _, seed := range []uint64{2026, 7} {
			g := synth.New(seed)
			people := make([]synth.Person, 4)
			for i := range people {
				people[i] = g.Person(g.CompanyName())
			}
			b := goldenBatch{Name: fmt.Sprintf("%s-%d", profile, seed)}
			for _, shape := range synth.Shapes {
				emails, err := g.Emails(synth.EmailInput{Profile: profile, Shape: shape, People: people})
				if err != nil {
					t.Fatal(err)
				}
				for j, m := range emails {
					b.Emails = append(b.Emails, goldenEmail{Name: fmt.Sprintf("%s-%d", shape, j+1), Input: m.Data})
				}
			}
			batches = append(batches, b)
		}
	}
	for i, b := range batches {
		batches[i] = anonymiseGolden(t, b)
		// The same batch must give the same bytes again, or the file
		// would pin noise.
		again := anonymiseGolden(t, b)
		if !reflect.DeepEqual(batches[i], again) {
			t.Fatalf("%s: two runs differ", b.Name)
		}
	}
	data, err := json.MarshalIndent(batches, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goldenFile, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// firstDiff returns the offset of the first byte where a and b differ.
func firstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}
