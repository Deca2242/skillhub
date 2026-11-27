package packs_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"skillhub/internal/packs"
	"skillhub/internal/testsupport"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

type memStore struct {
	mu    sync.Mutex
	packs []packs.Pack
}

func (m *memStore) Lock(context.Context) (func() error, error) {
	m.mu.Lock()
	return func() error {
		m.mu.Unlock()
		return nil
	}, nil
}

func (m *memStore) LoadPacks() ([]packs.Pack, error) {
	out := make([]packs.Pack, len(m.packs))
	for i, p := range m.packs {
		p.Members = slices.Clone(p.Members)
		out[i] = p
	}
	return out, nil
}

func (m *memStore) SavePacks(in []packs.Pack) error {
	out := make([]packs.Pack, len(in))
	for i, p := range in {
		p.Members = slices.Clone(p.Members)
		out[i] = p
	}
	m.packs = out
	return nil
}

func TestServiceCreateAndList(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	svc := packs.New(&memStore{}, func() time.Time { return now })
	ctx := context.Background()

	p, err := svc.Create(ctx, "seguridad", "Skills de auditoría")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.Name != "seguridad" || p.Description != "Skills de auditoría" || len(p.Members) != 0 {
		t.Fatalf("unexpected pack: %+v", p)
	}

	if _, err := svc.Create(ctx, "seguridad", "otra"); !errors.Is(err, packs.ErrPackExists) {
		t.Fatalf("duplicate Create error = %v, want ErrPackExists", err)
	}

	for _, bad := range []string{"", "   ", "a/b", "a\nb", "bad\x1bname"} {
		if _, err := svc.Create(ctx, bad, ""); !errors.Is(err, packs.ErrInvalidPackName) {
			t.Errorf("Create(%q) error = %v, want ErrInvalidPackName", bad, err)
		}
	}

	if _, err := svc.Create(ctx, "alpha", ""); err != nil {
		t.Fatalf("Create(alpha): %v", err)
	}
	list, err := svc.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Name != "alpha" || list[1].Name != "seguridad" {
		t.Fatalf("List = %+v, want [alpha, seguridad]", list)
	}
}

func TestServiceMembersAddRemoveAndGet(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	svc := packs.New(&memStore{}, func() time.Time { return now })
	ctx := context.Background()

	if _, err := svc.Create(ctx, "core", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.AddMember(ctx, "core", "lock:beta"); err != nil {
		t.Fatalf("AddMember(beta): %v", err)
	}
	if _, err := svc.AddMember(ctx, "core", "lock:alpha"); err != nil {
		t.Fatalf("AddMember(alpha): %v", err)
	}
	if _, err := svc.AddMember(ctx, "core", "lock:alpha"); !errors.Is(err, packs.ErrMemberExists) {
		t.Fatalf("duplicate AddMember error = %v, want ErrMemberExists", err)
	}

	got, err := svc.Get("core")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	wantMembers := []string{"lock:alpha", "lock:beta"}
	if !slices.Equal(got.Members, wantMembers) {
		t.Fatalf("Members = %v, want %v", got.Members, wantMembers)
	}

	if _, err := svc.RemoveMember(ctx, "core", "lock:alpha"); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if _, err := svc.RemoveMember(ctx, "core", "lock:alpha"); !errors.Is(err, packs.ErrMemberNotFound) {
		t.Fatalf("missing RemoveMember error = %v, want ErrMemberNotFound", err)
	}
	if _, err := svc.Get("nope"); !errors.Is(err, packs.ErrPackNotFound) {
		t.Fatalf("Get(nope) error = %v, want ErrPackNotFound", err)
	}
}
