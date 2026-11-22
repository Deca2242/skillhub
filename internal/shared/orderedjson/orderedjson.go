// Package orderedjson reads and rewrites JSON documents without changing
// their shape, which encoding/json cannot do: Go maps lose key order and the
// standard encoder escapes <, > and & and adds a newline. Every JSON file
// skillhub rewrites, the npx skills lock first, goes through a Document,
// never through map[string]any (docs/design.md section 5).
//
// A document is written the way JavaScript's JSON.stringify(value, null, 2)
// writes it, which is how npx skills writes its lock: two-space indentation,
// "key": value, {} and [] for empty containers. What a round trip keeps
// byte for byte:
//   - key order and unknown keys;
//   - every scalar exactly as it was written: string escapes, number
//     literals such as 2.50 or 1e3;
//   - whether the file ended with a newline.
//
// Input in another layout, such as compact JSON, is rewritten with two-space
// indentation. New strings are escaped as JSON.stringify escapes them.
package orderedjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"unicode/utf8"
)

var (
	// ErrSyntax reports input that is not one valid JSON value.
	ErrSyntax = errors.New("invalid JSON")
	// ErrDuplicateKey reports an object with the same key twice, whose
	// values could not both be kept.
	ErrDuplicateKey = errors.New("duplicate JSON object key")
	// ErrWrongKind reports an operation on a value of another kind.
	ErrWrongKind = errors.New("wrong JSON value kind")
	// ErrMissingKey reports an object member that an operation needs and
	// that is not there.
	ErrMissingKey = errors.New("JSON object key not found")
)

// Kind is the kind of a JSON value.
type Kind int

const (
	KindNull Kind = iota
	KindBool
	KindNumber
	KindString
	KindArray
	KindObject
)

func (k Kind) String() string {
	switch k {
	case KindNull:
		return "null"
	case KindBool:
		return "bool"
	case KindNumber:
		return "number"
	case KindString:
		return "string"
	case KindArray:
		return "array"
	case KindObject:
		return "object"
	default:
		return "kind(" + strconv.Itoa(int(k)) + ")"
	}
}

// Value is one JSON value. A scalar keeps the exact bytes it was read from;
// an object keeps its members in order.
type Value struct {
	kind    Kind
	raw     []byte
	members []member
	items   []*Value
}

type member struct {
	key    string
	rawKey []byte
	value  *Value
}

// Document is a whole JSON file.
type Document struct {
	root            *Value
	trailingNewline bool
}

// Parse reads a document. It refuses invalid JSON and objects with a
// duplicate key.
func Parse(data []byte) (*Document, error) {
	v, err := ParseValue(data)
	if err != nil {
		return nil, err
	}
	return &Document{root: v, trailingNewline: bytes.HasSuffix(data, []byte("\n"))}, nil
}

// Root is the document's top-level value.
func (d *Document) Root() *Value { return d.root }

