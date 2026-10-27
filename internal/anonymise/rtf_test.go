package anonymise

import (
	"bytes"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/synth"
)

func hexOf(s string) string { return hex.EncodeToString([]byte(s)) }

// wordRTF is shaped like a file Word saves, with the same secrets in every
// place RTF can hide them.
var wordRTF = `{\rtf1\ansi\ansicpg1252\deff0\uc1` + "\r\n" +
	`{\fonttbl{\f0\froman\fcharset0 Times New Roman;}}{\colortbl;\red0\green0\blue0;}{\*\generator Riched20 10.0.19041}` + "\r\n" +
	`{\info{\title Quote for Sarah Lane}{\author Sarah Lane}{\operator Priya Shah}{\company Acme Plumbing Pty Ltd}{\manager Priya}` +
	`{\doccomm call 0412 345 678}{\hlinkbase https://acme-plumbing.com.au/docs/}{\creatim\yr2025\mo1\dy6}}` + "\r\n" +
	`{\*\revtbl {Unknown;}{Priya Shah;}}{\*\template C:\\Users\\jdoe\\Templates\\Acme Quote.dotm}` + "\r\n" +
	`\pard Dear {\b Sa}rah La\i ne,\par` + "\r\n" +
	`\i0 Caf\'e9 owner Sarah Lane \u8212? call 0412 345 678.\par` + "\r\n" +
	`{\field{\*\fldinst HYPERLINK "mailto:sarah.lane@acme-plumbing.com.au"}{\fldrslt sarah.lane@acme-plumbing.com.au}}\par` + "\r\n" +
	`{\field{\*\fldinst MERGEFIELD FirstName}{\fldrslt \'abFirstName\'bb}}\par` + "\r\n" +
	`{\*\atnid PS}{\*\atnauthor Priya Shah}\chatn{\*\annotation{\*\atnref 1} Check with Sarah Lane}\par` + "\r\n" +
	`{\footnote \pard Priya, 0412 345 678}{\header \pard Prepared for Sarah Lane\par}` + "\r\n" +
	`Call\tab Sarah Lane\par` + "\r\n" +
	`{\shp{\*\shpinst{\sp{\sn wzDescription}{\sv Photo of Sarah Lane}}{\sp{\sn fFilled}{\sv 0}}{\shptxt Priya Shah}}}` + "\r\n" +
	`{\*\shppict{\pict\pngblip\picw10\pich10 ` + hexOf("\x89PNG real photo") + `}}{\nonshppict{\pict\wmetafile8\picw10 ` + hexOf("real wmf") + `}}` + "\r\n" +
	`{\object\objemb{\*\objclass Excel.Sheet.12}{\*\objdata ` + hexOf("real salary") + `}{\result{\pict\jpegblip ` + hexOf("\xff\xd8 real scan") + `}}}` + "\r\n" +
	`{\*\datastore ` + hexOf("jdoe metadata") + `}{\*\userprops {\propname Client}\proptype30{\staticval Sarah Lane}}` + "\r\n" +
	`{\field{\*\fldinst {FORMTEXT }{\*\datafield ` + hexOf("Lane default") + `}{\*\formfield{\fftype0\ffownhelp\ffownstat{\*\ffname Text1}` +
	`{\*\ffdeftext Sarah Lane}{\*\ffstattext Ask Priya Shah}{\*\ffhelptext call 0412 345 678}{\*\ffl Priya Shah}}}}{\fldrslt Sarah Lane}}\par` + "\r\n" +
	"}\r\n\x00"

var rtfSecrets = []string{
	"Sarah", "Lane", "Priya", "Shah", "acme-plumbing", "Acme Plumbing", "0412 345 678", "jdoe",
	hexOf("real photo"), hexOf("real wmf"), hexOf("real salary"), hexOf("real scan"), hexOf("jdoe"), hexOf("Lane default"), `\*\atnid PS}`,
}

