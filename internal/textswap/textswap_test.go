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
