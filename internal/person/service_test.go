package person_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/person"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/synth"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

var ctx = context.Background()

func TestGenerateAndRead(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")

	people, seed, err := a.Persons.Generate(ctx, alice, person.GenerateInput{Count: 9, Seed: 77})
	if err != nil || len(people) != 9 || seed != 77 {
		t.Fatalf("generate = %d people, seed %d, %v", len(people), seed, err)
	}
	companies := map[string]int{}
	for _, p := range people {
		companies[p.Company]++
		if p.ID == "" || p.CreatedBy != alice.AccountID || !strings.HasSuffix(p.Email, ".example") {
			t.Errorf("person = %+v", p)
		}
	}
	if len(companies) >= 9 {
		t.Errorf("people are not grouped into companies: %v", companies)
	}

	got, err := a.Persons.Get(ctx, admin, people[0].ID)
	if err != nil || got.Email != people[0].Email || got.Address.Postcode == "" {
		t.Errorf("get = %+v, %v", got, err)
	}
	if _, err := a.Persons.Get(ctx, admin, "missing"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("get missing err = %v, want ErrNotFound", err)
	}

	all, err := a.Persons.List(ctx, alice, person.ListFilter{})
	if err != nil || len(all) != 9 {
		t.Fatalf("list = %d, %v", len(all), err)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].LastName > all[i].LastName {
			t.Errorf("not sorted by last name: %s before %s", all[i-1].LastName, all[i].LastName)
		}
	}
	page, _ := a.Persons.List(ctx, alice, person.ListFilter{Limit: 4, Offset: 4})
	if len(page) != 4 || page[0].ID != all[4].ID {
		t.Errorf("page = %+v", page)
	}
	if capped, _ := a.Persons.List(ctx, alice, person.ListFilter{Limit: 1_000_000, Offset: -1}); len(capped) != 9 {
		t.Errorf("capped list = %d", len(capped))
	}
	byCompany, _ := a.Persons.List(ctx, alice, person.ListFilter{Company: people[0].Company})
	if len(byCompany) != companies[people[0].Company] {
		t.Errorf("company filter = %d, want %d", len(byCompany), companies[people[0].Company])
	}

	events, _ := a.Audit.List(ctx, admin, audit.Filter{Action: audit.ActionPersonGenerate})
	if len(events) != 1 || events[0].Details["count"] != float64(9) {
		t.Errorf("audit = %+v", events)
	}
}

func TestGenerateRules(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	ro := alice
	ro.Scopes = []principal.Scope{principal.ScopeRead}

	if _, _, err := a.Persons.Generate(ctx, ro, person.GenerateInput{Count: 1}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("read-only generate err = %v, want ErrForbidden", err)
	}
	for _, n := range []int{0, person.MaxGenerate + 1} {
		if _, _, err := a.Persons.Generate(ctx, alice, person.GenerateInput{Count: n}); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("count %d err = %v, want ErrInvalid", n, err)
		}
	}
	none := alice
	none.Scopes = nil
	if _, err := a.Persons.List(ctx, none, person.ListFilter{}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("no-scope list err = %v, want ErrForbidden", err)
	}
	if _, err := a.Persons.Get(ctx, none, "x"); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("no-scope get err = %v, want ErrForbidden", err)
	}
	if _, seed, err := a.Persons.Generate(ctx, alice, person.GenerateInput{Count: 1}); err != nil || seed == 0 {
		t.Errorf("random seed = %d, %v", seed, err)
	}
}

func TestSameSeedTwiceGetsDistinctEmails(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	first, _, err := a.Persons.Generate(ctx, admin, person.GenerateInput{Count: 3, Seed: 5})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := a.Persons.Generate(ctx, admin, person.GenerateInput{Count: 3, Seed: 5})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	for i := range first {
		if first[i].Email == second[i].Email || !strings.Contains(second[i].Email, "2@") {
			t.Errorf("clash not numbered: %s vs %s", first[i].Email, second[i].Email)
		}
	}
}

