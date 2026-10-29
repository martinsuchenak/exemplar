package runlimit

import "testing"

func TestCapHoldsAndReleases(t *testing.T) {
	l := New(2)
	for i := range 2 {
		if !l.Acquire() {
			t.Fatalf("acquire %d under the cap failed", i+1)
		}
	}
	if l.Acquire() {
		t.Error("a third acquire over the cap succeeded")
	}
	l.Release()
	if !l.Acquire() {
		t.Error("acquire after release failed")
	}
}

func TestNoLimit(t *testing.T) {
	for name, l := range map[string]*Limiter{"nil": nil, "zero": {}, "negative": New(-1)} {
		for i := 0; i < 100; i++ {
			if !l.Acquire() {
				t.Fatalf("%s: acquire %d failed", name, i)
			}
		}
		l.Release() // must not panic, whatever it did
	}
}

func TestReleaseBelowZeroIsHarmless(t *testing.T) {
	l := New(1)
	l.Release()
	l.Release()
	if !l.Acquire() {
		t.Error("acquire after extra releases failed")
	}
}
