package text

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text that comes from a third party (a skill's name and description, the
// names of its files, the lines of a diff) goes through Safe, SafeLine or
// SafeDiff before the CLI or the TUI prints it (A28 of the specification).
// A control character, a bidi control or a byte that is not UTF-8 prints
// as the visible escape Go gives it: \r, \x1b, or a backslash, a u and four
// hex digits for U+202E and the other bidi controls. It no longer acts on
// the terminal: \r cannot return to the start of a line and overwrite what
// was approved, an escape sequence loses its ESC or its C1 introducer and
// prints as text, and a bidi control cannot reorder what follows it.
// Everything else prints as it is: accents, emoji with their joiners,
// other scripts and a backslash written as text. internal/staging refuses
// file names with these characters with the same rule.

// Safe returns third-party text that may span lines, such as a diff or a
// description, ready to print. \n and \t stay; every other control
// character, bidi control and byte that is not UTF-8 is escaped.
func Safe(s string) string { return escape(s, true) }

// SafeLine returns a third-party value that must stay on one line, such as
// a name, a path or an identifier, ready to print. It escapes what Safe
// escapes and \n and \t too, so the value can start neither a line nor a
// column of its own.
func SafeLine(s string) string { return escape(s, false) }

// SafeDiff returns the unified diff of the file at path ready to print: the
// path in its two header lines is SafeLine, so a file name cannot add lines
// of its own to the diff, and the rest is Safe. A diff that does not start
// with the header of path is Safe as a whole.
func SafeDiff(path, unified string) string {
	header := "--- a/" + path + "\n+++ b/" + path + "\n"
	rest, ok := strings.CutPrefix(unified, header)
	if !ok {
		return Safe(unified)
	}
	name := SafeLine(path)
	return "--- a/" + name + "\n+++ b/" + name + "\n" + Safe(rest)
}

// escape returns s with every unsafe rune and every byte that is not UTF-8
// escaped; multiline keeps \n and \t. Text with nothing to escape is
// returned as it is.
func escape(s string, multiline bool) string {
	start := firstUnsafe(s, multiline)
	if start < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	b.WriteString(s[:start])
	for i := start; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case unsafeRune(r, multiline):
			quoted := strconv.QuoteRune(r)
			b.WriteString(quoted[1 : len(quoted)-1])
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// firstUnsafe returns the index of the first byte of s to escape, or -1.
func firstUnsafe(s string, multiline bool) int {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || unsafeRune(r, multiline) {
			return i
		}
		i += size
	}
	return -1
}

// unsafeRune reports a rune that acts on a terminal: a C0 or C1 control
// character or DEL (but \n and \t in multiline text), or a bidi control.
func unsafeRune(r rune, multiline bool) bool {
	if multiline && (r == '\n' || r == '\t') {
		return false
	}
	return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r)
}
