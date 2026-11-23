package text_test

import (
	"testing"

	"skillhub/internal/entrypoints/text"
)

func TestTUISearchMatch(t *testing.T) {
	cases := []struct {
		name   string
		fields []string
		want   string
	}{
		{
			name:   "empty",
			fields: nil,
			want:   "",
		},
		{
			name:   "single field",
			fields: []string{text.SearchFieldDescription},
			want:   "Coincide en: descripción",
		},
		{
			name:   "two fields",
			fields: []string{text.SearchFieldDescription, text.SearchFieldRepo},
			want:   "Coincide en: descripción y repositorio",
		},
		{
			name:   "all hidden fields",
			fields: []string{text.SearchFieldDescription, text.SearchFieldAlias, text.SearchFieldRepo, text.SearchFieldRemote},
			want:   "Coincide en: descripción, alias, repositorio y remoto",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := text.TUISearchMatch(tc.fields); got != tc.want {
				t.Errorf("TUISearchMatch(%v) = %q, want %q", tc.fields, got, tc.want)
			}
		})
	}
}
