package text_test

import (
	"fmt"
	"testing"

	"skillhub/internal/entrypoints/text"
)

// bidiControls are the Unicode bidi controls (A28): the Arabic letter mark,
// the left-to-right and right-to-left marks, the embeddings, overrides and
// their pop, and the isolates and their pop. The tests build every
// invisible character from its code point, so this source holds none.
var bidiControls = []rune{
	0x061c, 0x200e, 0x200f,
	0x202a, 0x202b, 0x202c, 0x202d, 0x202e,
	0x2066, 0x2067, 0x2068, 0x2069,
}

// rlo is U+202E, the right-to-left override, and rloShown how it prints.
// hostile is third-party text built to act on the terminal: \r returns to
// the start of the line, ESC[2K erases it and rlo reverses what follows;
// hostileShown is how it prints.
var (
	rlo          = string(rune(0x202e))
	rloShown     = shown(0x202e)
	hostile      = "\r\x1b[2K" + rlo
	hostileShown = `\r\x1b[2K` + rloShown
)

// shown is how a rune above ASCII that Safe escapes prints.
func shown(r rune) string { return fmt.Sprintf(`\u%04x`, r) }

// Text from a third party cannot act on the terminal: what would move the
// cursor, erase, recolor or reorder prints as a visible Go-style escape.
func TestSafeEscapesWhatWouldActOnTheTerminal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"carriage return", "aprobado\rinyectado", `aprobado\rinyectado`},
		{"escape sequence", "a\x1b[2Kb\x1b[31mc\x1b[0m", `a\x1b[2Kb\x1b[31mc\x1b[0m`},
		{"cursor movement", "uno\x1b[1Ados", `uno\x1b[1Ados`},
		{"hyperlink", "\x1b]8;;https://otro.example\x07texto\x1b]8;;\x07", `\x1b]8;;https://otro.example\atexto\x1b]8;;\a`},
		{"NUL", "a\x00b", `a\x00b`},
		{"backspace", "ab\bc", `ab\bc`},
		{"vertical tab and form feed", "a\vb\fc", `a\vb\fc`},
		{"other C0 controls", "a\x01b\x1fc", `a\x01b\x1fc`},
		{"DEL", "a\x7fb", `a\x7fb`},
		{"C1 next line", "a" + string(rune(0x85)) + "b", "a" + shown(0x85) + "b"},
		{"C1 control sequence introducer", "a" + string(rune(0x9b)) + "2Kb", "a" + shown(0x9b) + "2Kb"},
		{"bytes that are not UTF-8", "a\x9b2K\xffb", `a\x9b2K\xffb`},
		{"right-to-left override", "ab" + rlo + "dc.md", "ab" + rloShown + "dc.md"},
		{"everything at once", "línea\r\x1b[2K" + rlo + "oculta", `línea\r\x1b[2K` + rloShown + "oculta"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := text.Safe(tt.in); got != tt.want {
				t.Errorf("Safe(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if got := text.SafeLine(tt.in); got != tt.want {
				t.Errorf("SafeLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSafeEscapesEveryBidiControl(t *testing.T) {
	for _, r := range bidiControls {
		in := "a" + string(r) + "b"
		want := "a" + shown(r) + "b"
		if got := text.Safe(in); got != want {
			t.Errorf("Safe(%q) = %q, want %q", in, got, want)
		}
		if got := text.SafeLine(in); got != want {
			t.Errorf("SafeLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// Ordinary text prints as it is: accents, eñes, emoji (with their joiners
// and tags), right-to-left scripts, a backslash written as text, tabs and
// line breaks.
func TestSafeLeavesOrdinaryTextAsItIs(t *testing.T) {
	zwj := string(rune(0x200d))
	lines := []string{
		"",
		"Canción del ñandú: café, pingüino, Ångström, ½ € ©",
		"emoji 🎉, familia 👨" + zwj + "👩" + zwj + "👧 y bandera 🏴" + tags("gbsct"),
		"hebreo שלום y árabe مرحبا",
		`una barra invertida: \x1b, \r y ` + rloShown + ` escritos como texto`,
		"carácter de reemplazo " + string(rune(0xfffd)) + " y espacio sin corte" + string(rune(0xa0)) + "aquí",
	}
	for _, s := range lines {
		if got := text.Safe(s); got != s {
			t.Errorf("Safe(%q) = %q, want it unchanged", s, got)
		}
		if got := text.SafeLine(s); got != s {
			t.Errorf("SafeLine(%q) = %q, want it unchanged", s, got)
		}
	}
	block := "---\ndescription: Úsala así\n---\n\tcon sangría\nfin\n"
	if got := text.Safe(block); got != block {
		t.Errorf("Safe(%q) = %q, want it unchanged", block, got)
	}
}

// tags spells s with Unicode tag characters and closes it with the cancel
// tag: the tail of a subdivision flag emoji.
func tags(s string) string {
	out := ""
	for _, r := range s {
		out += string(0xe0000 + r)
	}
	return out + string(rune(0xe007f))
}

// A value that must stay on one line, such as a name or a path, cannot
// start a line or a column of its own: SafeLine escapes \n and \t too.
func TestSafeLineKeepsAValueOnOneLine(t *testing.T) {
	in := "nombre\tcolumna\nfila\r"
	if got, want := text.SafeLine(in), `nombre\tcolumna\nfila\r`; got != want {
		t.Errorf("SafeLine(%q) = %q, want %q", in, got, want)
	}
	if got, want := text.Safe(in), "nombre\tcolumna\nfila"+`\r`; got != want {
		t.Errorf("Safe(%q) = %q, want %q", in, got, want)
	}
}

// The file name in the header of a unified diff stays on its header line,
// so it cannot add lines of its own to the diff; the rest of the diff is
// Safe.
func TestSafeDiffKeepsTheFileNameInItsHeader(t *testing.T) {
	path := "notas\n+inyectada" + rlo + ".md"
	unified := "--- a/" + path + "\n+++ b/" + path + "\n@@ -1,2 +1,2 @@\n-uno\r\n+dos\x1b[2K\n \tigual\n"
	name := `notas\n+inyectada` + rloShown + ".md"
	want := "--- a/" + name + "\n+++ b/" + name + "\n" +
		"@@ -1,2 +1,2 @@\n" + `-uno\r` + "\n" + `+dos\x1b[2K` + "\n \tigual\n"
	if got := text.SafeDiff(path, unified); got != want {
		t.Errorf("SafeDiff = %q, want %q", got, want)
	}

	ordinary := "--- a/referencias/canción.md\n+++ b/referencias/canción.md\n@@ -1 +1 @@\n-uno\n+dos 🎉\n"
	if got := text.SafeDiff("referencias/canción.md", ordinary); got != ordinary {
		t.Errorf("SafeDiff of an ordinary diff = %q, want it unchanged", got)
	}
	if got := text.SafeDiff("x.md", ""); got != "" {
		t.Errorf("SafeDiff of no diff = %q, want empty", got)
	}
	// A diff whose header is not the expected one is still Safe.
	other := "--- otro\x1b[2K\n+++ otro\n"
	if got, want := text.SafeDiff("x.md", other), `--- otro\x1b[2K`+"\n+++ otro\n"; got != want {
		t.Errorf("SafeDiff = %q, want %q", got, want)
	}
}
