package graphtest

// StowedLinks are the links of the Stowed fixture, home-relative through the
// symlinked ~/.claude, with the text each one holds.
var StowedLinks = struct {
	// Good reaches the store folder find-skills, relative to the real link
	// directory as npx skills writes it.
	Good, GoodText string
	// Broken points, the same way, at the store folder gone, which was
	// parked in OFF/.
	Broken, BrokenText string
	// Chain reaches the store link computer-use, which leads to
	// orca-computer-use.
	Chain, ChainText string
	// Sibling is a link to Good, beside it in the real link directory.
	Sibling, SiblingText string
	// Outside leaves the known roots towards ~/projects/elsewhere.
	Outside, OutsideText string
}{
	Good: ".claude/skills/find-skills", GoodText: "../../../../.agents/skills/find-skills",
	Broken: ".claude/skills/gone", BrokenText: "../../../../.agents/skills/gone",
	Chain: ".claude/skills/computer-use", ChainText: "../../../../.agents/skills/computer-use",
	Sibling: ".claude/skills/finder", SiblingText: "find-skills",
	Outside: ".claude/skills/elsewhere", OutsideText: "../../../../projects/elsewhere",
}

// Stowed builds a home whose ~/.claude is a relative symlink to
// ~/dotfiles/a/claude, the way dotfiles managed with stow look, with the
// links of StowedLinks written in the real directory
// ~/dotfiles/a/claude/skills. Lexically, from ~/.claude/skills, each "../"
// text climbs out of the home; the kernel resolves it from the real
// directory.
func Stowed(h Home) {
	const store = ".agents/skills/"
	h.WriteFile(store+"find-skills/SKILL.md", SkillMD("find-skills", "find skills"))
	h.WriteFile(store+"orca-computer-use/SKILL.md", SkillMD("orca-computer-use", "computer use"))
	h.Symlink(store+"computer-use", "orca-computer-use")
	h.WriteFile(store+"OFF/gone/SKILL.md", SkillMD("gone", "parked by hand"))
	h.WriteFile("projects/elsewhere/SKILL.md", SkillMD("elsewhere", "foreign folder"))

	h.Mkdir("dotfiles/a/claude/skills")
	h.Symlink(".claude", "dotfiles/a/claude")
	l := StowedLinks
	for rel, text := range map[string]string{
		l.Good: l.GoodText, l.Broken: l.BrokenText, l.Chain: l.ChainText,
		l.Sibling: l.SiblingText, l.Outside: l.OutsideText,
	} {
		h.Symlink("dotfiles/a/claude/skills/"+rel[len(".claude/skills/"):], text)
	}
}
