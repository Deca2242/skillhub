// Package packs manages draft skill packs: named collections of exact skill
// identities stored in Skillhub's own state without modifying skill folders,
// links, or the skills lock.
package packs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrInvalidPackName reports a pack name that is empty or contains
	// disallowed characters such as path separators or control runes.
	ErrInvalidPackName = errors.New("invalid pack name")
	// ErrPackExists reports creating a pack whose name is already taken.
	ErrPackExists = errors.New("pack already exists")
	// ErrPackNotFound reports a pack name that matches no draft pack.
	ErrPackNotFound = errors.New("pack not found")
	// ErrMemberExists reports adding a skill identity already in the pack.
	ErrMemberExists = errors.New("skill is already a member of the pack")
	// ErrMemberNotFound reports removing a skill identity not in the pack.
	ErrMemberNotFound = errors.New("skill is not a member of the pack")
	// ErrCorruptSidecar reports a persisted packs sidecar that violates identity invariants.
	ErrCorruptSidecar = errors.New("corrupt packs sidecar")
	// ErrUnsupportedSidecarVersion reports a packs sidecar with an unsupported version.
	ErrUnsupportedSidecarVersion = errors.New("unsupported packs version")
)

// Pack is a draft collection of exact skill identities.
type Pack struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Members     []string  `json:"members"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

// Store persists draft packs under the shared state lock.
type Store interface {
	Lock(ctx context.Context) (func() error, error)
	LoadPacks() ([]Pack, error)
	SavePacks(packs []Pack) error
}

// Service manages draft packs over Store.
type Service struct {
	store Store
	now   func() time.Time
}

// New builds a Service over store. A nil now defaults to time.Now.
func New(store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}
}

// Create creates a new empty draft pack with name and description.
func (s *Service) Create(ctx context.Context, name, description string) (p Pack, err error) {
	if err := validateName(name); err != nil {
		return Pack{}, err
	}

	release, err := s.store.Lock(ctx)
	if err != nil {
		return Pack{}, fmt.Errorf("lock packs: %w", err)
	}
	defer func() {
		err = errors.Join(err, release())
	}()

	all, err := s.store.LoadPacks()
	if err != nil {
		return Pack{}, fmt.Errorf("load packs: %w", err)
	}
	for _, existing := range all {
		if existing.Name == name {
			return Pack{}, fmt.Errorf("%q: %w", name, ErrPackExists)
		}
	}

	ts := s.now().UTC()
	p = Pack{
		Name:        name,
		Description: description,
		Members:     []string{},
		CreatedAt:   ts,
		UpdatedAt:   ts,
	}
	all = append(all, clonePack(p))
	sortPacks(all)
	if err := s.store.SavePacks(all); err != nil {
		return Pack{}, fmt.Errorf("save packs: %w", err)
	}
	return clonePack(p), nil
}

// List returns all draft packs sorted by name.
func (s *Service) List() ([]Pack, error) {
	all, err := s.store.LoadPacks()
	if err != nil {
		return nil, fmt.Errorf("load packs: %w", err)
	}
	out := make([]Pack, len(all))
	for i, p := range all {
		out[i] = clonePack(p)
	}
	sortPacks(out)
	return out, nil
}

// Get returns the draft pack named name.
func (s *Service) Get(name string) (Pack, error) {
	all, err := s.store.LoadPacks()
	if err != nil {
		return Pack{}, fmt.Errorf("load packs: %w", err)
	}
	for _, p := range all {
		if p.Name == name {
			return clonePack(p), nil
		}
	}
	return Pack{}, fmt.Errorf("%q: %w", name, ErrPackNotFound)
}

// AddMember adds the exact skill identity memberID to packName.
func (s *Service) AddMember(ctx context.Context, packName, memberID string) (p Pack, err error) {
	if memberID == "" {
		return Pack{}, fmt.Errorf("%w: empty skill id", ErrMemberNotFound)
	}

	release, err := s.store.Lock(ctx)
	if err != nil {
		return Pack{}, fmt.Errorf("lock packs: %w", err)
	}
	defer func() {
		err = errors.Join(err, release())
	}()

	all, err := s.store.LoadPacks()
	if err != nil {
		return Pack{}, fmt.Errorf("load packs: %w", err)
	}
	idx := slices.IndexFunc(all, func(item Pack) bool { return item.Name == packName })
	if idx < 0 {
		return Pack{}, fmt.Errorf("%q: %w", packName, ErrPackNotFound)
	}

	target := clonePack(all[idx])
	if slices.Contains(target.Members, memberID) {
		return Pack{}, fmt.Errorf("%q in %q: %w", memberID, packName, ErrMemberExists)
	}
	target.Members = append(target.Members, memberID)
	slices.Sort(target.Members)
	target.UpdatedAt = s.now().UTC()
	all[idx] = clonePack(target)
	sortPacks(all)

	if err := s.store.SavePacks(all); err != nil {
		return Pack{}, fmt.Errorf("save packs: %w", err)
	}
	return clonePack(target), nil
}

// RemoveMember removes the exact skill identity memberID from packName.
func (s *Service) RemoveMember(ctx context.Context, packName, memberID string) (p Pack, err error) {
	release, err := s.store.Lock(ctx)
	if err != nil {
		return Pack{}, fmt.Errorf("lock packs: %w", err)
	}
	defer func() {
		err = errors.Join(err, release())
	}()

	all, err := s.store.LoadPacks()
	if err != nil {
		return Pack{}, fmt.Errorf("load packs: %w", err)
	}
	idx := slices.IndexFunc(all, func(item Pack) bool { return item.Name == packName })
	if idx < 0 {
		return Pack{}, fmt.Errorf("%q: %w", packName, ErrPackNotFound)
	}

	target := clonePack(all[idx])
	mIdx := slices.Index(target.Members, memberID)
	if mIdx < 0 {
		return Pack{}, fmt.Errorf("%q in %q: %w", memberID, packName, ErrMemberNotFound)
	}
	target.Members = slices.Delete(target.Members, mIdx, mIdx+1)
	target.UpdatedAt = s.now().UTC()
	all[idx] = clonePack(target)
	sortPacks(all)

	if err := s.store.SavePacks(all); err != nil {
		return Pack{}, fmt.Errorf("save packs: %w", err)
	}
	return clonePack(target), nil
}

// ForSkill returns all draft packs that include skillID, sorted by name.
func (s *Service) ForSkill(skillID string) ([]Pack, error) {
	if skillID == "" {
		return []Pack{}, nil
	}
	all, err := s.store.LoadPacks()
	if err != nil {
		return nil, fmt.Errorf("load packs: %w", err)
	}
	out := []Pack{}
	for _, p := range all {
		if slices.Contains(p.Members, skillID) {
			out = append(out, clonePack(p))
		}
	}
	sortPacks(out)
	return out, nil
}

func validateName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed != name {
		return ErrInvalidPackName
	}
	if !utf8.ValidString(name) {
		return ErrInvalidPackName
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '/' || r == '\\' || isBidiControl(r) {
			return ErrInvalidPackName
		}
	}
	return nil
}

func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x200E || r == 0x200F
}

func clonePack(p Pack) Pack {
	members := slices.Clone(p.Members)
	if members == nil {
		members = []string{}
	}
	p.Members = members
	return p
}

func sortPacks(in []Pack) {
	slices.SortFunc(in, func(a, b Pack) int {
		return cmp.Compare(a.Name, b.Name)
	})
}
