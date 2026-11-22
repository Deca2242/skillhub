package install

import (
	"bytes"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"

	"skillhub/internal/shared/treehash"
)

// Discovery follows npx skills 1.9.0, which skillhub must stay
// interoperable with (docs/design-v2.md section 11, point 2;
// docs/research/skills-cli-internals.md; remove.mjs as cited below):
//
//   - a search root holding SKILL.md is the only skill (discoverSkills,
//     remove.mjs:2695-2725);
//   - otherwise findSkillMdPaths (remove.mjs:4684-4740): the root and its
//     children, then the children of each container and, below a container,
//     skills two or three levels down that have no skill above them and no
//     skipped directory in their path;
//   - when no container yields a skill, every SKILL.md at most six path
//     elements deep.
//
// Deliberate differences, all towards refusing rather than guessing:
//   - npx's containers are skills/, skills/.curated/, skills/.experimental/,
//     skills/.system/ and the project skills directory of every agent it
//     knows (AGENT_PROJECT_SKILL_DIRS, remove.mjs:2125, such as
//     .claude/skills or .posit/assistant/skills). That list changes with
//     each release, so here a container is any directory named "skills" at
//     most three levels deep, which covers it: an extra candidate can only
//     turn a pick into an ambiguity error;
//   - the fallback also ignores skipped directories such as node_modules;
//   - the file name is exactly SKILL.md, which is what harnesses load;
//   - two skills with one name are an ambiguity, where npx keeps the first
//     it walks (remove.mjs:2741).

// skipDirs are never walked into below a container (remove.mjs:2587).
var skipDirs = []string{"node_modules", ".git", "dist", "build", "__pycache__"}

const (
	skillFile        = "SKILL.md"
	maxContainerSize = 3 // path elements of a container such as .posit/assistant/skills
	maxFallbackDepth = 6 // path elements of a SKILL.md found by the fallback
)

// candidates returns the folders of the skills in nodes, a tree relative to
// the search root, "" for the root itself, sorted.
func candidates(nodes []treehash.Node) []string {
	var mds []string
	containers := []string{}
	for _, n := range nodes {
		switch {
		case n.IsTree() && path.Base(n.Path) == "skills" && strings.Count(n.Path, "/")+1 <= maxContainerSize:
			containers = append(containers, n.Path)
		case n.Mode == treehash.ModeFile || n.Mode == treehash.ModeExecutable:
			if path.Base(n.Path) == skillFile {
				mds = append(mds, n.Path)
			}
		}
	}
	if slices.Contains(mds, skillFile) {
		return []string{""}
	}
	isSkill := func(dir string) bool { return slices.Contains(mds, dir+"/"+skillFile) }
	// A "skills" folder inside a skill is part of that skill, not a
	// container.
	containers = slices.DeleteFunc(containers, func(c string) bool {
		for d := c; d != "."; d = path.Dir(d) {
			if isSkill(d) {
				return true
			}
		}
		return false
	})
	found := map[string]bool{}
	for _, md := range mds {
		dir := path.Dir(md)
		if !strings.Contains(dir, "/") { // a child of the root
			found[dir] = true
			continue
		}
		for _, c := range containers {
			rest, ok := strings.CutPrefix(dir, c+"/")
			if !ok {
				continue
			}
			parts := strings.Split(rest, "/")
			if len(parts) == 1 || len(parts) <= 3 && !hasSkipped(parts) && !hasSkillAbove(c, parts, isSkill) {
				found[dir] = true
			}
		}
	}
	if len(found) == 0 {
		for _, md := range mds {
			if parts := strings.Split(md, "/"); len(parts) <= maxFallbackDepth && !hasSkipped(parts) {
				found[path.Dir(md)] = true
			}
		}
	}
	out := make([]string, 0, len(found))
	for dir := range found {
		out = append(out, dir)
	}
	slices.Sort(out)
	return out
}

func hasSkipped(parts []string) bool {
	return slices.ContainsFunc(parts, func(p string) bool { return slices.Contains(skipDirs, p) })
}

// hasSkillAbove reports whether a folder between container c and the skill
// parts names is itself a skill.
func hasSkillAbove(c string, parts []string, isSkill func(string) bool) bool {
	for i := 1; i < len(parts); i++ {
		if isSkill(c + "/" + strings.Join(parts[:i], "/")) {
			return true
		}
	}
	return false
}

// frontmatter is what npx skills reads from a SKILL.md (parseSkillMd,
// remove.mjs:2626-2670).
type frontmatter struct {
	name string
	ok   bool
}

// parseSkillMD returns the name of a SKILL.md, with ok false when npx skills
// would skip it: no header, a header that does not parse, a name or a
// description that is missing, empty or not a string, or a skill marked
// metadata.internal. A name holding a control character is skipped too:
// npx strips terminal escapes, and skillhub refuses them instead.
func parseSkillMD(data []byte) frontmatter {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	rest, found := bytes.CutPrefix(data, []byte("---\n"))
	if !found {
		return frontmatter{}
	}
	header, _, found := bytes.Cut(rest, []byte("\n---"))
	if !found {
		return frontmatter{}
	}
	var fields map[string]any
	if err := yaml.Unmarshal(header, &fields); err != nil {
		return frontmatter{}
	}
	name, okName := fields["name"].(string)
	desc, okDesc := fields["description"].(string)
	name, desc = strings.TrimSpace(name), strings.TrimSpace(desc)
	if !okName || !okDesc || name == "" || desc == "" || strings.ContainsFunc(name, unicode.IsControl) {
		return frontmatter{}
	}
	if meta, ok := fields["metadata"].(map[string]any); ok {
		switch meta["internal"] {
		case true, "true", "1":
			return frontmatter{}
		}
	}
	return frontmatter{name: name, ok: true}
}

// matchesFilter is filterSkills of npx skills (remove.mjs:2782-2790): the
// name equals the filter ignoring case or, failing that for every skill,
// after normalizeSkillName.
func matchesFilter(names []string, filter string) []int {
	var exact, normalized []int
	for i, n := range names {
		if strings.EqualFold(n, filter) {
			exact = append(exact, i)
		}
		if normalizeSkillName(n) == normalizeSkillName(filter) {
			normalized = append(normalized, i)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return normalized
}

var spacesRE = regexp.MustCompile(`[\s_]+`)

// normalizeSkillName is normalizeSkillName of npx skills
// (remove.mjs:2594-2596).
func normalizeSkillName(name string) string {
	return spacesRE.ReplaceAllString(strings.ToLower(name), "-")
}

var (
	unsafeKeyRE = regexp.MustCompile(`[^a-z0-9._]+`)
	keyEdgesRE  = regexp.MustCompile(`^[.\-]+|[.\-]+$`)
)

// StoreKey is the folder name npx skills gives a skill in the store,
// ~/.agents/skills/<key>: sanitizeName of its frontmatter name
// (remove.mjs:955-957, used at :2903). The lock entry itself is keyed by the
// name as written (recordGlobalLock, remove.mjs:6635-6690), which is
// Resolved.Name.
func StoreKey(name string) string {
	key := unsafeKeyRE.ReplaceAllString(strings.ToLower(name), "-")
	key = keyEdgesRE.ReplaceAllString(key, "")
	if len(key) > 255 {
		key = key[:255]
	}
	if key == "" {
		return "unnamed-skill"
	}
	return key
}
