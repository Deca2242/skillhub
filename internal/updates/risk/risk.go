// Package risk computes the version-1 risk labels for an update candidate
// from its file changes, SKILL.md frontmatter delta, local modification state,
// and added lines (docs/design.md section 7).
//
// It is pure: no disk I/O, no processes, no clock, and no network. Labels
// inform the user; they never block or decide on their own.
package risk

import (
	"path"
	"strconv"
	"strings"

	"skillhub/internal/shared/domain"
)

// RulesetVersion is the version of the risk-labeling rules implemented by
// Classify. Candidates store this value so later versions can relabel history.
const RulesetVersion = 1

// AddedFileLines holds the added lines for a single changed file, used by
// Classify to evaluate text-based heuristics such as suspicious_text.
type AddedFileLines struct {
	// Path is slash-separated and relative to the skill folder, matching
	// domain.FileChange.Path.
	Path string
	// Lines holds the added lines (without a leading diff "+" prefix).
	Lines []string
}

// Input describes a candidate's changes for risk classification. U7 builds it
// from the staged diff and the local modification check.
//
// All paths in FileChanges, StagedFiles, and AddedLines must be
// slash-separated and relative to the skill folder (including for skills
// inside a git clone, so rules like scripts/ apply consistently). StagedFiles
// lists all file paths in the staged skill tree (not only changed files) so
// references to existing skill files are not flagged as bare domains. Input
// does not carry removed lines, so domain and URL checks in Classify flag any
// domain-like reference present in AddedLines rather than diffing against
// removed lines.
type Input struct {
	FileChanges      []domain.FileChange
	StagedFiles      []string
	FrontmatterDelta domain.FrontmatterDelta
	LocallyModified  bool
	AddedLines       []AddedFileLines
}

// Classify evaluates the version-1 risk rules against in and returns the
// matching labels in the canonical order of domain.Labels(). When no other
// rule triggers, Classify returns []domain.Label{domain.LabelTextOnly}.
//
// Paths in in.FileChanges, in.StagedFiles, and in.AddedLines must be relative
// to the skill folder. Because Input omits removed lines, suspicious_text
// flags any domain, URL, or IP address appearing in in.AddedLines, except
// bare filenames that match a path or basename in in.FileChanges or
// in.StagedFiles.
func Classify(in Input) []domain.Label {
	labels := make([]domain.Label, 0, len(domain.Labels()))

	if hasNonTextFiles(in.FileChanges) {
		labels = append(labels, domain.LabelNonTextFiles)
	}
	if hasScriptsTouch(in.FileChanges) {
		labels = append(labels, domain.LabelTouchesScripts)
	}
	if hasModeChange(in.FileChanges) {
		labels = append(labels, domain.LabelModeChange)
	}
	if hasNewPermissions(in.FrontmatterDelta) {
		labels = append(labels, domain.LabelNewPermissions)
	}
	if hasSuspiciousText(in.AddedLines, knownFilePaths(in.FileChanges, in.StagedFiles)) {
		labels = append(labels, domain.LabelSuspiciousText)
	}
	if in.LocallyModified {
		labels = append(labels, domain.LabelLocallyModified)
	}

	if len(labels) == 0 {
		return []domain.Label{domain.LabelTextOnly}
	}
	return labels
}

func knownFilePaths(changes []domain.FileChange, staged []string) map[string]struct{} {
	paths := make(map[string]struct{}, (len(changes)+len(staged))*2)
	addPath := func(raw string) {
		clean := strings.TrimPrefix(path.Clean(raw), "./")
		clean = strings.TrimPrefix(clean, "/")
		if clean == "" || clean == "." {
			return
		}
		paths[clean] = struct{}{}
		paths[path.Base(clean)] = struct{}{}
	}
	for _, fc := range changes {
		addPath(fc.Path)
	}
	for _, s := range staged {
		addPath(s)
	}
	return paths
}

func hasNonTextFiles(changes []domain.FileChange) bool {
	for _, fc := range changes {
		if fc.Change != domain.ChangeAdded && fc.Change != domain.ChangeModified {
			continue
		}
		if fc.NewMode == "120000" || !isTextFile(fc.Path) {
			return true
		}
	}
	return false
}

func isTextFile(filePath string) bool {
	clean := strings.TrimPrefix(path.Clean(filePath), "./")
	base := path.Base(clean)
	if base == "." || base == "/" {
		return false
	}

	if isLicenseOrNotice(base) {
		return true
	}

	ext := strings.ToLower(path.Ext(base))
	return ext == ".md" || ext == ".txt" || ext == ".rst"
}

func isLicenseOrNotice(base string) bool {
	var suffix string
	switch {
	case strings.HasPrefix(base, "LICENSE"):
		suffix = strings.TrimPrefix(base, "LICENSE")
	case strings.HasPrefix(base, "NOTICE"):
		suffix = strings.TrimPrefix(base, "NOTICE")
	default:
		return false
	}

	if suffix == "" || strings.HasPrefix(suffix, "-") {
		return true
	}
	lower := strings.ToLower(suffix)
	return lower == ".md" || lower == ".txt" || lower == ".rst"
}

func hasScriptsTouch(changes []domain.FileChange) bool {
	for _, fc := range changes {
		if isUnderScripts(fc.Path) {
			return true
		}
	}
	return false
}

func isUnderScripts(filePath string) bool {
	clean := strings.TrimPrefix(path.Clean(filePath), "./")
	clean = strings.TrimPrefix(clean, "/")
	return clean == "scripts" || strings.HasPrefix(clean, "scripts/")
}

func hasModeChange(changes []domain.FileChange) bool {
	for _, fc := range changes {
		if isModeChange(fc) {
			return true
		}
	}
	return false
}

func isModeChange(fc domain.FileChange) bool {
	if fc.Change == domain.ChangeRemoved {
		return false
	}
	if fc.Change == domain.ChangeAdded {
		return hasExecutableBit(fc.NewMode)
	}
	if fc.OldMode != "" && fc.NewMode != "" && fc.OldMode != fc.NewMode {
		return true
	}
	return fc.OldMode == "" && hasExecutableBit(fc.NewMode)
}

func hasExecutableBit(mode string) bool {
	if mode == "" {
		return false
	}
	parsed, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return false
	}
	return parsed&0o111 != 0
}

func hasNewPermissions(delta domain.FrontmatterDelta) bool {
	for _, key := range delta.Changed {
		if !isAllowedFrontmatterKey(key) {
			return true
		}
	}
	return false
}

func isAllowedFrontmatterKey(key string) bool {
	switch key {
	case "description", "version", "license":
		return true
	}
	return strings.HasPrefix(key, "metadata.")
}
