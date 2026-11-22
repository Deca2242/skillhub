package install_test

import (
	"errors"
	"testing"

	"skillhub/internal/manage/install"
	"skillhub/internal/testsupport"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

func TestParseAcceptsTheGitHubForms(t *testing.T) {
	tests := []struct {
		in   string
		want install.Source
	}{
		{"owner/repo", install.Source{Repo: "owner/repo"}},
		{"  owner/repo  ", install.Source{Repo: "owner/repo"}},
		{"owner/repo/", install.Source{Repo: "owner/repo"}},
		{"Owner-1/repo.name_x", install.Source{Repo: "Owner-1/repo.name_x"}},
		{"owner/repo@my-skill", install.Source{Repo: "owner/repo", Skill: "my-skill"}},
		{"owner/repo@My Skill", install.Source{Repo: "owner/repo", Skill: "My Skill"}},
		// What follows @ that looks like a semver or a commit SHA pins a
		// ref, as npx skills reads it (skills-cli-internals.md, fact 5).
		{"owner/repo@v1.2.3", install.Source{Repo: "owner/repo", Ref: "v1.2.3"}},
		{"owner/repo@1.2", install.Source{Repo: "owner/repo", Ref: "1.2"}},
		{"owner/repo@0123456789abcdef0123456789abcdef01234567", install.Source{Repo: "owner/repo", Ref: "0123456789abcdef0123456789abcdef01234567"}},
		{"owner/repo/skills/alpha", install.Source{Repo: "owner/repo", Subpath: "skills/alpha"}},
		{"owner/repo/skills/alpha/", install.Source{Repo: "owner/repo", Subpath: "skills/alpha"}},
		{"https://github.com/owner/repo", install.Source{Repo: "owner/repo"}},
		{"https://github.com/owner/repo.git", install.Source{Repo: "owner/repo"}},
		{"https://www.github.com/owner/repo/", install.Source{Repo: "owner/repo"}},
		{"http://github.com/owner/repo", install.Source{Repo: "owner/repo"}},
		{"github.com/owner/repo", install.Source{Repo: "owner/repo"}},
		{"https://github.com/owner/repo/tree/main", install.Source{Repo: "owner/repo", Tree: "main"}},
		{"https://github.com/owner/repo/tree/main/skills/alpha", install.Source{Repo: "owner/repo", Tree: "main/skills/alpha"}},
		{"https://github.com/owner/repo/tree/feature/x/skills/alpha/", install.Source{Repo: "owner/repo", Tree: "feature/x/skills/alpha"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := install.Parse(tt.in)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseRejectsEveryOtherSourceWithATypedError(t *testing.T) {
	tests := []struct {
		in   string
		kind install.SourceKind
	}{
		{"https://gitlab.com/group/repo", install.KindGitLab},
		{"https://gitlab.com/group/sub/repo/-/tree/main/skills", install.KindGitLab},
		{"https://git.example.com/group/repo/-/tree/main/skills", install.KindGitLab},
		{"gitlab:group/repo", install.KindGitLab},
		{"git@github.com:owner/repo.git", install.KindSSH},
		{"git@gitlab.com:owner/repo.git", install.KindSSH},
		{"ssh://git@github.com/owner/repo.git", install.KindSSH},
		{"/home/user/skills/alpha", install.KindLocal},
		{"./alpha", install.KindLocal},
		{"../alpha", install.KindLocal},
		{".", install.KindLocal},
		{"~/skills/alpha", install.KindLocal},
		{"ftp://github.com/owner/repo", install.KindURL},
		{"https://github.com/owner/repo/blob/main/SKILL.md", install.KindMalformed},
		{"https://github.com/owner", install.KindMalformed},
		{"https://github.com/owner/repo/tree/", install.KindMalformed},
		{"https://github.com/owner/repo?tab=readme", install.KindMalformed},
		{"https://github.com/owner/repo#main", install.KindMalformed},
		{"https://github.com/owner/repo/tree/main/../x", install.KindMalformed},
		{"", install.KindMalformed},
		{"owner", install.KindMalformed},
		{"owner/", install.KindMalformed},
		{"owner/repo@", install.KindMalformed},
		{"own er/repo", install.KindMalformed},
		{"owner/re po", install.KindMalformed},
		{"owner/repo/../escape", install.KindMalformed},
		{"owner/repo/a//b", install.KindMalformed},
		{`owner/repo/a\b`, install.KindMalformed},
		{"owner/repo/.git/hooks", install.KindMalformed},
		{"github:owner/repo", install.KindMalformed},
		{"owner/repo#main", install.KindMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			_, err := install.Parse(tt.in)
			if !errors.Is(err, install.ErrUnsupportedSource) {
				t.Fatalf("Parse(%q) err = %v, want ErrUnsupportedSource", tt.in, err)
			}
			var se *install.SourceError
			if !errors.As(err, &se) {
				t.Fatalf("Parse(%q) err = %T, want *SourceError", tt.in, err)
			}
			if se.Kind != tt.kind || se.Input != tt.in {
				t.Errorf("Parse(%q) = kind %s input %q, want kind %s", tt.in, se.Kind, se.Input, tt.kind)
			}
		})
	}
}
