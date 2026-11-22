// Package graphtest builds, inside a fake home, the skills layout of the
// machine described in docs/research/harnesses-v2.md section 4 and
// docs/research/local-inventory.md, for the tests of the graph and of Doctor.
// It only writes through the Home it is given, which tests back with
// testsupport.Home.
package graphtest

import (
	"fmt"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/treehash"
)

// Home is the part of testsupport.Home the fixture writes through. Every
// path is relative to the fake home.
type Home interface {
	WriteFile(rel, content string) string
	Symlink(rel, target string) string
	Mkdir(rel string) string
	Path(rel string) string
}

// SkillMD is a SKILL.md with a name and a description.
func SkillMD(name, description string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n# %s\n", name, description, name)
}

func gentleSkillMD(name string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: gentle\nmetadata:\n  author: gentleman-programming\n---\n", name)
}

// The four skills parked by hand in ~/.agents/skills/OFF.
var OffSkills = []string{"accessibility", "domain-driven-design", "git-guardrails-claude-code", "web-quality-audit"}

// GentleSkills are the gentle-ai copies of the fixture, and GentleDirs every
// directory holding a real copy of each.
var (
	GentleSkills = []string{"go-testing", "work-unit-commits"}
	GentleDirs   = []string{
		".agents/skills", ".claude/skills", ".codex/skills", ".config/opencode/skills",
		".gemini/skills", ".gemini/config/skills", ".gemini/antigravity-cli/skills",
	}
)

// RequirementsDirs hold the seven identical copies of requirements-elicitation,
// none of them in the store.
var RequirementsDirs = []string{
	".cursor/skills", ".copilot/skills", ".opencode/skills", ".gemini/skills",
	".gemini/antigravity/skills", ".kiro/skills", ".kilocode/skills",
}

// BrokenLinks are the 19 dangling links, all towards a skill moved to OFF/:
// Pi 2, Kiro 4, Kilocode 4, Factory 2, Qwen 2, Junie 3 and Devin 2. The
// value is the link's text.
var BrokenLinks = map[string]string{}

func init() {
	up2, up3 := "../../.agents/skills/", "../../../.agents/skills/"
	add := func(dir, up string, names ...string) {
		for _, n := range names {
			BrokenLinks[dir+"/"+n] = up + n
		}
	}
	add(".pi/agent/skills", up3, "accessibility", "web-quality-audit")
	add(".kiro/skills", up2, OffSkills...)
	add(".kilocode/skills", up2, OffSkills...)
	add(".factory/skills", up2, "accessibility", "domain-driven-design")
	add(".qwen/skills", up2, "accessibility", "web-quality-audit")
	add(".junie/skills", up2, "accessibility", "domain-driven-design", "git-guardrails-claude-code")
	add(".config/devin/skills", up3, "domain-driven-design", "web-quality-audit")
}

// KarpathyClone is the plugin clone, and KarpathySkill its nested skill.
const (
	KarpathyClone = ".claude/skills/andrej-karpathy-skills"
	KarpathySkill = KarpathyClone + "/skills/karpathy-guidelines"
)

// AgentsLocal is the overlay of this machine, as in
// internal/data/agents.local.example.toml.
const AgentsLocal = `
[[agent]]
id = "codex"
global_dir = "~/.codex/skills"
reads_universal = true

[[agent]]
id = "pi"
global_dir = "~/.pi/agent/skills"
reads_universal = false

[[agent]]
id = "kilo"
global_dir = "~/.kilocode/skills"
reads_universal = false
`

