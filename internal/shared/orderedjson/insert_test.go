package orderedjson_test

import (
	"errors"
	"slices"
	"testing"

	"skillhub/internal/shared/orderedjson"
)

// threeSkills is an object whose members a test takes out and puts back.
const threeSkills = `{
  "skills": {
    "a": {
      "n": 1
    },
    "b": "two",
    "c": [
      3
    ]
  },
  "tail": true
}
`

func TestInsertAfterPutsTheMemberRightAfterItsNeighbour(t *testing.T) {
	tests := []struct {
		name     string
		prevKey  string
		wantKeys []string
	}{
		{"first, after no key", "", []string{"new", "a", "b", "c"}},
		{"in the middle", "a", []string{"a", "new", "b", "c"}},
		{"last, after the last key", "c", []string{"a", "b", "c", "new"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := mustParse(t, threeSkills)
			skills := mustGet(t, doc.Root(), "skills")

			if err := skills.InsertAfter(tt.prevKey, "new", orderedjson.Int(7)); err != nil {
				t.Fatalf("InsertAfter: %v", err)
			}
			if got := skills.Keys(); !slices.Equal(got, tt.wantKeys) {
				t.Errorf("keys = %v, want %v", got, tt.wantKeys)
			}
			if got, _ := mustGet(t, skills, "new").Literal(); got != "7" {
				t.Errorf("new = %s, want 7", got)
			}
		})
	}
}

func TestDeleteThenInsertAfterTheSameNeighbourIsByteIdentical(t *testing.T) {
	for _, key := range []string{"a", "b", "c"} {
		t.Run(key, func(t *testing.T) {
			doc := mustParse(t, threeSkills)
			skills := mustGet(t, doc.Root(), "skills")
			keys := skills.Keys()
			i := slices.Index(keys, key)
			prev := ""
			if i > 0 {
				prev = keys[i-1]
			}
			saved := mustGet(t, skills, key).Bytes()

			skills.Delete(key)
			v, err := orderedjson.ParseValue(saved)
			if err != nil {
				t.Fatalf("ParseValue: %v", err)
			}
			if err := skills.InsertAfter(prev, key, v); err != nil {
				t.Fatalf("InsertAfter: %v", err)
			}
			if got := string(doc.Bytes()); got != threeSkills {
				t.Errorf("bytes changed:\n%s\nwant:\n%s", got, threeSkills)
			}
		})
	}
}

func TestInsertAfterRefusesWithoutChangingTheObject(t *testing.T) {
	tests := []struct {
		name    string
		target  func(doc *orderedjson.Document) *orderedjson.Value
		prevKey string
		key     string
		want    error
	}{
		{
			name:    "a key already there",
			target:  func(doc *orderedjson.Document) *orderedjson.Value { v, _ := doc.Root().Get("skills"); return v },
			prevKey: "a", key: "c", want: orderedjson.ErrDuplicateKey,
		},
		{
			name:    "a neighbour that is not there",
			target:  func(doc *orderedjson.Document) *orderedjson.Value { v, _ := doc.Root().Get("skills"); return v },
			prevKey: "gone", key: "new", want: orderedjson.ErrMissingKey,
		},
		{
			name: "a value that is not an object",
			target: func(doc *orderedjson.Document) *orderedjson.Value {
				v, _ := doc.Root().Get("skills")
				c, _ := v.Get("c")
				return c
			},
			prevKey: "", key: "new", want: orderedjson.ErrWrongKind,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := mustParse(t, threeSkills)

			err := tt.target(doc).InsertAfter(tt.prevKey, tt.key, orderedjson.Null())
			if !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
			if got := string(doc.Bytes()); got != threeSkills {
				t.Errorf("a refused insert changed the document:\n%s", got)
			}
		})
	}
}
