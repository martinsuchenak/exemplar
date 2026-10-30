package textswap

import "testing"

func TestApply(t *testing.T) {
	r, err := New(map[string]string{
		"Ann": "Bea", "Bea": "Cy", "acme.example": "zeta.example", "A&B Pty Ltd": "C&D Pty Ltd",
		"1234.56": "9999.99", "": "ignored",
	})
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"Ann and Bea":                            "Bea and Cy",
		"ANN, ann, Annual, Joanne":               "BEA, Bea, Annual, Joanne",
		"x@acme.example www.acme.example":        "x@zeta.example www.zeta.example",
		"acme.example.au acme.examples":          "acme.example.au acme.examples",
		"ann@x.example x.Ann Ann-Marie":          "ann@x.example x.Ann Ann-Marie",
		"A&amp;B Pty Ltd and A&B Pty Ltd":        "C&amp;D Pty Ltd and C&D Pty Ltd",
		"pay 1234.56 not v1234.56 nor 1.1234.56": "pay 9999.99 not v1234.56 nor 1.1234.56",
		"Ann.":                                   "Bea.",
		"@Ann said, cc @ANN (@Ann)":              "@Bea said, cc @BEA (@Bea)",
		"x@Ann x.@Ann x-@Ann":                    "x@Ann x.@Ann x-@Ann",
	} {
		if got := r.Apply(in); got != want {
			t.Errorf("Apply(%q) = %q, want %q", in, got, want)
		}
	}
	var zero *Replacer
	if zero.Apply("Ann") != "Ann" || (&Replacer{}).Apply("Ann") != "Ann" {
		t.Error("empty replacer changed text")
	}
	if r, err := New(nil); err != nil || r.Apply("Ann") != "Ann" {
		t.Errorf("New(nil) = %v", err)
	}
}

func TestExactKeys(t *testing.T) {
	r, err := NewCased(map[string]string{"grace@x.example": "ann@y.example"},
		map[string]string{"Grace": "Ann", "Price": "Lee", "Grace Price": "Ann Lee", "grace@x.example": "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"the grace period, the price":    "the grace period, the price",
		"Grace Price, GRACE PRICE":       "Ann Lee, ANN LEE",
		"Grace PRICE and grace Price":    "Ann LEE and grace Lee",
		"mail GRACE@X.EXAMPLE or Grace.": "mail ANN@Y.EXAMPLE or Ann.",
		"Gracefully priced, Grace-Price": "Gracefully priced, Grace-Price",
	} {
		if got := r.Apply(in); got != want {
			t.Errorf("Apply(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLooseKeys(t *testing.T) {
	r, err := NewSets(Sets{Loose: map[string]string{"acme": "zeta"}, AnyCase: map[string]string{"acme": "ignored", "Ann": "Bea"}})
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"acme-prod logo_acme.png ACME x.acme.example": "zeta-prod logo_zeta.png ZETA x.zeta.example",
		"acmeco acme2 2acme":                          "acmeco acme2 2acme",
		"Ann-Marie and Ann":                           "Ann-Marie and Bea",
	} {
		if got := r.Apply(in); got != want {
			t.Errorf("Apply(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCaseVariantsKeepTheirOwnReplacement(t *testing.T) {
	for i := 0; i < 20; i++ { // map order must not matter
		r, err := NewSets(Sets{Loose: map[string]string{"Acme": "Zeta Group", "acme": "zetagroup"}})
		if err != nil {
			t.Fatal(err)
		}
		if got := r.Apply("Acme, acme, ACME"); got != "Zeta Group, zetagroup, ZETA GROUP" {
			t.Fatalf("Apply = %q", got)
		}
	}
}

func TestMatchesAreWhatApplyReplaces(t *testing.T) {
	r, err := New(map[string]string{"Sarah Lane": "Ann Lee", "acme": "zeta"})
	if err != nil {
		t.Fatal(err)
	}
	s := "SARAH LANE of acme met Sarah Laneway"
	got := r.Matches(s)
	want := []Match{{Start: 0, End: 10, To: "ANN LEE"}, {Start: 14, End: 18, To: "zeta"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("matches = %+v", got)
	}
	if r.Apply(s) != "ANN LEE of zeta met Sarah Laneway" {
		t.Errorf("apply = %q", r.Apply(s))
	}
	if (&Replacer{}).Matches(s) != nil || (*Replacer)(nil).Matches(s) != nil {
		t.Error("an empty replacer matched")
	}
}