func TestRTFLosesEverySecret(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	f := File{Name: "Quote.RTF", Data: []byte(wordRTF)}
	if errs := e.Prepare(ctx, []File{f}); errs[0] != nil {
		t.Fatal(errs[0])
	}
	out, err := e.Anonymise(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out.Data)
	for _, s := range rtfSecrets {
		if strings.Contains(strings.ToLower(got), strings.ToLower(s)) {
			t.Errorf("output still holds %q", s)
		}
	}
	if out.Format != FormatRTF || !strings.HasSuffix(out.Name, ".rtf") || out.Attachments != 2 ||
		strings.Join(out.DroppedParts, ",") != "picture 2 (wmetafile),embedded object data,data store,form field data" {
		t.Errorf("format %s, name %s, attachments %d, dropped %q", out.Format, out.Name, out.Attachments, out.DroppedParts)
	}
	for _, keep := range []string{
		// Structure, tables and dates stay as written.
		`{\fonttbl{\f0\froman\fcharset0 Times New Roman;}}{\colortbl;\red0\green0\blue0;}{\*\generator Riched20 10.0.19041}`,
		`{\creatim\yr2025\mo1\dy6}`, `{\*\revtbl {Unknown;}{`, `{\field{\*\fldinst MERGEFIELD FirstName}{\fldrslt \'abFirstName\'bb}}`,
		`{\sp{\sn wzDescription}{\sv `, `{\sp{\sn fFilled}{\sv 0}}`, `Call\tab `, "}\r\n\x00",
		// Text around a name keeps its escapes; an em dash outside ASCII is
		// written back in Windows-1252.
		`\i0 Caf\'e9 owner `, ` \'97 call 0`,
		// Replaced pictures keep their type.
		`{\*\shppict{\pict\pngblip\picw10\pich10 ` + "\r\n" + `89504e47`, `{\result{\pict\jpegblip ` + "\r\n" + `ffd8`,
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("output lost %q", keep)
		}
	}
	// The split name: the fake inside the bold group, the rest emptied, the
	// comma kept after \i.
	if !regexp.MustCompile(`\\pard Dear \{\\b [A-Z][a-z]+ [A-Z][a-z]+\}\\i ,\\par`).MatchString(got) {
		t.Errorf("split runs:\n%s", got)
	}
	// The comment's initials follow its fake author.
	m := regexp.MustCompile(`\{\\\*\\atnid ([^}]*)\}\{\\\*\\atnauthor ([^}]*)\}`).FindStringSubmatch(got)
	if m == nil || m[1] != initials(m[2]) {
		t.Errorf("atnid and atnauthor = %v", m)
	}
	if !regexp.MustCompile(`\{\\\*\\template file:///anonymised/[0-9a-f]{10}\.dotm\}`).MatchString(got) {
		t.Errorf("template path not replaced:\n%s", got)
	}
	again, err := newEngine(t, newMemRegistry()).Anonymise(ctx, f)
	if err != nil || !bytes.Equal(again.Data, out.Data) {
		t.Errorf("a second run differs: %v", err)
	}
}