func TestPickReusesThenCreates(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	existing, _, err := a.Persons.Generate(ctx, admin, person.GenerateInput{Count: 3, Seed: 9})
	if err != nil {
		t.Fatal(err)
	}
	picked, err := a.Persons.Pick(ctx, admin, synth.New(1), 5)
	if err != nil || len(picked) != 5 {
		t.Fatalf("pick = %d, %v", len(picked), err)
	}
	known := map[string]bool{}
	for _, p := range existing {
		known[p.ID] = true
	}
	reused := 0
	seen := map[string]bool{}
	for _, p := range picked {
		if known[p.ID] {
			reused++
		}
		if seen[p.ID] {
			t.Errorf("person %s picked twice", p.ID)
		}
		seen[p.ID] = true
	}
	if reused != 3 {
		t.Errorf("reused %d existing people, want all 3", reused)
	}
	if all, _ := a.Persons.List(ctx, admin, person.ListFilter{}); len(all) != 5 {
		t.Errorf("registry has %d people, want 5", len(all))
	}
	if _, err := a.Persons.Pick(ctx, admin, synth.New(1), 0); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("pick 0 err = %v, want ErrInvalid", err)
	}
}

func TestIdentities(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	if _, err := a.Persons.Identities(alice, "k-1", true); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member err = %v", err)
	}
	if _, err := a.Persons.Identities(admin, "", true); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("no key id err = %v", err)
	}
	draw := synth.New(5).Person("Acme Pty Ltd")

	// A dry run reads nothing back and writes nothing.
	dry, _ := a.Persons.Identities(admin, "k-1", false)
	if v, _ := dry.Token(ctx, "h-name", "Kent"); v != "Kent" {
		t.Errorf("dry token = %s", v)
	}
	if v, _ := dry.Token(ctx, "h-name", "Other"); v != "Kent" {
		t.Errorf("dry token not remembered in memory: %s", v)
	}
	p, err := dry.Person(ctx, []string{"h-email"}, draw)
	if err != nil || p.ID != "" || p.Email != draw.Email {
		t.Errorf("dry person = %+v, %v", p, err)
	}
	if again, _ := dry.Person(ctx, []string{"h-other", "h-email"}, synth.New(6).Person("X")); again.Email != draw.Email {
		t.Errorf("dry run lost the identity link: %+v", again)
	}
	if all, _ := a.Persons.List(ctx, admin, person.ListFilter{}); len(all) != 0 {
		t.Errorf("dry run stored %d people", len(all))
	}

	// A real run stores tokens and people, and a later resolver finds them.
	live, _ := a.Persons.Identities(admin, "k-1", true)
	if v, _ := live.Token(ctx, "h-name", "Kent"); v != "Kent" {
		t.Errorf("token = %s", v)
	}
	stored, err := live.Person(ctx, []string{"h-email"}, draw)
	if err != nil || stored.ID == "" {
		t.Fatalf("person = %+v, %v", stored, err)
	}
	if _, err := live.Person(ctx, []string{"h-email", "h-display"}, synth.New(7).Person("Y")); err != nil {
		t.Fatal(err)
	}

	later, _ := a.Persons.Identities(admin, "k-1", false)
	if v, ok, err := later.LookupToken(ctx, "h-name"); !ok || v != "Kent" || err != nil {
		t.Errorf("lookup = %s %v %v", v, ok, err)
	}
	if _, ok, _ := later.LookupToken(ctx, "h-missing"); ok {
		t.Error("missing token found")
	}
	if v, _ := later.Token(ctx, "h-name", "Other"); v != "Kent" {
		t.Errorf("stored token overwritten: %s", v)
	}
	// Linked through the second hash alone.
	found, err := later.Person(ctx, []string{"h-display"}, synth.New(8).Person("Z"))
	if err != nil || found.ID != stored.ID {
		t.Errorf("found = %+v, want %s", found, stored.ID)
	}
	if all, _ := a.Persons.List(ctx, admin, person.ListFilter{}); len(all) != 1 {
		t.Errorf("registry has %d people, want 1", len(all))
	}
}
