package anonymise

import "testing"

// The parsers read files from anyone with a key: whatever the bytes, they
// return an error or a result, never panic.

func FuzzParseRTF(f *testing.F) {
	f.Add([]byte(wordRTF))
	f.Add([]byte(`{\rtf1\uc2 a\u-10179??\u-8704??{\pict\bin3 a}b}x}`))
	f.Add([]byte(`{\rtf1{\*\atnid A}{\*\atnauthor B}{\sp{\sn wzDescription}{\sv C}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := parseRTF(data)
		if err != nil {
			return
		}
		_ = d.texts()
		if _, err := splice(d.data, nil); err != nil {
			t.Fatal(err)
		}
		_ = rtfSkeleton(d.data, d.toks, d.drops)
		// The whole rewrite too: an error is fine, a panic is not.
		e, err := New(testKey(t), defaultProfile(t), wordDetector{names: []string{"Sarah Lane", "B"}}, newMemRegistry())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = e.Anonymise(ctx, File{Name: "f.rtf", Data: data})
	})
}

func FuzzScanXML(f *testing.F) {
	f.Add([]byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t a='1'>x &amp; y</w:t></w:r></w:p></w:document>`))
	f.Add([]byte(`<a b="c" d='e'/><!-- x --><?pi x?>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		nodes, err := scanXML(data)
		if err != nil {
			return
		}
		for _, n := range nodes {
			if n.start < 0 || n.end > len(data) || n.start > n.end {
				t.Fatalf("node outside the data: %+v", n)
			}
			for _, a := range n.attrs {
				if a.start < n.start || a.end > n.end || a.start > a.end {
					t.Fatalf("attribute outside its tag: %+v", a)
				}
			}
		}
	})
}
