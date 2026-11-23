package text_test

import (
	"strings"
	"testing"

	"skillhub/internal/entrypoints/text"
)

func TestCompareTextsAreSpanishAndNonEmpty(t *testing.T) {
	if !strings.Contains(text.TUIKeysAgentFilter, "m ") {
		t.Errorf("TUIKeysAgentFilter %q lacks compare key m", text.TUIKeysAgentFilter)
	}
	if !strings.Contains(text.TUIKeysCompareAgents, "esc") {
		t.Errorf("TUIKeysCompareAgents %q lacks esc", text.TUIKeysCompareAgents)
	}
	if got := text.TUICompareTitle("Claude Code", "Codex"); !strings.Contains(got, "Claude Code") || !strings.Contains(got, "Codex") {
		t.Errorf("TUICompareTitle = %q, want both agent names", got)
	}
	if got := text.TUICompareSection("En común", 2); got != "En común (2)" {
		t.Errorf("TUICompareSection = %q, want %q", got, "En común (2)")
	}
	if text.TUICompareEmptyList == "" || text.TUICompareSingleAgent == "" || text.TUICompareSameAgent == "" {
		t.Error("compare empty/single/same messages must not be empty")
	}
	if got := text.TUICompareFirstMarked("Claude Code"); !strings.Contains(got, "Claude Code") {
		t.Errorf("TUICompareFirstMarked = %q, want agent name", got)
	}
}

func TestSkillCompareTextsSanitizeAndFormat(t *testing.T) {
	for _, s := range []string{
		text.TUIKeyCompareSkills,
		text.TUIActionCompare,
		text.TUIKeysCompareSkills,
		text.TUIKeysSkillDetailCompare,
	} {
		if s == "" {
			t.Fatal("skill compare text constant must not be empty")
		}
	}
	if !strings.Contains(text.TUIKeysSkillDetailCompare, text.TUIKeysSkillDetail) {
		t.Errorf("TUIKeysSkillDetailCompare %q should preserve TUIKeysSkillDetail %q", text.TUIKeysSkillDetailCompare, text.TUIKeysSkillDetail)
	}

	gotTitle := text.TUISkillCompareTitle("alpha\x1b[31m")
	if strings.Contains(gotTitle, "\x1b[31m") {
		t.Errorf("TUISkillCompareTitle did not escape ANSI: %q", gotTitle)
	}
	if !strings.Contains(gotTitle, `alpha\x1b[31m`) {
		t.Errorf("TUISkillCompareTitle = %q, want escaped name", gotTitle)
	}
}
