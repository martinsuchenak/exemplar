package fixture

import (
	"strings"
	"testing"
)

func TestAttachmentAlwaysSaysAttachment(t *testing.T) {
	for _, name := range []string{"report.pdf", "Zoë's quote.pdf", "tab\there.txt", "bad\x7fname", `quote".txt`, strings.Repeat("x", 300)} {
		if got := attachment(name); !strings.HasPrefix(got, "attachment") {
			t.Errorf("attachment(%q) = %q", name, got)
		}
	}
	if got := attachment("report.pdf"); got != `attachment; filename=report.pdf` {
		t.Errorf("plain name = %q", got)
	}
}

func TestValidNameRejectsControlCharacters(t *testing.T) {
	for name, ok := range map[string]bool{"a.txt": true, "Zoë.txt": true, "a\tb": false, "a\x00b": false, "a\nb": false, "..": false} {
		if validName(name) != ok {
			t.Errorf("validName(%q) != %v", name, ok)
		}
	}
}