// Bytes writes the document, ending with a newline when the file read did.
func (d *Document) Bytes() []byte {
	var b bytes.Buffer
	write(&b, d.root, 0)
	if d.trailingNewline {
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// ParseValue reads one JSON value, such as an entry saved with Value.Bytes.
func ParseValue(data []byte) (*Value, error) {
	if !json.Valid(data) {
		return nil, fmt.Errorf("%w", ErrSyntax)
	}
	p := parser{data: data}
	p.skipSpace()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	return v, nil
}

// Bytes writes the value on its own, at indentation level zero, with no
// final newline.
func (v *Value) Bytes() []byte {
	var b bytes.Buffer
	write(&b, v, 0)
	return b.Bytes()
}

// Kind is the value's kind.
func (v *Value) Kind() Kind { return v.kind }

// Keys lists an object's keys in order; nil for any other kind.
func (v *Value) Keys() []string {
	if v.kind != KindObject {
		return nil
	}
	keys := make([]string, 0, len(v.members))
	for _, m := range v.members {
		keys = append(keys, m.key)
	}
	return keys
}

// Get returns an object's member.
func (v *Value) Get(key string) (*Value, bool) {
	if v.kind != KindObject {
		return nil, false
	}
	for _, m := range v.members {
		if m.key == key {
			return m.value, true
		}
	}
	return nil, false
}

// Set replaces an object's member in place, keeping its position and key
// bytes, or appends it last when the key is new.
func (v *Value) Set(key string, val *Value) error {
	if v.kind != KindObject {
		return fmt.Errorf("%w: set %q on a %s", ErrWrongKind, key, v.kind)
	}
	for i := range v.members {
		if v.members[i].key == key {
			v.members[i].value = val
			return nil
		}
	}
	v.members = append(v.members, member{key: key, rawKey: quote(key), value: val})
	return nil
}

// InsertAfter adds a new member to an object right after the member prevKey,
// or first when prevKey is "". Deleting a member and inserting it again after
// the member that preceded it gives back the same bytes. It refuses a key that
// is already there (ErrDuplicateKey) and a prevKey that is not
// (ErrMissingKey), changing nothing.
func (v *Value) InsertAfter(prevKey, key string, val *Value) error {
	if v.kind != KindObject {
		return fmt.Errorf("%w: insert %q into a %s", ErrWrongKind, key, v.kind)
	}
	at := 0
	found := prevKey == ""
	for i, m := range v.members {
		if m.key == key {
			return fmt.Errorf("%w: %q", ErrDuplicateKey, key)
		}
		if !found && m.key == prevKey {
			at, found = i+1, true
		}
	}
	if !found {
		return fmt.Errorf("%w: insert %q after %q", ErrMissingKey, key, prevKey)
	}
	v.members = slices.Insert(v.members, at, member{key: key, rawKey: quote(key), value: val})
	return nil
}

// Delete removes an object's member and reports whether it was there.
func (v *Value) Delete(key string) bool {
	if v.kind != KindObject {
		return false
	}
	for i, m := range v.members {
		if m.key == key {
			v.members = append(v.members[:i:i], v.members[i+1:]...)
			return true
		}
	}
	return false
}

// Len is the number of an array's items or an object's members.
func (v *Value) Len() int {
	switch v.kind {
	case KindArray:
		return len(v.items)
	case KindObject:
		return len(v.members)
	default:
		return 0
	}
}

// Index returns an array's item i; it panics when i is out of range, like a
// slice index.
func (v *Value) Index(i int) *Value { return v.items[i] }

// Append adds an item at the end of an array.
func (v *Value) Append(val *Value) error {
	if v.kind != KindArray {
		return fmt.Errorf("%w: append to a %s", ErrWrongKind, v.kind)
	}
	v.items = append(v.items, val)
	return nil
}

// Text decodes a string value.
func (v *Value) Text() (string, error) {
	if v.kind != KindString {
		return "", fmt.Errorf("%w: text of a %s", ErrWrongKind, v.kind)
	}
	var s string
	if err := json.Unmarshal(v.raw, &s); err != nil {
		return "", fmt.Errorf("%w: %w", ErrSyntax, err)
	}
	return s, nil
}

// Int decodes a number value that is an integer.
func (v *Value) Int() (int64, error) {
	if v.kind != KindNumber {
		return 0, fmt.Errorf("%w: int of a %s", ErrWrongKind, v.kind)
	}
	n, err := strconv.ParseInt(string(v.raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s is not an integer", ErrWrongKind, v.raw)
	}
	return n, nil
}

// Literal returns the exact text of a number, bool or null, such as 2.50.
func (v *Value) Literal() (string, error) {
	switch v.kind {
	case KindNumber, KindBool, KindNull:
		return string(v.raw), nil
	default:
		return "", fmt.Errorf("%w: literal of a %s", ErrWrongKind, v.kind)
	}
}

// String builds a string value escaped as JSON.stringify escapes it.
func String(s string) *Value { return &Value{kind: KindString, raw: quote(s)} }

// Int builds an integer number value.
func Int(n int64) *Value {
	return &Value{kind: KindNumber, raw: []byte(strconv.FormatInt(n, 10))}
}

// Number builds a number value from its JSON literal, kept as written.
func Number(literal string) (*Value, error) {
	v, err := ParseValue([]byte(literal))
	if err != nil || v.kind != KindNumber || string(v.raw) != literal {
		return nil, fmt.Errorf("%w: %q is not a JSON number", ErrSyntax, literal)
	}
	return v, nil
}

// Bool builds a boolean value.
func Bool(b bool) *Value {
	return &Value{kind: KindBool, raw: []byte(strconv.FormatBool(b))}
}

// Null builds a null value.
func Null() *Value { return &Value{kind: KindNull, raw: []byte("null")} }

// NewObject builds an empty object.
func NewObject() *Value { return &Value{kind: KindObject} }

// NewArray builds an empty array.
func NewArray() *Value { return &Value{kind: KindArray} }

// write writes v as JSON.stringify(v, null, 2) does, at depth levels of
// indentation.
func write(b *bytes.Buffer, v *Value, depth int) {
	switch v.kind {
	case KindObject:
		if len(v.members) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, m := range v.members {
			indent(b, depth+1)
			b.Write(m.rawKey)
			b.WriteString(": ")
			write(b, m.value, depth+1)
			if i < len(v.members)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		indent(b, depth)
		b.WriteByte('}')
	case KindArray:
		if len(v.items) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, item := range v.items {
			indent(b, depth+1)
			write(b, item, depth+1)
			if i < len(v.items)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		indent(b, depth)
		b.WriteByte(']')
	default:
		b.Write(v.raw)
	}
}

func indent(b *bytes.Buffer, depth int) {
	for range depth {
		b.WriteString("  ")
	}
}

// quote escapes s as JSON.stringify does: ", \ and the controls below U+0020
// only, the usual ones with their short escapes and the rest as \u00xx in
// lowercase. Invalid UTF-8 becomes U+FFFD.
func quote(s string) []byte {
	const hexDigits = "0123456789abcdef"
	b := make([]byte, 0, len(s)+2)
	b = append(b, '"')
	for _, r := range s { // ranging decodes invalid UTF-8 as U+FFFD
		switch r {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\b':
			b = append(b, '\\', 'b')
		case '\f':
			b = append(b, '\\', 'f')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		default:
			if r < 0x20 {
				b = append(b, '\\', 'u', '0', '0', hexDigits[r>>4], hexDigits[r&0xf])
				continue
			}
			b = utf8.AppendRune(b, r)
		}
	}
	return append(b, '"')
}

// parser reads JSON already checked by json.Valid, so it only has to split
// tokens, not report grammar errors.
type parser struct {
	data []byte
	pos  int
}

func (p *parser) skipSpace() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) value() (*Value, error) {
	if p.pos >= len(p.data) {
		return nil, fmt.Errorf("%w: unexpected end", ErrSyntax)
	}
	switch c := p.data[p.pos]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		return &Value{kind: KindString, raw: p.stringToken()}, nil
	case c == 't' || c == 'f':
		return &Value{kind: KindBool, raw: p.word()}, nil
	case c == 'n':
		return &Value{kind: KindNull, raw: p.word()}, nil
	default:
		return &Value{kind: KindNumber, raw: p.word()}, nil
	}
}

func (p *parser) object() (*Value, error) {
	v := &Value{kind: KindObject}
	p.pos++ // {
	p.skipSpace()
	if p.data[p.pos] == '}' {
		p.pos++
		return v, nil
	}
	seen := map[string]bool{}
	for {
		p.skipSpace()
		rawKey := p.stringToken()
		var key string
		if err := json.Unmarshal(rawKey, &key); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrSyntax, err)
		}
		if seen[key] {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateKey, key)
		}
		seen[key] = true
		p.skipSpace()
		p.pos++ // :
		p.skipSpace()
		val, err := p.value()
		if err != nil {
			return nil, err
		}
		v.members = append(v.members, member{key: key, rawKey: rawKey, value: val})
		p.skipSpace()
		if p.data[p.pos] == '}' {
			p.pos++
			return v, nil
		}
		p.pos++ // ,
	}
}

func (p *parser) array() (*Value, error) {
	v := &Value{kind: KindArray}
	p.pos++ // [
	p.skipSpace()
	if p.data[p.pos] == ']' {
		p.pos++
		return v, nil
	}
	for {
		p.skipSpace()
		item, err := p.value()
		if err != nil {
			return nil, err
		}
		v.items = append(v.items, item)
		p.skipSpace()
		if p.data[p.pos] == ']' {
			p.pos++
			return v, nil
		}
		p.pos++ // ,
	}
}

// stringToken returns a string token with its quotes, exactly as written.
func (p *parser) stringToken() []byte {
	start := p.pos
	p.pos++ // opening quote
	for p.data[p.pos] != '"' {
		if p.data[p.pos] == '\\' {
			p.pos++
		}
		p.pos++
	}
	p.pos++ // closing quote
	return bytes.Clone(p.data[start:p.pos])
}

// word returns a number, true, false or null token.
func (p *parser) word() []byte {
	start := p.pos
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ',', '}', ']', ' ', '\t', '\r', '\n':
			return bytes.Clone(p.data[start:p.pos])
		}
		p.pos++
	}
	return bytes.Clone(p.data[start:p.pos])
}
