package app

import (
	"context"
	"errors"
	"fmt"

	"skillhub/internal/manage/install"
	"skillhub/internal/staging"
)

// The refusals of a website install (S14, L84): a site that publishes
// /.well-known/agent-skills/ is resolved by install.WebResolver through the
// Web port, then prepared, reviewed, planned and undone like a GitHub skill.
var (
	// ErrSiteUnavailable reports a website that could not be read: no
	// network, no curl, an HTTP error on a file the index lists.
	ErrSiteUnavailable = errors.New("the website could not be read")
	// ErrNoWellKnown reports a website with no valid index at
	// /.well-known/agent-skills/ or /.well-known/skills/.
	ErrNoWellKnown = errors.New("the website publishes no well-known skills index")
	// ErrWebTooLarge reports a website skill beyond the download limits.
	ErrWebTooLarge = errors.New("the website's skill exceeds the download limits")
	// ErrUnsafeWebFile reports a file the index names that skillhub will not
	// download or write: an unsafe path, another host or outside the site.
	ErrUnsafeWebFile = errors.New("the website's index names a file skillhub will not write")
	// ErrWebDigestMismatch reports a skill whose content is not the digest
	// its index publishes.
	ErrWebDigestMismatch = errors.New("the website's skill does not match its digest")
	// ErrWebArchive reports a skill published as an archive, which skillhub
	// does not unpack.
	ErrWebArchive = errors.New("the website publishes the skill as an archive")
)

// webRefusals maps the errors of a website install to their kinds; the
// first match wins.
var webRefusals = []struct {
	kind   error
	causes []error
}{
	{ErrWebTooLarge, []error{install.ErrWebLimits, staging.ErrLimitsExceeded}},
	{ErrWebDigestMismatch, []error{install.ErrDigestMismatch}},
	{ErrWebArchive, []error{install.ErrUnsupportedArtifact}},
	{ErrUnsafeWebFile, []error{install.ErrUnsafeFile, staging.ErrUnsupportedEntry}},
	{ErrNoWellKnown, []error{install.ErrNoIndex}},
}

// resolveWeb resolves a website source. Every failure is a refusal: the
// known ones by kind, and any other, the website not read, is
// ErrSiteUnavailable. A canceled context stays what it is.
func (a *App) resolveWeb(ctx context.Context, src install.Source) (install.Resolved, error) {
	res, err := install.WebResolver{Web: a.d.Web}.Resolve(ctx, src)
	if err == nil {
		return res, nil
	}
	if r := webRefusal(err); isRefusal(r) {
		return install.Resolved{}, r
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return install.Resolved{}, err
	}
	return install.Resolved{}, &RefusalError{Kind: ErrSiteUnavailable, Err: err}
}

// webRefusal is refusal, then the website kinds; any other error is
// returned unchanged.
func webRefusal(err error) error {
	if r := refusal(err); isRefusal(r) {
		return r
	}
	for _, r := range webRefusals {
		for _, c := range r.causes {
			if errors.Is(err, c) {
				return &RefusalError{Kind: r.kind, Err: err}
			}
		}
	}
	return err
}

// missingWeb is the error of a website install without the Web port.
func missingWeb() error {
	return fmt.Errorf("%w: Web", ErrMissingDependency)
}