// Machine writes the layout into h and returns the lock entries of
// ~/.agents/.skill-lock.json, in its order. The caller registers
// KarpathyClone as a clean GitHub clone with its fake git.
func Machine(h Home) []domain.LockEntry {
	store := ".agents/skills/"
	lockNames := []string{"agent-browser", "archify", "find-skills", "grill-me", "grill-with-docs", "orca-cli", "orchestration"}
	for _, n := range lockNames {
		h.WriteFile(store+n+"/SKILL.md", SkillMD(n, n))
	}
	h.WriteFile(store+"i-have-adhd/SKILL.md", SkillMD("i-have-adhd", "manual"))
	// The alias: computer-use is a link to its sibling, whose frontmatter
	// name is orca-computer-use.
	h.WriteFile(store+"orca-computer-use/SKILL.md", SkillMD("orca-computer-use", "computer use"))
	h.Symlink(store+"computer-use", "orca-computer-use")
	for _, n := range OffSkills {
		h.WriteFile(store+"OFF/"+n+"/SKILL.md", SkillMD(n, n))
	}
	h.WriteFile(store+"OFF/README.md", "# OFF\n\n| Skill | Por qué está apagada | Cómo reactivarla |\n")

	// Claude Code: links to the store, the alias, an own copy, OFF/ links,
	// the claude.ai cache and a plugin clone.
	for _, n := range append(lockNames, "i-have-adhd", "computer-use") {
		h.Symlink(".claude/skills/"+n, "../../.agents/skills/"+n)
	}
	h.WriteFile(".claude/skills/humanizer/SKILL.md", SkillMD("humanizer", "own copy"))
	for _, n := range OffSkills {
		h.Symlink(".claude/skills/OFF/"+n, "../../../.agents/skills/OFF/"+n)
	}
	bucket := ".claude/skills/synced/2637_ab12/"
	h.WriteFile(bucket+".bucket-2637", "")
	h.WriteFile(bucket+"manifest.json", `{"lastUpdated":1,"skills":[]}`)
	h.WriteFile(bucket+"pdf/SKILL.md", SkillMD("pdf", "claude.ai"))
	h.WriteFile(KarpathyClone+"/.claude-plugin/plugin.json", `{"name":"andrej-karpathy-skills"}`)
	h.WriteFile(KarpathySkill+"/SKILL.md", SkillMD("karpathy-guidelines", "plugin"))
	h.Mkdir(KarpathyClone + "/.git/objects")

	// Codex: an own skill, a link, the provider's .system.
	h.WriteFile(".codex/skills/brainstorming/SKILL.md", SkillMD("brainstorming", "codex only"))
	h.WriteFile(".codex/skills/.system/skill-creator/SKILL.md", SkillMD("skill-creator", "provider"))
	for _, dir := range []string{".codex/skills", ".gemini/skills", ".cursor/skills", ".copilot/skills"} {
		h.Symlink(dir+"/i-have-adhd", "../../.agents/skills/i-have-adhd")
	}
	h.Symlink(".gemini/config/skills/orchestration", "../../../.agents/skills/orchestration")
	h.Symlink(".pi/agent/skills/find-skills", "../../../.agents/skills/find-skills")

	// gentle-ai: real copies everywhere, its _shared/ notes, and its state.
	for _, dir := range GentleDirs {
		for _, n := range GentleSkills {
			h.WriteFile(dir+"/"+n+"/SKILL.md", gentleSkillMD(n))
		}
	}
	for _, dir := range []string{".agents/skills", ".claude/skills", ".codex/skills", ".config/opencode/skills"} {
		h.WriteFile(dir+"/_shared/persona.md", "shared notes")
	}
	h.WriteFile(".gentle-ai/state.json", `{"installed_agents":["claude-code"],"components":["engram","skills","persona"]}`)

	for _, dir := range RequirementsDirs {
		h.WriteFile(dir+"/requirements-elicitation/SKILL.md", SkillMD("requirements-elicitation", "copied by hand"))
	}
	for link, target := range BrokenLinks {
		h.Symlink(link, target)
	}

	// ~/.claude-omni: divergent copies of the orca skills, identical ones
	// of the rest.
	omni := ".claude-omni/skills/"
	for _, n := range []string{"orca-cli", "orchestration"} {
		h.WriteFile(omni+n+"/SKILL.md", SkillMD(n, "older"))
	}
	h.WriteFile(omni+"computer-use/SKILL.md", SkillMD("orca-computer-use", "older"))
	for _, n := range append([]string{"find-skills", "grill-with-docs"}, OffSkills...) {
		h.WriteFile(omni+n+"/SKILL.md", SkillMD(n, n))
	}

	h.WriteFile(".config/skillhub/agents.local.toml", AgentsLocal)

	hash := func(rel string) string {
		tree, err := treehash.Dir(h.Path(rel))
		if err != nil {
			panic(fmt.Sprintf("graphtest: hash %s: %v", rel, err))
		}
		return tree.SHA
	}
	const modified = "0000000000000000000000000000000000000000"
	entry := func(name, source, folderHash string) domain.LockEntry {
		return domain.LockEntry{
			Name: name, Source: source, SourceType: "github",
			SkillPath: "skills/" + name + "/SKILL.md", SkillFolderHash: folderHash,
		}
	}
	var entries []domain.LockEntry
	for _, n := range []string{"agent-browser", "archify", "find-skills", "grill-me", "grill-with-docs"} {
		entries = append(entries, entry(n, "owner/"+n, hash(store+n)))
	}
	// Orca rewrites its own skills in the store.
	for _, n := range []string{"orca-cli", "orchestration", "computer-use"} {
		entries = append(entries, entry(n, "stablyai/orca", modified))
	}
	for _, n := range OffSkills {
		entries = append(entries, entry(n, "owner/"+n, hash(store+"OFF/"+n)))
	}
	return entries
}
