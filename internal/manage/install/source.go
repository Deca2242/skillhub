// Package install resolves a GitHub source to one skill folder at one
// commit, or a website that publishes /.well-known/agent-skills/ to one
// skill (wellknown.go; S14, L84), and prepares it for review
// (docs/design-v2.md section 7). Preparing writes only below its staging root.
// Planner.Install plans publishing a review to the store, the links and the
// lock, and writes nothing itself: running it is engine.Apply.
package install

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrUnsupportedSource is the error every *SourceError matches.
var ErrUnsupportedSource = errors.New("unsupported install source")

// SourceKind says why a source is refused.
type SourceKind string

const (
	// KindGitLab is a GitLab repository, by URL or with the gitlab: prefix.
	KindGitLab SourceKind = "gitlab"
	// KindSSH is an SSH remote such as git@host:owner/repo.
	KindSSH SourceKind = "ssh"
	// KindLocal is a path on this machine.
	KindLocal SourceKind = "local"
	// KindURL is a URL skillhub does not install from: a git host other
	// than github.com (raw.githubusercontent.com, huggingface.co), a URL
	// ending in .git, or a scheme other than https.
	KindURL SourceKind = "url"
	// KindInsecure is an http:// URL of a website: a well-known source is
	// read over https only.
	KindInsecure SourceKind = "insecure"
	// KindMalformed is anything else that is not one of the accepted
	// GitHub forms.
	KindMalformed SourceKind = "malformed"
)

// SourceError is a refused install source.
type SourceError struct {
	Input string
	Kind  SourceKind
}

func (e *SourceError) Error() string {
	return fmt.Sprintf("%s: %s source %q", ErrUnsupportedSource, e.Kind, e.Input)
}

// Is makes every SourceError match ErrUnsupportedSource.
func (e *SourceError) Is(target error) bool { return target == ErrUnsupportedSource }

// Source is a parsed install source: a GitHub one (Repo is set) or a
// website that publishes /.well-known/agent-skills/ (Site is set; S14, L84).
type Source struct {
	// Site is the website's https URL as the user typed it, without the
	// @skill suffix: the lock entry's sourceBaseUrl, as npx skills add
	// <url> records it. "" for GitHub.
	Site string
	// Repo is owner/name.
	Repo string
	// Ref is the branch, tag or commit the user pinned; "" follows the
	// default branch.
	Ref string
	// Subpath is the folder of the repository to search, "" for its root.
	Subpath string
	// Skill selects one skill by its frontmatter name (owner/repo@skill),
	// or, for a website, by its index name (https://host@skill).
	Skill string
	// Tree is what follows /tree/ in a GitHub URL: a ref and an optional
	// path. A ref may hold slashes, so Resolve splits it against the
	// repository; Ref and Subpath stay empty until then.
	Tree string
}

var (
	ownerRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$`)
	repoRE  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	// versionPinRE is isVersionPin of npx skills 1.9.0 (remove.mjs:770).
	versionPinRE = regexp.MustCompile(`^v?\d+(?:\.\d+)+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$|^[0-9a-fA-F]{40}$`)
)

// Parse reads one of the four accepted forms, as npx skills 1.9.0 reads
// them (parseSource, remove.mjs:789-927; harnesses-v2.md section 1):
//
//	owner/repo
//	owner/repo@skill            (a semver or a 40-hex SHA after @ pins a ref)
//	owner/repo/subpath
//	https://github.com/owner/repo/tree/<ref>[/<path>]
//
// plus https://github.com/owner/repo[.git], and a website that publishes
// /.well-known/agent-skills/ (parseSite):
//
//	https://host[/path][@skill]
//
// GitLab, SSH, local paths, an http:// website and any other URL are a
// *SourceError.
func Parse(input string) (Source, error) {
	s := strings.TrimSpace(input)
	fail := func(kind SourceKind) (Source, error) {
		return Source{}, &SourceError{Input: input, Kind: kind}
	}
	switch {
	case s == "":
		return fail(KindMalformed)
	case s == "." || s == ".." || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../") || strings.HasPrefix(s, "~"):
		return fail(KindLocal)
	case strings.HasPrefix(s, "gitlab:"):
		return fail(KindGitLab)
	case strings.HasPrefix(s, "git@") || strings.HasPrefix(s, "ssh://"):
		return fail(KindSSH)
	case strings.HasPrefix(s, "github.com/"):
		s = "https://" + s
	}
	if scheme, rest, ok := strings.Cut(s, "://"); ok {
		return parseURL(s, scheme, rest, fail)
	}
	if strings.ContainsAny(s, ":#") {
		return fail(KindMalformed)
	}
	return parseShorthand(s, fail)
}

func parseURL(s, scheme, rest string, fail func(SourceKind) (Source, error)) (Source, error) {
	host, p, _ := strings.Cut(rest, "/")
	host = strings.ToLower(host)
	switch {
	case host == "gitlab.com" || strings.Contains(p, "/-/tree/"):
		return fail(KindGitLab)
	case strings.EqualFold(scheme, "http") && host != "github.com" && host != "www.github.com":
		return fail(KindInsecure)
	case scheme != "https" && scheme != "http":
		return fail(KindURL)
	case host != "github.com" && host != "www.github.com":
		return parseSite(s, fail)
	case strings.ContainsAny(p, "?#\\"):
		return fail(KindMalformed)
	}
	parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
	if len(parts) < 2 {
		return fail(KindMalformed)
	}
	src := Source{Repo: parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")}
	if !validRepo(src.Repo) {
		return fail(KindMalformed)
	}
	switch {
	case len(parts) == 2:
		return src, nil
	case parts[2] != "tree" || len(parts) == 3:
		return fail(KindMalformed)
	}
	src.Tree = strings.Join(parts[3:], "/")
	if !validPath(src.Tree) {
		return fail(KindMalformed)
	}
	return src, nil
}

func parseShorthand(s string, fail func(SourceKind) (Source, error)) (Source, error) {
	owner, rest, _ := strings.Cut(s, "/")
	if repo, skill, ok := strings.Cut(rest, "@"); ok && !strings.Contains(repo, "/") {
		src := Source{Repo: owner + "/" + repo}
		switch {
		case !validRepo(src.Repo) || strings.TrimSpace(skill) == "":
			return fail(KindMalformed)
		case versionPinRE.MatchString(skill):
			src.Ref = skill
		default:
			src.Skill = skill
		}
		return src, nil
	}
	repo, sub, _ := strings.Cut(strings.TrimSuffix(rest, "/"), "/")
	src := Source{Repo: owner + "/" + repo, Subpath: sub}
	if !validRepo(src.Repo) || sub != "" && !validPath(sub) {
		return fail(KindMalformed)
	}
	return src, nil
}

func validRepo(repo string) bool {
	owner, name, _ := strings.Cut(repo, "/")
	return ownerRE.MatchString(owner) && repoRE.MatchString(name) && name != "." && name != ".."
}

// validPath accepts a slash path of plain names: no "", ".", "..", ".git",
// backslash or control character.
func validPath(p string) bool {
	for elem := range strings.SplitSeq(p, "/") {
		if elem == "" || elem == "." || elem == ".." || strings.EqualFold(elem, ".git") ||
			strings.ContainsFunc(elem, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' }) {
			return false
		}
	}
	return true
}
