package orderedjson_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/shared/orderedjson"
	"skillhub/internal/testsupport"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// lockLike is shaped like the global lock of npx skills, as JSON.stringify
// writes it with an indent of 2, plus the cases a rewrite must keep: keys
// out of alphabetical order, unknown keys, empty containers, <, > and &,
// non-ASCII text, escapes written in a form Go would not choose, and number
// literals Go would print differently.
const lockLike = `{
  "version": 3,
  "skills": {
    "grill-me": {
      "source": "mattpocock/skills",
      "sourceType": "github",
      "sourceUrl": "https://github.com/mattpocock/skills.git",
      "skillPath": "skills/productivity/grill-me/SKILL.md",
      "skillFolderHash": "0123456789abcdef0123456789abcdef01234567",
      "installedAt": "2026-10-01T10:00:00.000Z",
      "updatedAt": "2026-10-02T10:00:00.000Z",
      "skillName": "grill-me",
      "futureKey": {
        "nested": [
          1,
          2.50,
          -0,
          1e3,
          true,
          false,
          null
        ]
      }
    },
    "a-first-but-second": {
      "source": "x/y",
      "note": "<b> & </b> ñandú é \/   tab\there"
    }
  },
  "dismissed": {},
  "lastSelectedAgents": [],
  "zzz": "unknown top-level key"
}`

