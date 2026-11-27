package app

import (
	"errors"
	"slices"

	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/install"
	"skillhub/internal/manage/remove"
	"skillhub/internal/manage/repair"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/ports"
)

// The refusals of the version 2 operations, one kind for each thing the
// user can be told, whatever planner refused. A refusal is a *RefusalError:
// errors.Is matches both its kind and the planner's own error, so the entry
// points translate the kind once and tests can still name the cause.
var (
	ErrPlanChanged        = errors.New("the plan changed since it was shown")
	ErrNoPlan             = errors.New("the request has no plan to apply")
	ErrInvalidRequest     = errors.New("invalid request")
	ErrInvalidKey         = errors.New("invalid skill key")
	ErrNotFound           = errors.New("no skill with that key")
	ErrForeignZone        = errors.New("the skill is in a zone skillhub never touches")
	ErrAmbiguous          = errors.New("the key names more than one skill")
	ErrAlias              = errors.New("the key is an alias of another skill folder")
	ErrChanged            = errors.New("the skill changed while it was observed")
	ErrAlreadyDisabled    = errors.New("the skill is already disabled")
	ErrNotDisabled        = errors.New("the skill is not disabled")
	ErrKeyReinstalled     = errors.New("another skill with the same key was installed while it was disabled")
	ErrKeyInUse           = errors.New("the key is taken in the store")
	ErrKeyExists          = errors.New("a skill with that key already exists")
	ErrUnknownHarness     = errors.New("unknown harness")
	ErrNotAdministrable   = errors.New("the harness has no link directory skillhub can manage")
	ErrHarnessNotDetected = errors.New("the harness is not detected on this machine")
	ErrNotLinked          = errors.New("the harness has no link to the skill")
	ErrAlreadyLinked      = errors.New("the harness already links the skill")
	ErrPathTaken          = errors.New("something already exists at the path")
	ErrParkingVisible     = errors.New("the parking area is inside a directory a harness reads")
	ErrNothingToAdopt     = errors.New("no skill to adopt in OFF/")
	ErrNeighbourParked    = errors.New("a neighbour of the lock entry is disabled or gone")
	ErrBadParkedRecord    = errors.New("the disabled skill's record does not match its folder")
	ErrNameMismatch       = errors.New("the skill's name is not its store key")
	ErrPinnedRef          = errors.New("the source pins a ref and an install never writes one")
	ErrMissingDir         = errors.New("a directory the install needs does not exist")
	ErrInvalidReview      = errors.New("the revision is not a prepared install")
	ErrStagingChanged     = errors.New("the prepared copy changed since it was reviewed")
	ErrUnsupportedSource  = errors.New("unsupported install source")
	ErrNoSkill            = errors.New("no skill found at the source")
	ErrAmbiguousSkill     = errors.New("the source holds several skills")
	ErrStale              = errors.New("the finding changed since it was detected")
	ErrNotOffered         = errors.New("the repair does not apply to the finding")
	ErrNeedsCanonical     = errors.New("the canonical folder must be chosen")
	ErrBadCanonical       = errors.New("the canonical folder is not one of the skill's folders")
	ErrLockedSkill        = errors.New("a lock skill's folder is never replaced by a copy of unknown origin")
	ErrLinkedCopy         = errors.New("links lead to a folder the repair moves or removes")
	ErrNoPlacement        = errors.New("a harness that does not read the store would lose the skill")
	ErrPrecondition       = errors.New("something the plan expects changed")
	ErrForeignState       = errors.New("another program changed something during the operation")
	ErrOperationPending   = errors.New("an interrupted operation must be recovered first")
	ErrChangedSince       = errors.New("something changed since the operation")
	ErrNothingToUndo      = errors.New("nothing to undo")
	ErrUnsupportedTree    = errors.New("the folder cannot be kept in a snapshot")
	ErrLockChanged        = errors.New("the skills lock changed")
	ErrNoEntryToReinstall = errors.New("the lock has no entry with that key to reinstall")
	ErrEntryPinsRef       = errors.New("the lock entry pins a ref, which is never removed")
	ErrSourceMismatch     = errors.New("the lock entry records another source")
	ErrLinkedLinkDir      = errors.New("the link directory is below a symlink whose real path cannot be read")
	ErrCanonicalNotUsed   = errors.New("the repair does not choose a canonical folder")
	ErrRepoNotFound       = errors.New("the GitHub repository does not exist or is not visible")
	ErrRefNotFound        = errors.New("the ref or path does not exist in the GitHub repository")
	ErrRateLimited        = errors.New("the GitHub rate limit is exhausted")
	ErrNotAuthenticated   = errors.New("gh is not authenticated")
	ErrGitHubUnavailable  = errors.New("GitHub could not be read")
	ErrLockUnsupported    = errors.New("the skills lock has a version skillhub does not write")
	ErrLockMalformed      = errors.New("the skills lock is malformed")
	ErrStagingFailed      = errors.New("the install staging could not be read or emptied")
	ErrNoProject          = errors.New("no active project directory")
	ErrProjectLinked      = errors.New("project root or skill directory is a symlink")
	ErrWrongProject       = errors.New("project operation belongs to another project")
	ErrCrossDevice        = errors.New("source and destination are on different file systems")
)

