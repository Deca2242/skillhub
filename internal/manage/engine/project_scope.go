package engine

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ProjectRoot identifies one of the two approved project-scoped skill roots.
type ProjectRoot string

const (
	ProjectRootAgents ProjectRoot = ".agents/skills"
	ProjectRootClaude ProjectRoot = ".claude/skills"
)

// ErrProjectScopeUnsupported reports a valid project-scoped plan entering an
// engine execution, check, undo, or recovery boundary before scoped
// filesystem operations are enabled.
var ErrProjectScopeUnsupported = errors.New("project-scoped operations are not supported")

// ErrWrongProject reports an operation bound to a different startup project
// directory than the current invocation.
var ErrWrongProject = errors.New("project-scoped operation belongs to another project")

// WrongProjectError identifies the project that owns an operation and the
// project directory from which the caller attempted to act.
type WrongProjectError struct {
	OpProjectDir      string
	CurrentProjectDir string
}

func (e *WrongProjectError) Error() string {
	if e.CurrentProjectDir == "" {
		return fmt.Sprintf("%s: belongs to project %s", ErrWrongProject, e.OpProjectDir)
	}
	return fmt.Sprintf("%s: belongs to %s, current project is %s", ErrWrongProject, e.OpProjectDir, e.CurrentProjectDir)
}

func (e *WrongProjectError) Unwrap() error { return ErrWrongProject }

// ProjectScope binds a plan to an explicit startup project directory and one
// exact direct skill copy under an approved project root.
type ProjectScope struct {
	ProjectDir string      `json:"project_dir"`
	Root       ProjectRoot `json:"root"`
	SkillName  string      `json:"skill_name"`
}

func (r ProjectRoot) valid() bool {
	return r == ProjectRootAgents || r == ProjectRootClaude
}

// Validate checks that s names an absolute clean startup project directory, one
// of the two approved project roots, and a single direct skill directory name.
func (s ProjectScope) Validate() error {
	if !validProjectDir(s.ProjectDir) {
		return fmt.Errorf("invalid project directory %q", s.ProjectDir)
	}
	if !s.Root.valid() {
		return fmt.Errorf("unsupported project root %q", s.Root)
	}
	if !validProjectSkillName(s.SkillName) {
		return fmt.Errorf("invalid project skill name %q", s.SkillName)
	}
	return nil
}

// SkillPath returns the exact expected skill directory path for s.
func (s ProjectScope) SkillPath() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	return filepath.Join(s.ProjectDir, filepath.FromSlash(string(s.Root)), s.SkillName), nil
}

// HasProjectScope reports whether p carries an opt-in project binding.
func (p Plan) HasProjectScope() bool {
	return p.Project != nil
}

// ValidateProjectScope validates p's opt-in project binding when present and
// checks that the plan consists solely of the matching single tree step.
// Legacy plans without a project binding return nil.
func (p Plan) ValidateProjectScope() error {
	if p.Project == nil {
		return nil
	}
	wantPath, err := p.Project.SkillPath()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidStep, err)
	}
	if p.TouchesLock || p.LockSHA != "" {
		return fmt.Errorf("%w: project-scoped plan must not touch the global lock", ErrInvalidStep)
	}
	if !p.Effect.IsZero() {
		return fmt.Errorf("%w: project-scoped plan must not mutate global state", ErrInvalidStep)
	}
	if len(p.Steps) != 1 {
		return fmt.Errorf("%w: project-scoped plan must have 1 step, got %d", ErrInvalidStep, len(p.Steps))
	}
	s := p.Steps[0]
	if err := s.shape(); err != nil {
		return err
	}
	if err := s.semantics(); err != nil {
		return err
	}
	wantKind := StepRemoveTree
	if p.Undoes != "" {
		if !validSnapshotID(p.Undoes) {
			return fmt.Errorf("%w: undoes %q", ErrInvalidStep, p.Undoes)
		}
		wantKind = StepRestoreTree
	}
	if s.Kind != wantKind {
		return fmt.Errorf("%w: project-scoped plan step is %s, want %s", ErrInvalidStep, s.Kind, wantKind)
	}
	if s.Path != wantPath {
		return fmt.Errorf("%w: project-scoped step path %q does not match %q", ErrInvalidStep, s.Path, wantPath)
	}
	return nil
}

func (e *Engine) requireProjectScopeGate(scope ProjectScope, op string) error {
	wantPath, err := scope.SkillPath()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidStep, err)
	}
	if e == nil || e.d.ProjectFS == nil {
		return fmt.Errorf("%w: %s for %s", ErrProjectScopeUnsupported, op, wantPath)
	}
	cur := ""
	if validProjectDir(e.d.ProjectDir) {
		cur = e.d.ProjectDir
	} else if e.d.ProjectDir != "" {
		if clean := filepath.Clean(e.d.ProjectDir); validProjectDir(clean) {
			cur = clean
		}
	}
	if cur != scope.ProjectDir {
		return &WrongProjectError{OpProjectDir: scope.ProjectDir, CurrentProjectDir: cur}
	}
	return nil
}

func (e *Engine) requireProjectGate(p Plan, op string) error {
	if p.Project == nil {
		return nil
	}
	if err := p.ValidateProjectScope(); err != nil {
		return err
	}
	return e.requireProjectScopeGate(*p.Project, op)
}

func (p Plan) verifyAndCheckProjectScope(e *Engine, op string) error {
	if p.Project == nil {
		return nil
	}
	if err := p.verifyID(); err != nil {
		return err
	}
	return e.requireProjectGate(p, op)
}

func validProjectDir(dir string) bool {
	return validPath(dir) && !strings.ContainsRune(dir, 0)
}

func validProjectSkillName(name string) bool {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) {
		return false
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) || strings.ContainsRune(name, 0) {
		return false
	}
	return true
}
