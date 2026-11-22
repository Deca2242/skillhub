package ports

import "context"

// CatalogSkill is one skill the public skills.sh directory lists.
type CatalogSkill struct {
	// ID is skills.sh's own id, owner/repo/skill for a GitHub source.
	ID string
	// Source is where the skill lives: owner/repo for a GitHub repository,
	// or a host such as mintlify.com for a well-known source.
	Source string
	// SkillID is the skill's folder name inside Source; install takes
	// Source@SkillID.
	SkillID string
	// Name is the display name, which may differ from SkillID.
	Name string
	// Installs is how many times skills.sh counted it installed.
	Installs int
}

// HTTPSFetcher reads one https URL. It only reads: well-known skill sources
// (RFC 8615, /.well-known/agent-skills/) are fetched through it, by running
// curl, since no package here opens network connections itself.
type HTTPSFetcher interface {
	// Get returns the body of url, which must be https, refusing a body
	// larger than maxBytes and any redirect.
	Get(ctx context.Context, url string, maxBytes int64) ([]byte, error)
}

// Catalog searches the public skills.sh directory. It only reads: it never
// installs anything.
type Catalog interface {
	// Search returns at most limit skills matching query, as skills.sh
	// ranks them. query has at least two characters.
	Search(ctx context.Context, query string, limit int) ([]CatalogSkill, error)
}