// ErrCandidatesNotSuperseded is the warning of a remove that ran but could
// not mark its open update candidates superseded (A4 of L51). It is not a
// refusal: the operation ran.
var ErrCandidatesNotSuperseded = errors.New("the open update candidates could not be marked superseded")

var ()

// refusals maps each planner and engine error to its kind; the first match
// wins.
var refusals = []struct {
	kind   error
	causes []error
}{
	{ErrPlanChanged, []error{engine.ErrPlanChanged}},
	{ErrInvalidKey, []error{toggle.ErrInvalidKey, remove.ErrInvalidKey}},
	{ErrNotFound, []error{toggle.ErrNotFound, remove.ErrNotFound}},
	{ErrForeignZone, []error{toggle.ErrForeignZone, remove.ErrForeignZone, repair.ErrForeignZone}},
	{ErrAlias, []error{remove.ErrAlias}},
	{ErrAmbiguous, []error{toggle.ErrAmbiguous, remove.ErrAmbiguous}},
	{ErrChanged, []error{remove.ErrChanged}},
	{ErrAlreadyDisabled, []error{toggle.ErrAlreadyParked}},
	{ErrNotDisabled, []error{toggle.ErrNotParked}},
	{ErrKeyReinstalled, []error{toggle.ErrKeyReinstalled}},
	{ErrKeyInUse, []error{toggle.ErrKeyInUse, repair.ErrKeyInUse}},
	{ErrKeyExists, []error{install.ErrKeyExists}},
	{ErrUnknownHarness, []error{toggle.ErrUnknownHarness, install.ErrUnknownHarness}},
	{ErrNotAdministrable, []error{toggle.ErrNotAdministrable, install.ErrNotAdministrable}},
	{ErrHarnessNotDetected, []error{toggle.ErrHarnessNotDetected}},
	{ErrNotLinked, []error{toggle.ErrNotLinked}},
	{ErrAlreadyLinked, []error{toggle.ErrAlreadyLinked}},
	{ErrPathTaken, []error{toggle.ErrPathTaken, install.ErrPathTaken, repair.ErrPathTaken}},
	{ErrParkingVisible, []error{toggle.ErrParkingVisible}},
	{ErrNothingToAdopt, []error{toggle.ErrNothingToAdopt}},
	{ErrNeighbourParked, []error{toggle.ErrNeighbourParked}},
	{ErrBadParkedRecord, []error{toggle.ErrBadParkedRecord}},
	{ErrNameMismatch, []error{install.ErrNameMismatch}},
	{ErrPinnedRef, []error{install.ErrPinnedRef}},
	{ErrMissingDir, []error{install.ErrMissingDir}},
	{ErrInvalidReview, []error{install.ErrInvalidReview}},
	{ErrStagingChanged, []error{install.ErrStagingChanged}},
	{ErrUnsupportedSource, []error{install.ErrUnsupportedSource}},
	{ErrNoSkill, []error{install.ErrNoSkill}},
	{ErrAmbiguousSkill, []error{install.ErrAmbiguousSkill}},
	{ErrStale, []error{repair.ErrStale}},
	{ErrNotOffered, []error{repair.ErrNotOffered}},
	{ErrNeedsCanonical, []error{repair.ErrNeedsCanonical}},
	{ErrBadCanonical, []error{repair.ErrBadCanonical}},
	{ErrLockedSkill, []error{repair.ErrLockedSkill}},
	{ErrLinkedCopy, []error{repair.ErrLinkedCopy}},
	{ErrNoPlacement, []error{repair.ErrNoPlacement}},
	{ErrOperationPending, []error{engine.ErrOperationPending, engine.ErrV1OperationPending}},
	{ErrForeignState, []error{engine.ErrForeignState}},
	{ErrPrecondition, []error{engine.ErrPrecondition}},
	{ErrChangedSince, []error{engine.ErrChangedSince}},
	{ErrNothingToUndo, []error{engine.ErrNothingToUndo}},
	{ErrUnsupportedTree, []error{engine.ErrUnsupportedTree}},
	{ErrLockChanged, []error{ports.ErrLockChanged, ports.ErrLockMoved, ports.ErrLockEntryExists}},
	{ErrNoEntryToReinstall, []error{install.ErrNoEntryToReinstall}},
	{ErrEntryPinsRef, []error{install.ErrEntryPinsRef}},
	{ErrSourceMismatch, []error{install.ErrSourceMismatch}},
	{ErrLinkedLinkDir, []error{toggle.ErrLinkedLinkDir}},
	{ErrRepoNotFound, []error{ports.ErrRepoNotFound}},
	{ErrRefNotFound, []error{ports.ErrRefNotFound}},
	{ErrRateLimited, []error{ports.ErrRateLimited}},
	{ErrNotAuthenticated, []error{ports.ErrNotAuthenticated}},
	{ErrLockUnsupported, []error{ports.ErrUnsupportedLockVersion}},
	{ErrLockMalformed, []error{ports.ErrMalformedLock}},
	{ErrNoProject, []error{remove.ErrNoProject}},
	{ErrProjectLinked, []error{remove.ErrProjectLinked}},
	{ErrWrongProject, []error{engine.ErrWrongProject}},
	{ErrCrossDevice, []error{ports.ErrCrossDevice}},
}

