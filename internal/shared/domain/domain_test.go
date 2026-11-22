package domain_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/testsupport"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

func TestSkillIDsNameTheirOrigin(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"a lock entry is named by its lock name", domain.LockID("grill-me"), "lock:grill-me"},
		{"a skill inside a clone is named by its home-relative folder", domain.GitID(".claude/skills/karpathy"), "git:.claude/skills/karpathy"},
		{"a folder without origin is named by its home-relative folder", domain.DirID(".claude/skills/humanizer"), "dir:.claude/skills/humanizer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("id = %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestSettingsDefaultsAndMinimumInterval(t *testing.T) {
	def := domain.DefaultSettings()
	if def.CheckInterval != time.Hour || !def.Notifications || def.Ignore == nil || len(def.Ignore) != 0 {
		t.Fatalf("DefaultSettings() = %+v, want 1h, notifications on, an empty ignore list", def)
	}
	if err := def.Validate(); err != nil {
		t.Fatalf("the default settings are invalid: %v", err)
	}
	tests := []struct {
		name     string
		interval time.Duration
		wantErr  error
	}{
		{"exactly the minimum is accepted", 15 * time.Minute, nil},
		{"two hours is accepted", 2 * time.Hour, nil},
		{"one minute under the minimum is rejected", 14 * time.Minute, domain.ErrIntervalTooShort},
		{"zero is rejected", 0, domain.ErrIntervalTooShort},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := domain.DefaultSettings()
			s.CheckInterval = tt.interval
			if err := s.Validate(); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// enumCase lists one enum: every value the design names, in its order, and
// the values the package reports.
type enumCase struct {
	name   string
	want   []string
	got    []string
	parse  func(string) error
	sample string
}

func enumCases() []enumCase {
	return []enumCase{
		{
			name:  "State",
			want:  []string{"up_to_date", "update_available", "unchecked", "check_failed", "no_origin", "disabled", "missing"},
			got:   strs(domain.States()),
			parse: func(s string) error { var v domain.State; return v.UnmarshalText([]byte(s)) },
		},
		{
			name:  "Issue",
			want:  []string{"locally_modified", "duplicate", "divergent_copy", "broken_link", "untracked_branch", "interrupted_update", "removed_upstream"},
			got:   strs(domain.Issues()),
			parse: func(s string) error { var v domain.Issue; return v.UnmarshalText([]byte(s)) },
		},
		{
			name:  "Label",
			want:  []string{"non_text_files", "touches_scripts", "mode_change", "new_permissions", "suspicious_text", "locally_modified", "text_only"},
			got:   strs(domain.Labels()),
			parse: func(s string) error { var v domain.Label; return v.UnmarshalText([]byte(s)) },
		},
		{
			name:  "CandidateState",
			want:  []string{"pending", "applied", "skipped", "superseded", "blocked", "failed"},
			got:   strs(domain.CandidateStates()),
			parse: func(s string) error { var v domain.CandidateState; return v.UnmarshalText([]byte(s)) },
		},
		{
			name:  "OriginKind",
			want:  []string{"lock", "git_clone", "none"},
			got:   strs(domain.OriginKinds()),
			parse: func(s string) error { var v domain.OriginKind; return v.UnmarshalText([]byte(s)) },
		},
		{
			name:  "LocationKind",
			want:  []string{"real", "symlink", "broken_link"},
			got:   strs(domain.LocationKinds()),
			parse: func(s string) error { var v domain.LocationKind; return v.UnmarshalText([]byte(s)) },
		},
		{
			name:  "OperationKind",
			want:  []string{"apply", "undo"},
			got:   strs(domain.OperationKinds()),
			parse: func(s string) error { var v domain.OperationKind; return v.UnmarshalText([]byte(s)) },
		},
		{
			name:  "HistoryAction",
			want:  []string{"applied", "undone", "skipped", "failed", "recovered"},
			got:   strs(domain.HistoryActions()),
			parse: func(s string) error { var v domain.HistoryAction; return v.UnmarshalText([]byte(s)) },
		},
		{
			name:  "SnapshotCause",
			want:  []string{"apply", "undo"},
			got:   strs(domain.SnapshotCauses()),
			parse: func(s string) error { var v domain.SnapshotCause; return v.UnmarshalText([]byte(s)) },
		},
		{
			name:  "BlockReason",
			want:  []string{"dirty_worktree", "local_commits", "hash_mismatch", "unsupported_entry", "limits_exceeded", "git_attributes"},
			got:   strs(domain.BlockReasons()),
			parse: func(s string) error { var v domain.BlockReason; return v.UnmarshalText([]byte(s)) },
		},
		{
			name:  "FileChangeKind",
			want:  []string{"added", "modified", "removed"},
			got:   strs(domain.FileChangeKinds()),
			parse: func(s string) error { var v domain.FileChangeKind; return v.UnmarshalText([]byte(s)) },
		},
	}
}

func strs[T ~string](values []T) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, string(v))
	}
	return out
}