func TestARoundTripIsByteIdentical(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"a lock-like document without a final newline", lockLike},
		{"a lock-like document with a final newline", lockLike + "\n"},
		{"an empty object", "{}"},
		{"an empty object with a final newline", "{}\n"},
		{"a top-level array", "[\n  \"a\",\n  {}\n]"},
		{"a top-level scalar", `"<&>"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := orderedjson.Parse([]byte(tt.input))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := string(doc.Bytes()); got != tt.input {
				t.Fatalf("round trip changed the bytes:\n%s\nwant:\n%s", got, tt.input)
			}
		})
	}
}

func TestReadingValues(t *testing.T) {
	doc := mustParse(t, lockLike)
	root := doc.Root()
	if got := root.Keys(); !slices.Equal(got, []string{"version", "skills", "dismissed", "lastSelectedAgents", "zzz"}) {
		t.Fatalf("Keys() = %q, want the file's order", got)
	}
	version, ok := root.Get("version")
	if !ok || version.Kind() != orderedjson.KindNumber {
		t.Fatalf("Get(version) = %v, %v", version, ok)
	}
	if n, err := version.Int(); err != nil || n != 3 {
		t.Fatalf("version.Int() = %d, %v; want 3", n, err)
	}
	skills, _ := root.Get("skills")
	if got := skills.Keys(); !slices.Equal(got, []string{"grill-me", "a-first-but-second"}) {
		t.Fatalf("skills keys = %q", got)
	}
	second, _ := skills.Get("a-first-but-second")
	note, _ := second.Get("note")
	if got, err := note.Text(); err != nil || got != "<b> & </b> ñandú é /   tab\there" {
		t.Fatalf("note.Text() = %q, %v", got, err)
	}
	if _, err := version.Text(); !errors.Is(err, orderedjson.ErrWrongKind) {
		t.Fatalf("Text() of a number = %v, want ErrWrongKind", err)
	}
	if _, ok := root.Get("absent"); ok {
		t.Fatal("Get(absent) reported a value")
	}
	agents, _ := root.Get("lastSelectedAgents")
	if agents.Kind() != orderedjson.KindArray || agents.Len() != 0 {
		t.Fatalf("lastSelectedAgents = kind %v, len %d", agents.Kind(), agents.Len())
	}
	nested := mustGet(t, mustGet(t, mustGet(t, skills, "grill-me"), "futureKey"), "nested")
	if nested.Len() != 7 || nested.Index(1).Kind() != orderedjson.KindNumber || nested.Index(6).Kind() != orderedjson.KindNull {
		t.Fatalf("nested array read wrong: len %d", nested.Len())
	}
}

func TestEditingChangesOnlyWhatWasEdited(t *testing.T) {
	doc := mustParse(t, lockLike+"\n")
	entry := mustGet(t, mustGet(t, doc.Root(), "skills"), "grill-me")
	if err := entry.Set("skillFolderHash", orderedjson.String("fedcba9876543210fedcba9876543210fedcba98")); err != nil {
		t.Fatal(err)
	}
	if err := entry.Set("updatedAt", orderedjson.String("2026-11-21T12:00:00.000Z")); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(lockLike+"\n", `"skillFolderHash": "0123456789abcdef0123456789abcdef01234567"`, `"skillFolderHash": "fedcba9876543210fedcba9876543210fedcba98"`, 1)
	want = strings.Replace(want, `"updatedAt": "2026-10-02T10:00:00.000Z"`, `"updatedAt": "2026-11-21T12:00:00.000Z"`, 1)
	if got := string(doc.Bytes()); got != want {
		t.Fatalf("after editing two values:\n%s\nwant:\n%s", got, want)
	}

	// A new key goes last, in the same layout.
	if err := doc.Root().Set("added", orderedjson.Int(7)); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Bytes()); !strings.HasSuffix(got, "  \"zzz\": \"unknown top-level key\",\n  \"added\": 7\n}\n") {
		t.Fatalf("a new key was not appended last:\n%s", got)
	}
	if !doc.Root().Delete("added") || doc.Root().Delete("added") {
		t.Fatal("Delete should remove an existing key once")
	}
	if got := string(doc.Bytes()); got != want {
		t.Fatalf("after deleting the added key:\n%s\nwant:\n%s", got, want)
	}
}

// New strings are written as JSON.stringify writes them: <, >, & and U+2028
// stay as they are, control characters are escaped with lowercase hex.
func TestNewStringsAreWrittenLikeJavaScript(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"html characters stay as they are", "<a & b>", `"<a & b>"`},
		{"non-ASCII text stays as it is", "ñandú    ", "\"ñandú    \""},
		{"quotes and backslashes are escaped", `say "hi" \ bye`, `"say \"hi\" \\ bye"`},
		{"short escapes for the usual controls", "\b\f\n\r\t", `"\b\f\n\r\t"`},
		{"other controls use lowercase hex", "\x01\x1f", `"\u0001\u001f"`},
		{"DEL is not a control for JSON", "\x7f", "\"\x7f\""},
		{"invalid UTF-8 becomes the replacement character", "a\xffb", "\"a�b\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(orderedjson.String(tt.in).Bytes()); got != tt.want {
				t.Fatalf("String(%q).Bytes() = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

// A value saved on its own and put back at its old place leaves the document
// byte-identical: this is how a lock entry is restored from a snapshot.
func TestAValueSavedAloneRestoresByteIdentical(t *testing.T) {
	doc := mustParse(t, lockLike)
	skills := mustGet(t, doc.Root(), "skills")
	saved := mustGet(t, skills, "grill-me").Bytes()

	if err := mustGet(t, skills, "grill-me").Set("skillFolderHash", orderedjson.String("changed")); err != nil {
		t.Fatal(err)
	}
	restored, err := orderedjson.ParseValue(saved)
	if err != nil {
		t.Fatalf("ParseValue(saved entry): %v", err)
	}
	if err := skills.Set("grill-me", restored); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Bytes()); got != lockLike {
		t.Fatalf("restored document:\n%s\nwant:\n%s", got, lockLike)
	}
}

func TestOtherLayoutsAreRewrittenWithTwoSpaces(t *testing.T) {
	doc := mustParse(t, `{"a":[1,{"b":null}],  "c" : {}}`)
	want := "{\n  \"a\": [\n    1,\n    {\n      \"b\": null\n    }\n  ],\n  \"c\": {}\n}"
	if got := string(doc.Bytes()); got != want {
		t.Fatalf("compact input rewritten as:\n%s\nwant:\n%s", got, want)
	}
}

func TestParseRejectsWhatItCannotKeep(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"invalid JSON", `{"a": }`, orderedjson.ErrSyntax},
		{"trailing data", `{} {}`, orderedjson.ErrSyntax},
		{"empty input", ``, orderedjson.ErrSyntax},
		{"a duplicate key would lose one of its values", `{"a": 1, "a": 2}`, orderedjson.ErrDuplicateKey},
		{"a duplicate key written with an escape", `{"a": 1, "a": 2}`, orderedjson.ErrDuplicateKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := orderedjson.Parse([]byte(tt.input)); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Parse(%q) = %v, want %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestEditingTheWrongKindIsRefused(t *testing.T) {
	arr := mustParse(t, `[1]`).Root()
	if err := arr.Set("k", orderedjson.Null()); !errors.Is(err, orderedjson.ErrWrongKind) {
		t.Fatalf("Set on an array = %v, want ErrWrongKind", err)
	}
	if _, err := orderedjson.Number("01"); !errors.Is(err, orderedjson.ErrSyntax) {
		t.Fatalf("Number(01) = %v, want ErrSyntax", err)
	}
	n, err := orderedjson.Number("2.50")
	if err != nil || string(n.Bytes()) != "2.50" {
		t.Fatalf("Number(2.50) = %v, %v", n, err)
	}
	if got := string(orderedjson.Bool(true).Bytes()); got != "true" {
		t.Fatalf("Bool(true) = %s", got)
	}
}

func mustParse(t *testing.T, s string) *orderedjson.Document {
	t.Helper()
	doc, err := orderedjson.Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func mustGet(t *testing.T, v *orderedjson.Value, key string) *orderedjson.Value {
	t.Helper()
	got, ok := v.Get(key)
	if !ok {
		t.Fatalf("missing key %q", key)
	}
	return got
}