// InstallChoice identifies one skill inside an ambiguous install source.
type InstallChoice struct {
	Name   string
	Folder string
	Source string
}

// RefusalError is a refusal of a version 2 operation. Kind is one of the
// Err* kinds above; Err is the planner's or the engine's error, kept whole.
// Related names what the user may act on: the disabled neighbours to enable
// first (ErrNeighbourParked) or the paths that hold the key
// (ErrKeyReinstalled, ErrKeyExists). Choices lists the selectable skills
// when Kind is ErrAmbiguousSkill.
type RefusalError struct {
	Kind    error
	Err     error
	Related []string
	Choices []InstallChoice
}

func (e *RefusalError) Error() string { return e.Err.Error() }

// Unwrap lets errors.Is and errors.As reach both the kind and the cause.
func (e *RefusalError) Unwrap() []error { return []error{e.Kind, e.Err} }

// refusal wraps err in a *RefusalError when it is a known refusal, and
// returns any other error, nil included, unchanged.
func refusal(err error) error {
	if err == nil {
		return nil
	}
	var done *RefusalError
	if errors.As(err, &done) {
		return err
	}
	for _, r := range refusals {
		for _, c := range r.causes {
			if errors.Is(err, c) {
				return &RefusalError{
					Kind:    r.kind,
					Err:     err,
					Related: related(err),
					Choices: installChoices(err),
				}
			}
		}
	}
	return err
}

// isRefusal reports whether err is a *RefusalError.
func isRefusal(err error) bool {
	var re *RefusalError
	return errors.As(err, &re)
}

func related(err error) []string {
	var np *toggle.NeighbourParkedError
	if errors.As(err, &np) {
		return append([]string{}, np.Parked...)
	}
	var kr *toggle.KeyReinstalledError
	if errors.As(err, &kr) && kr.Folder != "" {
		return []string{kr.Folder}
	}
	var ke *install.KeyExistsError
	if errors.As(err, &ke) {
		var out []string
		for _, p := range []string{ke.Store, ke.Parked} {
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	var wpe *engine.WrongProjectError
	if errors.As(err, &wpe) && wpe.OpProjectDir != "" {
		return []string{wpe.OpProjectDir}
	}
	return nil
}

func installChoices(err error) []InstallChoice {
	var ae *install.AmbiguousSkillError
	if !errors.As(err, &ae) || len(ae.Choices) == 0 {
		return nil
	}
	out := make([]InstallChoice, len(ae.Choices))
	for i, c := range ae.Choices {
		out[i] = InstallChoice{Name: c.Name, Folder: c.Folder, Source: c.Source}
	}
	return out
}

// InstallChoices returns the selectable skills of an ambiguous install
// refusal, or nil when err carries none.
func InstallChoices(err error) []InstallChoice {
	var re *RefusalError
	if errors.As(err, &re) && len(re.Choices) > 0 {
		return slices.Clone(re.Choices)
	}
	return installChoices(err)
}