func TestEveryEnumListsExactlyTheDesignValues(t *testing.T) {
	for _, tc := range enumCases() {
		t.Run(tc.name, func(t *testing.T) {
			if !slices.Equal(tc.got, tc.want) {
				t.Fatalf("values = %q, want %q", tc.got, tc.want)
			}
			for _, v := range tc.want {
				if err := tc.parse(v); err != nil {
					t.Errorf("UnmarshalText(%q) = %v, want nil", v, err)
				}
			}
			for _, bad := range []string{"", "unknown", strings.ToUpper(tc.want[0])} {
				if err := tc.parse(bad); !errors.Is(err, domain.ErrUnknownValue) {
					t.Errorf("UnmarshalText(%q) = %v, want ErrUnknownValue", bad, err)
				}
			}
		})
	}
}

func TestPersistedRecordsUseSnakeCaseKeysAndStringEnums(t *testing.T) {
	at := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	c := domain.UpdateCandidate{
		ID:                  "c1",
		Target:              domain.LockID("grill-me"),
		Skills:              []string{domain.LockID("grill-me")},
		FromSHA:             "a",
		InstalledSHAAtStage: "b",
		ToCommit:            "c",
		ToSHA:               "d",
		StagedPath:          "/staging/c1",
		FileChanges:         []domain.FileChange{{Path: "SKILL.md", Change: domain.ChangeModified, OldMode: "100644", NewMode: "100755", OldSHA: "x", NewSHA: "y"}},
		FrontmatterDelta:    domain.FrontmatterDelta{Changed: []string{"description"}},
		Labels:              []domain.Label{domain.LabelModeChange},
		RulesetVersion:      1,
		FirstSeenAt:         at,
		State:               domain.CandidateBlocked,
		BlockReason:         domain.BlockHashMismatch,
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"id"`, `"target"`, `"skills"`, `"from_sha"`, `"installed_sha_at_stage"`, `"to_commit"`, `"to_sha"`,
		`"staged_path"`, `"file_changes"`, `"frontmatter_delta"`, `"labels":["mode_change"]`, `"ruleset_version"`,
		`"first_seen_at"`, `"state":"blocked"`, `"block_reason":"hash_mismatch"`, `"change":"modified"`,
	} {
		if !strings.Contains(string(data), key) {
			t.Errorf("marshaled candidate lacks %s: %s", key, data)
		}
	}
	var back domain.UpdateCandidate
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.State != c.State || back.BlockReason != c.BlockReason || !back.FirstSeenAt.Equal(at) || back.FileChanges[0] != c.FileChanges[0] {
		t.Fatalf("round trip = %+v, want %+v", back, c)
	}

	bad := strings.Replace(string(data), `"state":"blocked"`, `"state":"exploded"`, 1)
	if err := json.Unmarshal([]byte(bad), &back); !errors.Is(err, domain.ErrUnknownValue) {
		t.Fatalf("Unmarshal of an unknown state = %v, want ErrUnknownValue", err)
	}
}