func TestRTFNamesCarryAcrossFiles(t *testing.T) {
	doc := File{Name: "a.rtf", Data: []byte(`{\rtf1\ansi{\info{\author Wendy Moss}}\pard Hello\par}`)}
	notes := File{Name: "n.txt", Data: []byte("Wendy Moss called\n")}
	e := newEngine(t, newMemRegistry())
	for _, err := range e.Prepare(ctx, []File{doc, notes}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := e.Anonymise(ctx, notes)
	if err != nil || strings.Contains(string(out.Data), "Wendy") {
		t.Errorf("notes = %q, %v", out.Data, err)
	}
}

func TestGeneratedRTFRoundTrip(t *testing.T) {
	g := synth.New(5)
	people := []synth.Person{g.Person(g.CompanyName()), g.Person(g.CompanyName())}
	file, err := g.File("rtf", synth.FileInput{People: people})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range people {
		names = append(names, p.Name())
	}
	e, err := New(testKey(t), defaultProfile(t), wordDetector{names: names}, newMemRegistry())
	if err != nil {
		t.Fatal(err)
	}
	f := File{Name: "x.rtf", Data: file.Data}
	e.Prepare(ctx, []File{f})
	out, err := e.Anonymise(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if strings.Contains(string(out.Data), n) {
			t.Errorf("%s survived:\n%s", n, out.Data)
		}
	}
}

func TestRTFErrors(t *testing.T) {
	for name, c := range map[string]struct{ data, want string }{
		"not rtf":      {"hello", `does not start with {\rtf`},
		"extra close":  {`{\rtf1 a}}`, "closes no group"},
		"unclosed":     {`{\rtf1 {\b a}`, "never closed"},
		"bad escape":   {`{\rtf1 \'zz}`, `bad \' escape`},
		"cut escape":   {`{\rtf1 \'e`, `cut-off \' escape`},
		"trailing \\":  {`{\rtf1 a\`, "ends in a backslash"},
		"bin past end": {`{\rtf1 {\pict\bin99 ab}}`, "runs past the end"},
		"code page":    {`{\rtf1\ansi\ansicpg1250 a}`, "code page 1250 is not supported"},
		"mac":          {`{\rtf1\mac a}`, "mac character set"},
		"too deep":     {`{\rtf1` + strings.Repeat("{", rtfMaxDepth+1), "nest more than"},
	} {
		f := File{Name: "a.rtf", Data: []byte(c.data)}
		e := newEngine(t, newMemRegistry())
		perr := e.Prepare(ctx, []File{f})[0]
		_, err := e.Anonymise(ctx, f)
		if err == nil || perr == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: prepare %v, anonymise %v, want %q", name, perr, err, c.want)
		}
	}
}

func TestRTFDecoding(t *testing.T) {
	for in, want := range map[string]string{
		`{\rtf1 Caf\'e9 \'93q\'94}`:         "Café “q”",
		`{\rtf1\uc1 a\u233?b}`:              "aéb",
		`{\rtf1\uc1 a\u233\'e9b}`:           "aéb", // the fallback is a \'xx
		`{\rtf1\uc0 a\u233 b}`:              "aéb",
		`{\rtf1\uc2 a\u-10179??\u-8704??b}`: "a😀b",
		`{\rtf1 a\{b\}c\\d\~e\-f\emdash g}`: "a{b}c\\d\u00a0ef—g",
		"{\\rtf1 one\r\ntwo}":               "onetwo",
		`{\rtf1 {\pict\bin3 a}b}x}`:         "x",
	} {
		d, err := parseRTF([]byte(in))
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		var got []string
		for i := range d.units {
			got = append(got, d.units[i].text())
		}
		if strings.Join(got, "|") != want {
			t.Errorf("%s = %q, want %q", in, got, want)
		}
	}
	// Breaks and destinations end a run of text; formatting does not.
	d, err := parseRTF([]byte(`{\rtf1 Sa{\b rah} La\i ne\tab Next{\footnote Note}After\par Last}`))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := range d.units {
		got = append(got, d.units[i].text())
	}
	if strings.Join(got, "|") != "Sarah Lane|Next|Note|After|Last" {
		t.Errorf("units = %q", got)
	}
}

func TestRTFEncode(t *testing.T) {
	for _, c := range []struct {
		in        string
		uc        int
		afterWord bool
		want      string
	}{
		{"Ann {Lee}\\", 1, false, `Ann \{Lee\}\\`},
		{"Zoë – ok", 1, false, `Zo\'eb \'96 ok`},
		{"Łukasz", 1, false, `\u321?ukasz`},
		{"Łukasz", 0, false, `\u321 ukasz`},
		{"😀", 1, false, `\u-10179?\u-8704?`},
		{"Ann", 1, true, " Ann"},
		{"", 1, true, ""},
	} {
		if got := rtfEncode(c.in, c.uc, c.afterWord); got != c.want {
			t.Errorf("rtfEncode(%q, %d, %v) = %q, want %q", c.in, c.uc, c.afterWord, got, c.want)
		}
	}
	// What rtfEncode writes decodes back to what it was given.
	for _, s := range []string{"Zoë Łukasz 😀 {x}", "a\\b"} {
		d, err := parseRTF([]byte(`{\rtf1\uc1 ` + rtfEncode(s, 1, false) + `}`))
		if err != nil || len(d.units) != 1 || d.units[0].text() != s {
			t.Errorf("round trip of %q: %v %+v", s, err, d)
		}
	}
}

func TestRTFSkeletonIgnoresTextAndPictures(t *testing.T) {
	a, _ := tokenizeRTF([]byte(`{\rtf1\b Ann \'e9{\pict\pngblip 00}\u233?}`))
	b, _ := tokenizeRTF([]byte(`{\rtf1\b Zoë Lee{\pict\jpegblip ffd8}}`))
	c, _ := tokenizeRTF([]byte(`{\rtf1\i Ann}`))
	if rtfSkeleton(nil, a, nil) != rtfSkeleton(nil, b, nil) || rtfSkeleton(nil, a, nil) == rtfSkeleton(nil, c, nil) {
		t.Errorf("skeletons: %q %q %q", rtfSkeleton(nil, a, nil), rtfSkeleton(nil, b, nil), rtfSkeleton(nil, c, nil))
	}
}
