package engine_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

func legacyReferencePlan() engine.Plan {
	p := engine.Plan{
		Meta: engine.Meta{
			Action:          "remove",
			Targets:         []string{"claude-code"},
			Canonical:       "/home/user/.agents/skills/alpha",
			Source:          "owner/alpha",
			ReviewedTreeSHA: "1111111111111111111111111111111111111111",
		},
		LockSHA:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TouchesLock: true,
		Steps: []engine.Step{
			{
				Kind: engine.StepRemoveTree,
				Path: "/home/user/.agents/skills/alpha",
				Pre: []engine.Image{
					{
						Path:    "/home/user/.agents/skills/alpha",
						State:   engine.StateTree,
						TreeSHA: "1111111111111111111111111111111111111111",
					},
				},
				Post: []engine.Image{
					{
						Path:  "/home/user/.agents/skills/alpha",
						State: engine.StateAbsent,
					},
				},
			},
			{
				Kind: engine.StepLockRemove,
				Name: "alpha",
				Raw:  []byte(`{"source":"owner/alpha"}`),
				Pre: []engine.Image{
					{
						Entry: "alpha",
						State: engine.StateEntry,
						Raw:   []byte(`{"source":"owner/alpha"}`),
					},
				},
				Post: []engine.Image{
					{
						Entry: "alpha",
						State: engine.StateAbsent,
					},
				},
			},
		},
		Effect: engine.Effect{
			DropManaged: []string{"alpha"},
		},
	}
	return p
}

func TestLegacyPlanIDAndJSONPreservedByteForByteWhenProjectAbsent(t *testing.T) {
	const wantJSON = `{"id":"","action":"remove","targets":["claude-code"],"canonical":"/home/user/.agents/skills/alpha","source":"owner/alpha","reviewed_tree_sha":"1111111111111111111111111111111111111111","lock_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","touches_lock":true,"steps":[{"kind":"removeTree","path":"/home/user/.agents/skills/alpha","pre":[{"path":"/home/user/.agents/skills/alpha","state":"tree","tree_sha":"1111111111111111111111111111111111111111"}],"post":[{"path":"/home/user/.agents/skills/alpha","state":"absent"}]},{"kind":"lockRemove","name":"alpha","raw":"eyJzb3VyY2UiOiJvd25lci9hbHBoYSJ9","pre":[{"entry":"alpha","state":"entry","raw":"eyJzb3VyY2UiOiJvd25lci9hbHBoYSJ9"}],"post":[{"entry":"alpha","state":"absent"}]}],"effect":{"drop_managed":["alpha"]}}`
	const wantID = "ebd83c11e7f1b24ecfc86de7125b7bf8567e63b61414ca82f1bb654ee8cfa86f"

	p := legacyReferencePlan()
	gotJSON, err := json.Marshal(p)
	must(t, err)
	if string(gotJSON) != wantJSON {
		t.Fatalf("legacy JSON changed:\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
	if bytes.Contains(gotJSON, []byte(`"project"`)) {
		t.Fatalf("legacy JSON unexpectedly contains project field: %s", gotJSON)
	}
	gotID, err := p.ComputeID()
	must(t, err)
	if gotID != wantID {
		t.Fatalf("legacy PlanID = %s, want %s", gotID, wantID)
	}
}

func validProjectRemovalPlan(t *testing.T, projectDir string, root engine.ProjectRoot, skillName, treeSHA string) engine.Plan {
	t.Helper()
	skillPath := filepath.Join(projectDir, filepath.FromSlash(string(root)), skillName)
	p := engine.Plan{
		Meta: engine.Meta{
			Action:  "remove",
			Targets: []string{},
			Project: &engine.ProjectScope{
				ProjectDir: projectDir,
				Root:       root,
				SkillName:  skillName,
			},
		},
		Steps: []engine.Step{
			{
				Kind: engine.StepRemoveTree,
				Path: skillPath,
				Pre: []engine.Image{
					{
						Path:    skillPath,
						State:   engine.StateTree,
						TreeSHA: treeSHA,
					},
				},
				Post: []engine.Image{
					{
						Path:  skillPath,
						State: engine.StateAbsent,
					},
				},
			},
		},
	}
	id, err := p.ComputeID()
	must(t, err)
	p.ID = id
	return p
}

func validProjectUndoPlan(t *testing.T, projectDir string, root engine.ProjectRoot, skillName, snapDir, treeSHA, undoes string) engine.Plan {
	t.Helper()
	skillPath := filepath.Join(projectDir, filepath.FromSlash(string(root)), skillName)
	p := engine.Plan{
		Meta: engine.Meta{
			Action:  "undo",
			Targets: []string{},
			Project: &engine.ProjectScope{
				ProjectDir: projectDir,
				Root:       root,
				SkillName:  skillName,
			},
		},
		Steps: []engine.Step{
			{
				Kind: engine.StepRestoreTree,
				Path: skillPath,
				Src:  filepath.Join(snapDir, "trees", "0"),
				Pre: []engine.Image{
					{
						Path:  skillPath,
						State: engine.StateAbsent,
					},
				},
				Post: []engine.Image{
					{
						Path:    skillPath,
						State:   engine.StateTree,
						TreeSHA: treeSHA,
					},
				},
			},
		},
		Undoes: undoes,
	}
	id, err := p.ComputeID()
	must(t, err)
	p.ID = id
	return p
}

func TestProjectScopeAffectsPlanIDAndSurvivesJSONRoundTrip(t *testing.T) {
	projDir := filepath.Join(t.TempDir(), "workspace")
	const treeSHA = "1111111111111111111111111111111111111111"

	base := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
	withoutProject := base
	withoutProject.Project = nil
	withoutID, err := withoutProject.ComputeID()
	must(t, err)
	if withoutID == base.ID {
		t.Fatal("adding ProjectScope kept the same PlanID")
	}

	variants := map[string]func(*engine.Plan){
		"project dir": func(p *engine.Plan) {
			cp := *p.Project
			cp.ProjectDir = projDir + "-other"
			p.Project = &cp
		},
		"project root": func(p *engine.Plan) {
			cp := *p.Project
			cp.Root = engine.ProjectRootClaude
			p.Project = &cp
		},
		"skill name": func(p *engine.Plan) {
			cp := *p.Project
			cp.SkillName = "beta"
			p.Project = &cp
		},
	}
	for name, edit := range variants {
		t.Run("changes id on "+name, func(t *testing.T) {
			p := clonePlan(t, base)
			edit(&p)
			id, err := p.ComputeID()
			must(t, err)
			if id == base.ID {
				t.Errorf("changing %s did not change PlanID", name)
			}
		})
	}

	t.Run("round trip plan record and snapshot JSON", func(t *testing.T) {
		data, err := json.Marshal(base)
		must(t, err)
		var decoded engine.Plan
		must(t, json.Unmarshal(data, &decoded))
		if !reflect.DeepEqual(decoded, base) {
			t.Fatalf("decoded plan = %+v, want %+v", decoded, base)
		}
		gotID, err := decoded.ComputeID()
		must(t, err)
		if gotID != base.ID {
			t.Fatalf("round-tripped ComputeID = %s, want %s", gotID, base.ID)
		}

		rec := engine.Record{
			Format:        "skillhub.operation",
			Version:       2,
			PlanID:        base.ID,
			Plan:          base,
			SnapshotID:    "op-test1",
			SnapshotSHA:   strings.Repeat("a", 64),
			StartedAt:     time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC),
			LastCompleted: -1,
		}
		recBytes, err := json.Marshal(rec)
		must(t, err)
		var recOut engine.Record
		must(t, json.Unmarshal(recBytes, &recOut))
		if !reflect.DeepEqual(recOut.Plan.Project, base.Project) {
			t.Fatalf("record round-trip Project = %+v, want %+v", recOut.Plan.Project, base.Project)
		}

		snap := engine.Snapshot{
			Format:     "skillhub.snapshot",
			Version:    2,
			SnapshotID: "op-test1",
			PlanID:     base.ID,
			TakenAt:    time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC),
			Plan:       base,
		}
		snapBytes, err := json.Marshal(snap)
		must(t, err)
		var snapOut engine.Snapshot
		must(t, json.Unmarshal(snapBytes, &snapOut))
		if !reflect.DeepEqual(snapOut.Plan.Project, base.Project) {
			t.Fatalf("snapshot round-trip Project = %+v, want %+v", snapOut.Plan.Project, base.Project)
		}
	})
}

func TestProjectScopeValidationRejectsInvalidBindingsAndMismatchedSteps(t *testing.T) {
	projDir := filepath.Join(t.TempDir(), "project")
	snapDir := filepath.Join(t.TempDir(), "snapshots", "op-prev")
	const treeSHA = "1111111111111111111111111111111111111111"

	valid := []struct {
		name string
		plan engine.Plan
	}{
		{"agents removal", validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)},
		{"claude removal", validProjectRemovalPlan(t, projDir, engine.ProjectRootClaude, "alpha", treeSHA)},
		{"agents undo", validProjectUndoPlan(t, projDir, engine.ProjectRootAgents, "alpha", snapDir, treeSHA, "op-prev")},
		{"claude undo", validProjectUndoPlan(t, projDir, engine.ProjectRootClaude, "alpha", snapDir, treeSHA, "op-prev")},
	}
	for _, tt := range valid {
		t.Run("valid "+tt.name, func(t *testing.T) {
			if err := tt.plan.ValidateProjectScope(); err != nil {
				t.Fatalf("ValidateProjectScope() = %v, want nil", err)
			}
			if !tt.plan.HasProjectScope() {
				t.Fatal("HasProjectScope() = false, want true")
			}
		})
	}

	invalid := []struct {
		name  string
		build func() engine.Plan
	}{
		{"empty project dir", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.ProjectDir = ""
			return p
		}},
		{"relative project dir", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.ProjectDir = "relative/project"
			return p
		}},
		{"unclean project dir with trailing slash", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.ProjectDir = projDir + "/"
			return p
		}},
		{"unclean project dir with traversal", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.ProjectDir = projDir + "/../project"
			return p
		}},
		{"root project dir", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.ProjectDir = "/"
			return p
		}},
		{"invalid utf8 project dir", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.ProjectDir = projDir + "\xff"
			return p
		}},
		{"nul byte project dir", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.ProjectDir = projDir + "\x00x"
			return p
		}},
		{"empty root", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.Root = ""
			return p
		}},
		{"unsupported root", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.Root = ".codex/skills"
			return p
		}},
		{"root with trailing slash", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.Root = ".agents/skills/"
			return p
		}},
		{"empty skill name", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.SkillName = ""
			return p
		}},
		{"dot skill name", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.SkillName = "."
			return p
		}},
		{"dotdot skill name", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.SkillName = ".."
			return p
		}},
		{"nested skill name", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.SkillName = "group/alpha"
			return p
		}},
		{"invalid utf8 skill name", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.SkillName = "alpha\xff"
			return p
		}},
		{"nul byte skill name", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Project.SkillName = "alpha\x00x"
			return p
		}},
		{"zero steps", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Steps = []engine.Step{}
			return p
		}},
		{"multiple steps", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			other := filepath.Join(projDir, ".claude", "skills", "beta")
			p.Steps = append(p.Steps, engine.Step{
				Kind: engine.StepRemoveTree,
				Path: other,
				Pre:  []engine.Image{{Path: other, State: engine.StateTree, TreeSHA: treeSHA}},
				Post: []engine.Image{{Path: other, State: engine.StateAbsent}},
			})
			return p
		}},
		{"mismatched path sibling skill", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			other := filepath.Join(projDir, ".agents", "skills", "beta")
			p.Steps[0].Path = other
			p.Steps[0].Pre[0].Path = other
			p.Steps[0].Post[0].Path = other
			return p
		}},
		{"mismatched path wrong root", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			other := filepath.Join(projDir, ".claude", "skills", "alpha")
			p.Steps[0].Path = other
			p.Steps[0].Pre[0].Path = other
			p.Steps[0].Post[0].Path = other
			return p
		}},
		{"mismatched path arbitrary outside directory", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			other := filepath.Join(projDir, "elsewhere", "alpha")
			p.Steps[0].Path = other
			p.Steps[0].Pre[0].Path = other
			p.Steps[0].Post[0].Path = other
			return p
		}},
		{"removeClone rejected", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			skillPath := p.Steps[0].Path
			cloneSHA := strings.Repeat("a", 64)
			p.Steps[0] = engine.Step{
				Kind: engine.StepRemoveClone,
				Path: skillPath,
				Pre:  []engine.Image{{Path: skillPath, State: engine.StateClone, CloneSHA: cloneSHA}},
				Post: []engine.Image{{Path: skillPath, State: engine.StateAbsent}},
			}
			return p
		}},
		{"single lockRemove step rejected", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			raw := []byte(`{"source":"owner/alpha"}`)
			p.Steps[0] = engine.Step{
				Kind: engine.StepLockRemove,
				Name: "alpha",
				Raw:  raw,
				Pre:  []engine.Image{{Entry: "alpha", State: engine.StateEntry, Raw: raw}},
				Post: []engine.Image{{Entry: "alpha", State: engine.StateAbsent}},
			}
			return p
		}},
		{"move step rejected", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			skillPath := p.Steps[0].Path
			dst := filepath.Join(projDir, ".agents", "skills", "beta")
			p.Steps[0] = engine.Step{
				Kind: engine.StepMove,
				Src:  skillPath,
				Dst:  dst,
				Pre: []engine.Image{
					{Path: skillPath, State: engine.StateTree, TreeSHA: treeSHA},
					{Path: dst, State: engine.StateAbsent},
				},
				Post: []engine.Image{
					{Path: skillPath, State: engine.StateAbsent},
					{Path: dst, State: engine.StateTree, TreeSHA: treeSHA},
				},
			}
			return p
		}},
		{"restoreTree without undoes rejected", func() engine.Plan {
			p := validProjectUndoPlan(t, projDir, engine.ProjectRootAgents, "alpha", snapDir, treeSHA, "op-prev")
			p.Undoes = ""
			return p
		}},
		{"removeTree with undoes rejected", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Undoes = "op-prev"
			return p
		}},
		{"touches lock rejected", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.TouchesLock = true
			return p
		}},
		{"lock sha rejected", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.LockSHA = strings.Repeat("b", 64)
			return p
		}},
		{"non-zero effect rejected", func() engine.Plan {
			p := validProjectRemovalPlan(t, projDir, engine.ProjectRootAgents, "alpha", treeSHA)
			p.Effect = engine.Effect{DropManaged: []string{"alpha"}}
			return p
		}},
	}
	for _, tt := range invalid {
		t.Run("invalid "+tt.name, func(t *testing.T) {
			p := reID(t, tt.build())
			if err := p.ValidateProjectScope(); !errors.Is(err, engine.ErrInvalidStep) {
				t.Errorf("ValidateProjectScope() = %v, want ErrInvalidStep", err)
			}
			e, _ := newEnv(t)
			eng := e.engine()
			if err := eng.Check(p); !errors.Is(err, engine.ErrInvalidStep) {
				t.Errorf("Check() = %v, want ErrInvalidStep", err)
			}
			if _, err := eng.Begin(t.Context(), p); !errors.Is(err, engine.ErrInvalidStep) {
				t.Errorf("Begin() = %v, want ErrInvalidStep", err)
			}
		})
	}
}

type spyFiles struct {
	engine.Files
	calls []string
}

func (s *spyFiles) ReadFile(path string) ([]byte, error) {
	s.calls = append(s.calls, "ReadFile "+path)
	return s.Files.ReadFile(path)
}

func (s *spyFiles) WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	s.calls = append(s.calls, "WriteFileAtomic "+path)
	return s.Files.WriteFileAtomic(path, data, perm)
}

func (s *spyFiles) CopyTree(src, dst string) error {
	s.calls = append(s.calls, "CopyTree "+src+" -> "+dst)
	return s.Files.CopyTree(src, dst)
}

func (s *spyFiles) RemoveTree(path string) error {
	s.calls = append(s.calls, "RemoveTree "+path)
	return s.Files.RemoveTree(path)
}

func (s *spyFiles) Mode(path string) (fs.FileMode, error) {
	s.calls = append(s.calls, "Mode "+path)
	return s.Files.Mode(path)
}

func (s *spyFiles) Exists(path string) (bool, error) {
	s.calls = append(s.calls, "Exists "+path)
	return s.Files.Exists(path)
}

func (s *spyFiles) MkdirAll(path string, perm fs.FileMode) error {
	s.calls = append(s.calls, "MkdirAll "+path)
	return s.Files.MkdirAll(path, perm)
}

func (s *spyFiles) Chmod(path string, perm fs.FileMode) error {
	s.calls = append(s.calls, "Chmod "+path)
	return s.Files.Chmod(path, perm)
}

type spyLinks struct {
	engine.Links
	calls []string
}

func (s *spyLinks) Readlink(path string) (string, error) {
	s.calls = append(s.calls, "Readlink "+path)
	return s.Links.Readlink(path)
}

func (s *spyLinks) Symlink(target, path string) error {
	s.calls = append(s.calls, "Symlink "+path)
	return s.Links.Symlink(target, path)
}

func (s *spyLinks) RenameNoReplace(oldPath, newPath string) error {
	s.calls = append(s.calls, "RenameNoReplace "+oldPath+" -> "+newPath)
	return s.Links.RenameNoReplace(oldPath, newPath)
}

func (s *spyLinks) RemoveLink(path, target string) error {
	s.calls = append(s.calls, "RemoveLink "+path)
	return s.Links.RemoveLink(path, target)
}

type spyHash struct {
	calls []string
}

func (s *spyHash) hash(dir string) (treehash.Tree, error) {
	s.calls = append(s.calls, dir)
	return treehash.Dir(dir)
}

func TestProjectBoundPlanFailsClosedAtAllEngineBoundaries(t *testing.T) {
	e, _ := newEnv(t)
	projectDir := filepath.Join(t.TempDir(), "proj")
	skillDir := filepath.Join(projectDir, ".agents", "skills", "alpha")
	must(t, os.MkdirAll(skillDir, 0o755))
	must(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: alpha\n---\nProject alpha.\n"), 0o644))
	tr, err := treehash.Dir(skillDir)
	must(t, err)

	plan := validProjectRemovalPlan(t, projectDir, engine.ProjectRootAgents, "alpha", tr.SHA)
	projectBefore := tree(t, projectDir)
	homeBefore := tree(t, e.home.Root())

	realFiles := fsadapter.NewFileOps(e.p.Guard())
	filesSpy := &spyFiles{Files: realFiles}
	linksSpy := &spyLinks{Links: fsadapter.NewLinkOps(e.p.Guard())}
	hashSpy := &spyHash{}
	eng := engine.New(engine.Deps{
		Paths:    e.p,
		Files:    filesSpy,
		Links:    linksSpy,
		Lock:     e.lock,
		DropLock: e.lock,
		State:    e.state,
		Dirs:     fsadapter.NewScanner(e.p.Guard()),
		Clock:    e.clock,
		HashDir:  hashSpy.hash,
	})

	assertNoLegacyCalls := func(t *testing.T, op string) {
		t.Helper()
		if len(filesSpy.calls) != 0 || len(linksSpy.calls) != 0 || len(hashSpy.calls) != 0 {
			t.Errorf("%s touched legacy path I/O: files=%v links=%v hash=%v", op, filesSpy.calls, linksSpy.calls, hashSpy.calls)
		}
		filesSpy.calls = nil
		linksSpy.calls = nil
		hashSpy.calls = nil
	}

	t.Run("NewBuilder refuses before legacy observation", func(t *testing.T) {
		b := eng.NewBuilder(engine.Meta{
			Action:  "remove",
			Targets: []string{},
			Project: &engine.ProjectScope{ProjectDir: projectDir, Root: engine.ProjectRootAgents, SkillName: "alpha"},
		})
		if err := b.RemoveTree(skillDir); !errors.Is(err, engine.ErrProjectScopeUnsupported) {
			t.Fatalf("RemoveTree() = %v, want ErrProjectScopeUnsupported", err)
		}
		if _, err := b.Build(); !errors.Is(err, engine.ErrProjectScopeUnsupported) {
			t.Fatalf("Build() = %v, want ErrProjectScopeUnsupported", err)
		}
		assertNoLegacyCalls(t, "NewBuilder")

		bad := eng.NewBuilder(engine.Meta{
			Action:  "remove",
			Targets: []string{},
			Project: &engine.ProjectScope{ProjectDir: "relative", Root: engine.ProjectRootAgents, SkillName: "alpha"},
		})
		if err := bad.RemoveTree(skillDir); !errors.Is(err, engine.ErrInvalidStep) {
			t.Fatalf("invalid NewBuilder RemoveTree() = %v, want ErrInvalidStep", err)
		}
		if _, err := bad.Build(); !errors.Is(err, engine.ErrInvalidStep) {
			t.Fatalf("invalid NewBuilder Build() = %v, want ErrInvalidStep", err)
		}
		assertNoLegacyCalls(t, "invalid NewBuilder")
	})

	t.Run("tampered ProjectScope fails ID verification before execution", func(t *testing.T) {
		tampered := clonePlan(t, plan)
		cp := *tampered.Project
		cp.SkillName = "beta"
		tampered.Project = &cp
		if err := eng.Check(tampered); !errors.Is(err, engine.ErrPlanChanged) {
			t.Fatalf("Check(tampered) = %v, want ErrPlanChanged", err)
		}
		if _, err := eng.Begin(t.Context(), tampered); !errors.Is(err, engine.ErrPlanChanged) {
			t.Fatalf("Begin(tampered) = %v, want ErrPlanChanged", err)
		}
		if _, err := eng.Apply(t.Context(), tampered); !errors.Is(err, engine.ErrPlanChanged) {
			t.Fatalf("Apply(tampered) = %v, want ErrPlanChanged", err)
		}
		assertNoLegacyCalls(t, "tampered ProjectScope")
	})

	t.Run("Check refuses before legacy path observation", func(t *testing.T) {
		if err := eng.Check(plan); !errors.Is(err, engine.ErrProjectScopeUnsupported) {
			t.Fatalf("Check() = %v, want ErrProjectScopeUnsupported", err)
		}
		assertNoLegacyCalls(t, "Check")
	})

	t.Run("Begin refuses before lock, Check, snapshot, or record", func(t *testing.T) {
		run, err := eng.Begin(t.Context(), plan)
		if !errors.Is(err, engine.ErrProjectScopeUnsupported) {
			t.Fatalf("Begin() = %v, want ErrProjectScopeUnsupported", err)
		}
		if run != nil {
			_ = run.Release()
			t.Fatal("Begin() returned non-nil Run")
		}
		assertNoLegacyCalls(t, "Begin")
		if recordExists(t, e.p) || len(snapshotDirs(t, e.p)) != 0 {
			t.Error("Begin wrote record or snapshot")
		}
	})

	t.Run("Apply refuses without touching project or home", func(t *testing.T) {
		if _, err := eng.Apply(t.Context(), plan); !errors.Is(err, engine.ErrProjectScopeUnsupported) {
			t.Fatalf("Apply() = %v, want ErrProjectScopeUnsupported", err)
		}
		assertNoLegacyCalls(t, "Apply")
		if got := tree(t, projectDir); !reflect.DeepEqual(got, projectBefore) {
			t.Fatalf("Apply mutated projectDir: %v", got)
		}
		if got := tree(t, e.home.Root()); !reflect.DeepEqual(got, homeBefore) {
			t.Fatalf("Apply mutated home: %v", got)
		}
	})

	t.Run("PendingRecord and Recover fail closed for a project-bound record", func(t *testing.T) {
		const snapID = "op-20261124T100000.000000000Z-projrec"
		rec := engine.Record{
			Format:        "skillhub.operation",
			Version:       2,
			PlanID:        plan.ID,
			Plan:          plan,
			SnapshotID:    snapID,
			SnapshotSHA:   strings.Repeat("a", 64),
			StartedAt:     e.clock.Now().UTC(),
			LastCompleted: -1,
		}
		recData, err := json.MarshalIndent(rec, "", "  ")
		must(t, err)
		must(t, os.MkdirAll(filepath.Dir(engine.RecordFile(e.p)), 0o755))
		must(t, os.WriteFile(engine.RecordFile(e.p), append(recData, '\n'), 0o600))
		t.Cleanup(func() { _ = os.Remove(engine.RecordFile(e.p)) })

		filesSpy.calls = nil
		linksSpy.calls = nil
		hashSpy.calls = nil

		_, _, err = eng.PendingRecord()
		if !errors.Is(err, engine.ErrUnsupportedRecord) || !errors.Is(err, engine.ErrProjectScopeUnsupported) {
			t.Fatalf("PendingRecord() = %v, want ErrUnsupportedRecord + ErrProjectScopeUnsupported", err)
		}
		_, err = eng.Recover(t.Context())
		if !errors.Is(err, engine.ErrUnsupportedRecord) || !errors.Is(err, engine.ErrProjectScopeUnsupported) {
			t.Fatalf("Recover() = %v, want ErrUnsupportedRecord + ErrProjectScopeUnsupported", err)
		}
		for _, c := range filesSpy.calls {
			if strings.Contains(c, projectDir) || !strings.HasPrefix(c, "ReadFile ") {
				t.Errorf("Recover/PendingRecord made unexpected file call: %s", c)
			}
		}
		if len(linksSpy.calls) != 0 || len(hashSpy.calls) != 0 {
			t.Errorf("Recover touched links/hash: links=%v hash=%v", linksSpy.calls, hashSpy.calls)
		}
		if !recordExists(t, e.p) {
			t.Error("Recover removed the unsupported pending record")
		}
		if got := tree(t, projectDir); !reflect.DeepEqual(got, projectBefore) {
			t.Fatalf("Recover mutated projectDir: %v", got)
		}
		if len(journal(t, e)) != 0 {
			t.Fatalf("Recover wrote journal: %+v", journal(t, e))
		}
	})

	t.Run("UndoPlan and Undo fail closed before observing project path", func(t *testing.T) {
		const snapID = "op-20261124T100000.000000000Z-projundo"
		snapDir := eng.SnapshotDir(snapID)
		copyDir := filepath.Join(snapDir, "trees", "0")
		must(t, os.MkdirAll(copyDir, 0o755))
		must(t, os.WriteFile(filepath.Join(copyDir, "SKILL.md"), []byte("---\nname: alpha\n---\nProject alpha.\n"), 0o644))
		snap := engine.Snapshot{
			Format:     "skillhub.snapshot",
			Version:    2,
			SnapshotID: snapID,
			PlanID:     plan.ID,
			TakenAt:    e.clock.Now().UTC(),
			Trees: []engine.TreeCopy{
				{Path: skillDir, TreeSHA: tr.SHA, Copy: "trees/0"},
			},
			Links:    []engine.LinkCopy{},
			Entries:  []engine.EntryCopy{},
			LockKeys: []string{},
			State: engine.StateImage{
				Managed: map[string]domain.ManagedSkill{},
				Parked:  map[string]domain.ParkedSkill{},
			},
			Plan: plan,
		}
		snapBytes, err := json.MarshalIndent(snap, "", "  ")
		must(t, err)
		snapBytes = append(snapBytes, '\n')
		must(t, os.WriteFile(filepath.Join(snapDir, "snapshot.json"), snapBytes, 0o644))
		sumBytes := sha256.Sum256(snapBytes)
		sum := hex.EncodeToString(sumBytes[:])
		must(t, e.state.AppendJournal(domain.HistoryEntry{
			At:         e.clock.Now().UTC(),
			Action:     domain.ActionApplied,
			Target:     engine.JournalTarget(plan),
			SnapshotID: snapID,
			Labels:     []domain.Label{},
			Detail:     "plan=" + plan.ID + " snapshot_sha=" + sum,
		}))

		filesSpy.calls = nil
		linksSpy.calls = nil
		hashSpy.calls = nil

		if _, err := eng.UndoPlan(snapID); !errors.Is(err, engine.ErrProjectScopeUnsupported) {
			t.Fatalf("UndoPlan() = %v, want ErrProjectScopeUnsupported", err)
		}
		if _, err := eng.Undo(t.Context(), snapID); !errors.Is(err, engine.ErrProjectScopeUnsupported) {
			t.Fatalf("Undo() = %v, want ErrProjectScopeUnsupported", err)
		}
		for _, c := range filesSpy.calls {
			if strings.Contains(c, projectDir) {
				t.Errorf("UndoPlan/Undo touched projectDir: %s", c)
			}
			if strings.HasPrefix(c, "Write") || strings.HasPrefix(c, "Copy") || strings.HasPrefix(c, "Remove") {
				t.Errorf("UndoPlan/Undo performed write call: %s", c)
			}
		}
		for _, c := range hashSpy.calls {
			if strings.Contains(c, projectDir) {
				t.Errorf("UndoPlan/Undo hashed projectDir: %s", c)
			}
		}
		if len(linksSpy.calls) != 0 {
			t.Errorf("UndoPlan/Undo touched links: %v", linksSpy.calls)
		}
		if got := tree(t, projectDir); !reflect.DeepEqual(got, projectBefore) {
			t.Fatalf("Undo mutated projectDir: %v", got)
		}
		if len(journal(t, e)) != 1 {
			t.Fatalf("Undo wrote extra journal entry: %+v", journal(t, e))
		}
	})

	t.Run("ReadSnapshot rejects snapshot with invalid project binding", func(t *testing.T) {
		const snapID = "op-20261124T100000.000000000Z-badproj"
		badPlan := clonePlan(t, plan)
		badPlan.Project.Root = ".codex/skills"
		badPlan = reID(t, badPlan)
		snapDir := eng.SnapshotDir(snapID)
		copyDir := filepath.Join(snapDir, "trees", "0")
		must(t, os.MkdirAll(copyDir, 0o755))
		must(t, os.WriteFile(filepath.Join(copyDir, "SKILL.md"), []byte("---\nname: alpha\n---\nProject alpha.\n"), 0o644))
		snap := engine.Snapshot{
			Format:     "skillhub.snapshot",
			Version:    2,
			SnapshotID: snapID,
			PlanID:     badPlan.ID,
			TakenAt:    e.clock.Now().UTC(),
			Trees: []engine.TreeCopy{
				{Path: skillDir, TreeSHA: tr.SHA, Copy: "trees/0"},
			},
			Links:    []engine.LinkCopy{},
			Entries:  []engine.EntryCopy{},
			LockKeys: []string{},
			State: engine.StateImage{
				Managed: map[string]domain.ManagedSkill{},
				Parked:  map[string]domain.ParkedSkill{},
			},
			Plan: badPlan,
		}
		snapBytes, err := json.MarshalIndent(snap, "", "  ")
		must(t, err)
		must(t, os.WriteFile(filepath.Join(snapDir, "snapshot.json"), append(snapBytes, '\n'), 0o644))

		if _, err := eng.ReadSnapshot(snapID); !errors.Is(err, engine.ErrUnsupportedRecord) {
			t.Fatalf("ReadSnapshot() = %v, want ErrUnsupportedRecord", err)
		}
	})
}

func TestLegacyGlobalWorkflowUnaffected(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	noOwnLevels(t, e)
	start := observeWorld(t, e)
	b := eng.NewBuilder(engine.Meta{Action: "remove", Targets: []string{"claude-code"}})
	must(t, b.RemoveTree(fp.beta))
	plan, err := b.Build()
	must(t, err)
	if plan.HasProjectScope() {
		t.Fatal("legacy plan reported HasProjectScope = true")
	}
	must(t, eng.Check(plan))
	out, err := eng.Apply(t.Context(), plan)
	must(t, err)
	if _, err := os.Lstat(fp.beta); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("beta still exists after global remove: %v", err)
	}
	_, err = eng.Undo(t.Context(), out.SnapshotID)
	must(t, err)
	assertWorld(t, "after global remove undo", observeWorld(t, e), start)
}

type stubCrossDeviceProjectFS struct {
	*fsadapter.ProjectOps
}

func (s stubCrossDeviceProjectFS) SameDevice(_, _, _ string) (bool, error) {
	return false, nil
}

func newProjectEnabledEngine(e *env, filesSpy *spyFiles, linksSpy *spyLinks, hashSpy *spyHash, projectDir string, pfs engine.ProjectFS) *engine.Engine {
	if pfs == nil {
		pfs = fsadapter.NewProjectOps(e.p.Guard())
	}
	return engine.New(engine.Deps{
		Paths:      e.p,
		Files:      filesSpy,
		Links:      linksSpy,
		Lock:       e.lock,
		DropLock:   e.lock,
		State:      e.state,
		Dirs:       fsadapter.NewScanner(e.p.Guard()),
		Clock:      e.clock,
		HashDir:    hashSpy.hash,
		ProjectDir: projectDir,
		ProjectFS:  pfs,
	})
}

func TestProjectScopedEngineApplyUndoRecoverAndGuards(t *testing.T) {
	e, _ := newEnv(t)
	noOwnLevels(t, e)
	filesSpy := &spyFiles{Files: fsadapter.NewFileOps(e.p.Guard())}
	linksSpy := &spyLinks{Links: fsadapter.NewLinkOps(e.p.Guard())}
	hashSpy := &spyHash{}

	projectDir := filepath.Join(filepath.Dir(e.home.Root()), "project")
	otherProjectDir := filepath.Join(filepath.Dir(e.home.Root()), "other-project")
	must(t, os.MkdirAll(otherProjectDir, 0o755))

	skillDir := filepath.Join(projectDir, ".agents", "skills", "alpha")
	claudeSame := filepath.Join(projectDir, ".claude", "skills", "alpha")
	must(t, os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755))
	must(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: alpha\n---\nProject agents alpha.\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(skillDir, "scripts", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755))
	must(t, os.Chmod(filepath.Join(skillDir, "scripts", "run.sh"), 0o755))
	must(t, os.MkdirAll(filepath.Join(skillDir, "empty-dir"), 0o755))

	// Competing global skill and sibling-root project skill with the same name must stay untouched.
	globalAlpha := filepath.Join(e.home.Root(), ".agents", "skills", "alpha")
	must(t, os.MkdirAll(globalAlpha, 0o755))
	must(t, os.WriteFile(filepath.Join(globalAlpha, "SKILL.md"), []byte("global alpha\n"), 0o644))
	must(t, os.MkdirAll(claudeSame, 0o755))
	must(t, os.WriteFile(filepath.Join(claudeSame, "SKILL.md"), []byte("claude alpha\n"), 0o644))

	beforeGlobal, err := treehash.Dir(globalAlpha)
	must(t, err)
	beforeClaude, err := treehash.Dir(claudeSame)
	must(t, err)
	beforeAlpha, err := treehash.Dir(skillDir)
	must(t, err)

	eng := newProjectEnabledEngine(e, filesSpy, linksSpy, hashSpy, projectDir, nil)
	assertNoProjectLegacyIO := func(t *testing.T, phase string) {
		t.Helper()
		for _, c := range filesSpy.calls {
			if strings.Contains(c, projectDir) {
				t.Errorf("%s touched projectDir via legacy Files: %s", phase, c)
			}
		}
		for _, c := range linksSpy.calls {
			if strings.Contains(c, projectDir) {
				t.Errorf("%s touched projectDir via legacy Links: %s", phase, c)
			}
		}
		for _, c := range hashSpy.calls {
			if strings.Contains(c, projectDir) {
				t.Errorf("%s touched projectDir via legacy HashDir: %s", phase, c)
			}
		}
		filesSpy.calls = nil
		linksSpy.calls = nil
		hashSpy.calls = nil
	}

	b := eng.NewBuilder(engine.Meta{
		Action:  "remove",
		Targets: []string{},
		Project: &engine.ProjectScope{ProjectDir: projectDir, Root: engine.ProjectRootAgents, SkillName: "alpha"},
	})
	must(t, b.RemoveTree(skillDir))
	plan, err := b.Build()
	must(t, err)
	assertNoProjectLegacyIO(t, "Build")

	// Cross-device refusal during Check.
	crossEng := newProjectEnabledEngine(e, filesSpy, linksSpy, hashSpy, projectDir, stubCrossDeviceProjectFS{fsadapter.NewProjectOps(e.p.Guard())})
	if err := crossEng.Check(plan); !errors.Is(err, ports.ErrCrossDevice) {
		t.Fatalf("Check(cross-device) = %v, want ErrCrossDevice", err)
	}

	// Wrong-project refusal during Check and Apply.
	otherEng := newProjectEnabledEngine(e, filesSpy, linksSpy, hashSpy, otherProjectDir, nil)
	var wpe *engine.WrongProjectError
	if err := otherEng.Check(plan); !errors.Is(err, engine.ErrWrongProject) || !errors.As(err, &wpe) {
		t.Fatalf("otherEng.Check() = %v, want WrongProjectError", err)
	}
	if _, err := otherEng.Apply(t.Context(), plan); !errors.Is(err, engine.ErrWrongProject) {
		t.Fatalf("otherEng.Apply() = %v, want WrongProjectError", err)
	}

	// Symlink swap after Build refuses Apply with ErrPrecondition/ErrProjectTreeLinked before moving.
	swapTemp := filepath.Join(filepath.Dir(e.home.Root()), "swap-alpha")
	must(t, os.Rename(skillDir, swapTemp))
	must(t, os.Symlink(swapTemp, skillDir))
	if _, err := eng.Apply(t.Context(), plan); err == nil {
		t.Fatal("Apply succeeded when skillDir was swapped to a symlink")
	}
	must(t, os.Remove(skillDir))
	must(t, os.Rename(swapTemp, skillDir))

	// Apply removes only projectDir/.agents/skills/alpha cleanly via ProjectFS.
	must(t, eng.Check(plan))
	out, err := eng.Apply(t.Context(), plan)
	must(t, err)
	assertNoProjectLegacyIO(t, "Apply")

	if _, err := os.Lstat(skillDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("skillDir still exists after Apply: %v", err)
	}
	if got, err := treehash.Dir(globalAlpha); err != nil || got.SHA != beforeGlobal.SHA {
		t.Fatalf("global alpha modified: err=%v got=%v", err, got)
	}
	if got, err := treehash.Dir(claudeSame); err != nil || got.SHA != beforeClaude.SHA {
		t.Fatalf("claude project alpha modified: err=%v got=%v", err, got)
	}

	// Wrong project cannot UndoPlan or Undo.
	if _, err := otherEng.UndoPlan(out.SnapshotID); !errors.Is(err, engine.ErrWrongProject) {
		t.Fatalf("otherEng.UndoPlan() = %v, want WrongProjectError", err)
	}
	if _, err := otherEng.Undo(t.Context(), out.SnapshotID); !errors.Is(err, engine.ErrWrongProject) {
		t.Fatalf("otherEng.Undo() = %v, want WrongProjectError", err)
	}

	// Foreign occupant at skillDir blocks UndoPlan with ErrChangedSince.
	must(t, os.WriteFile(skillDir, []byte("foreign file"), 0o644))
	if _, err := eng.UndoPlan(out.SnapshotID); !errors.Is(err, engine.ErrChangedSince) {
		t.Fatalf("UndoPlan with foreign file = %v, want ErrChangedSince", err)
	}
	must(t, os.Remove(skillDir))

	// Delete .agents completely before Undo; Undo recreates .agents/skills and restores alpha byte for byte.
	must(t, os.RemoveAll(filepath.Join(projectDir, ".agents")))
	filesSpy.calls = nil
	linksSpy.calls = nil
	hashSpy.calls = nil
	_, err = eng.Undo(t.Context(), out.SnapshotID)
	must(t, err)
	assertNoProjectLegacyIO(t, "Undo")

	restoredAlpha, err := treehash.Dir(skillDir)
	must(t, err)
	if restoredAlpha.SHA != beforeAlpha.SHA || !reflect.DeepEqual(restoredAlpha.Nodes, beforeAlpha.Nodes) {
		t.Fatalf("restored project alpha mismatch: got=%+v want=%+v", restoredAlpha, beforeAlpha)
	}
	if _, err := os.Lstat(filepath.Join(skillDir, "empty-dir")); err != nil {
		t.Fatalf("empty-dir missing after Undo: %v", err)
	}

	// Crash recovery of an interrupted project StepRemoveTree (run via Begin without Execute).
	b2 := eng.NewBuilder(engine.Meta{
		Action:  "remove",
		Targets: []string{},
		Project: &engine.ProjectScope{ProjectDir: projectDir, Root: engine.ProjectRootAgents, SkillName: "alpha"},
	})
	must(t, b2.RemoveTree(skillDir))
	plan2, err := b2.Build()
	must(t, err)

	// Simulate interrupted StepRemoveTree: skill removed, record stays pending at LastCompleted=-1.
	run, err := eng.Begin(t.Context(), plan2)
	must(t, err)
	ops := fsadapter.NewProjectOps(e.p.Guard())
	trashPath := filepath.Join(filepath.Dir(e.p.ParkedDir()), "trash", "manual-crash-trash")
	must(t, os.MkdirAll(filepath.Dir(trashPath), 0o755))
	must(t, ops.RemoveSkillToTrash(projectDir, string(engine.ProjectRootAgents), "alpha", trashPath))
	must(t, run.Release())

	// Wrong-project Recover refuses and preserves current.json and leaves skill absent.
	if _, err := otherEng.Recover(t.Context()); !errors.Is(err, engine.ErrWrongProject) {
		t.Fatalf("otherEng.Recover() = %v, want WrongProjectError", err)
	}
	if !recordExists(t, e.p) {
		t.Fatal("otherEng.Recover deleted pending current.json")
	}

	// Same-project Recover rolls back the interrupted removal from snapshot and restores alpha.
	res, err := eng.Recover(t.Context())
	must(t, err)
	if !res.Found || !res.RolledBack {
		t.Fatalf("Recover result = %+v, want Found=true RolledBack=true", res)
	}
	recoveredAlpha, err := treehash.Dir(skillDir)
	must(t, err)
	if recoveredAlpha.SHA != beforeAlpha.SHA {
		t.Fatalf("recovered alpha SHA = %s, want %s", recoveredAlpha.SHA, beforeAlpha.SHA)
	}
	if _, err := os.Stat(trashPath); err == nil {
		must(t, os.RemoveAll(trashPath))
	}

	// Crash recovery of an interrupted Undo (StepRestoreTree interrupted after restoring folder):
	// Recover must roll back using RemoveSkillToTrash and leave alpha absent cleanly.
	bApply := eng.NewBuilder(engine.Meta{
		Action:  "remove",
		Targets: []string{},
		Project: &engine.ProjectScope{ProjectDir: projectDir, Root: engine.ProjectRootAgents, SkillName: "alpha"},
	})
	must(t, bApply.RemoveTree(skillDir))
	applyPlan, err := bApply.Build()
	must(t, err)
	appliedOut, err := eng.Apply(t.Context(), applyPlan)
	must(t, err)
	undoPlan, err := eng.UndoPlan(appliedOut.SnapshotID)
	must(t, err)
	undoRun, err := eng.Begin(t.Context(), undoPlan)
	must(t, err)
	snapCopyDir := filepath.Join(eng.SnapshotDir(appliedOut.SnapshotID), "trees", "0")
	undoStage := filepath.Join(filepath.Dir(e.p.ParkedDir()), "restore", "manual-undo-crash")
	must(t, os.MkdirAll(filepath.Dir(undoStage), 0o755))
	must(t, fsadapter.NewFileOps(e.p.Guard()).CopyTree(snapCopyDir, undoStage))
	must(t, ops.RestoreSkillFromStaging(undoStage, projectDir, string(engine.ProjectRootAgents), "alpha"))
	must(t, undoRun.Release())
	resUndo, err := eng.Recover(t.Context())
	must(t, err)
	if !resUndo.Found || !resUndo.RolledBack {
		t.Fatalf("Recover(interrupted undo) = %+v, want Found=true RolledBack=true", resUndo)
	}
	if _, err := os.Lstat(skillDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("skillDir still present after rolled-back interrupted undo: %v", err)
	}

	// Foreign state during interrupted removal halts Recover with ForeignStateError and leaves current.json.
	_, err = eng.Undo(t.Context(), appliedOut.SnapshotID)
	must(t, err)
	bForeign := eng.NewBuilder(engine.Meta{
		Action:  "remove",
		Targets: []string{},
		Project: &engine.ProjectScope{ProjectDir: projectDir, Root: engine.ProjectRootAgents, SkillName: "alpha"},
	})
	must(t, bForeign.RemoveTree(skillDir))
	foreignPlan, err := bForeign.Build()
	must(t, err)
	foreignRun, err := eng.Begin(t.Context(), foreignPlan)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(skillDir, "foreign.txt"), []byte("changed"), 0o644))
	must(t, foreignRun.Release())
	var fe *engine.ForeignStateError
	if _, err := eng.Recover(t.Context()); !errors.As(err, &fe) {
		t.Fatalf("Recover(foreign state) err = %v, want ForeignStateError", err)
	}
	if !recordExists(t, e.p) {
		t.Fatal("Recover(foreign state) deleted pending record")
	}
	must(t, os.Remove(filepath.Join(skillDir, "foreign.txt")))
	must(t, os.Remove(engine.RecordFile(e.p)))
}

func TestProjectScopedLinksValidationLifecycleAndRollback(t *testing.T) {
	projDir := filepath.Join(t.TempDir(), "project")
	snapDir := filepath.Join(t.TempDir(), "snapshots", "op-prev")
	const treeSHA = "1111111111111111111111111111111111111111"
	const relTarget = "../../.claude/skills/alpha"
	skillPath := filepath.Join(projDir, ".claude", "skills", "alpha")
	linkPath := filepath.Join(projDir, ".agents", "skills", "alpha")

	t.Run("ValidateProjectScope accepts valid unlink+remove and restore+link plans", func(t *testing.T) {
		remPlan := engine.Plan{
			Meta: engine.Meta{
				Action:  "remove",
				Targets: []string{},
				Project: &engine.ProjectScope{ProjectDir: projDir, Root: engine.ProjectRootClaude, SkillName: "alpha"},
			},
			Steps: []engine.Step{
				{
					Kind:   engine.StepUnlink,
					Path:   linkPath,
					Target: relTarget,
					Pre:    []engine.Image{{Path: linkPath, State: engine.StateLink, Target: relTarget}},
					Post:   []engine.Image{{Path: linkPath, State: engine.StateAbsent}},
				},
				{
					Kind: engine.StepRemoveTree,
					Path: skillPath,
					Pre:  []engine.Image{{Path: skillPath, State: engine.StateTree, TreeSHA: treeSHA}},
					Post: []engine.Image{{Path: skillPath, State: engine.StateAbsent}},
				},
			},
		}
		remPlan = reID(t, remPlan)
		if err := remPlan.ValidateProjectScope(); err != nil {
			t.Fatalf("ValidateProjectScope(remove with unlink) = %v, want nil", err)
		}

		undoPlan := engine.Plan{
			Meta: engine.Meta{
				Action:  "undo",
				Targets: []string{},
				Project: &engine.ProjectScope{ProjectDir: projDir, Root: engine.ProjectRootClaude, SkillName: "alpha"},
			},
			Undoes: "op-prev",
			Steps: []engine.Step{
				{
					Kind: engine.StepRestoreTree,
					Path: skillPath,
					Src:  filepath.Join(snapDir, "trees", "0"),
					Pre:  []engine.Image{{Path: skillPath, State: engine.StateAbsent}},
					Post: []engine.Image{{Path: skillPath, State: engine.StateTree, TreeSHA: treeSHA}},
				},
				{
					Kind:   engine.StepLink,
					Path:   linkPath,
					Target: relTarget,
					Pre:    []engine.Image{{Path: linkPath, State: engine.StateAbsent}},
					Post:   []engine.Image{{Path: linkPath, State: engine.StateLink, Target: relTarget}},
				},
			},
		}
		undoPlan = reID(t, undoPlan)
		if err := undoPlan.ValidateProjectScope(); err != nil {
			t.Fatalf("ValidateProjectScope(undo with link) = %v, want nil", err)
		}
	})

	t.Run("ValidateProjectScope rejects invalid link steps", func(t *testing.T) {
		cases := []struct {
			name string
			plan engine.Plan
		}{
			{
				name: "unlink targeting wantPath",
				plan: engine.Plan{
					Meta: engine.Meta{Action: "remove", Targets: []string{}, Project: &engine.ProjectScope{ProjectDir: projDir, Root: engine.ProjectRootClaude, SkillName: "alpha"}},
					Steps: []engine.Step{
						{Kind: engine.StepUnlink, Path: skillPath, Target: relTarget, Pre: []engine.Image{{Path: skillPath, State: engine.StateLink, Target: relTarget}}, Post: []engine.Image{{Path: skillPath, State: engine.StateAbsent}}},
						{Kind: engine.StepRemoveTree, Path: skillPath, Pre: []engine.Image{{Path: skillPath, State: engine.StateTree, TreeSHA: treeSHA}}, Post: []engine.Image{{Path: skillPath, State: engine.StateAbsent}}},
					},
				},
			},
			{
				name: "unlink outside project",
				plan: engine.Plan{
					Meta: engine.Meta{Action: "remove", Targets: []string{}, Project: &engine.ProjectScope{ProjectDir: projDir, Root: engine.ProjectRootClaude, SkillName: "alpha"}},
					Steps: []engine.Step{
						{Kind: engine.StepUnlink, Path: filepath.Join(projDir, ".codex", "skills", "alpha"), Target: relTarget, Pre: []engine.Image{{Path: filepath.Join(projDir, ".codex", "skills", "alpha"), State: engine.StateLink, Target: relTarget}}, Post: []engine.Image{{Path: filepath.Join(projDir, ".codex", "skills", "alpha"), State: engine.StateAbsent}}},
						{Kind: engine.StepRemoveTree, Path: skillPath, Pre: []engine.Image{{Path: skillPath, State: engine.StateTree, TreeSHA: treeSHA}}, Post: []engine.Image{{Path: skillPath, State: engine.StateAbsent}}},
					},
				},
			},
			{
				name: "duplicate unlink path",
				plan: engine.Plan{
					Meta: engine.Meta{Action: "remove", Targets: []string{}, Project: &engine.ProjectScope{ProjectDir: projDir, Root: engine.ProjectRootClaude, SkillName: "alpha"}},
					Steps: []engine.Step{
						{Kind: engine.StepUnlink, Path: linkPath, Target: relTarget, Pre: []engine.Image{{Path: linkPath, State: engine.StateLink, Target: relTarget}}, Post: []engine.Image{{Path: linkPath, State: engine.StateAbsent}}},
						{Kind: engine.StepUnlink, Path: linkPath, Target: relTarget, Pre: []engine.Image{{Path: linkPath, State: engine.StateLink, Target: relTarget}}, Post: []engine.Image{{Path: linkPath, State: engine.StateAbsent}}},
						{Kind: engine.StepRemoveTree, Path: skillPath, Pre: []engine.Image{{Path: skillPath, State: engine.StateTree, TreeSHA: treeSHA}}, Post: []engine.Image{{Path: skillPath, State: engine.StateAbsent}}},
					},
				},
			},
			{
				name: "unlink after removeTree",
				plan: engine.Plan{
					Meta: engine.Meta{Action: "remove", Targets: []string{}, Project: &engine.ProjectScope{ProjectDir: projDir, Root: engine.ProjectRootClaude, SkillName: "alpha"}},
					Steps: []engine.Step{
						{Kind: engine.StepRemoveTree, Path: skillPath, Pre: []engine.Image{{Path: skillPath, State: engine.StateTree, TreeSHA: treeSHA}}, Post: []engine.Image{{Path: skillPath, State: engine.StateAbsent}}},
						{Kind: engine.StepUnlink, Path: linkPath, Target: relTarget, Pre: []engine.Image{{Path: linkPath, State: engine.StateLink, Target: relTarget}}, Post: []engine.Image{{Path: linkPath, State: engine.StateAbsent}}},
					},
				},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				p := reID(t, tc.plan)
				if err := p.ValidateProjectScope(); !errors.Is(err, engine.ErrInvalidStep) {
					t.Errorf("ValidateProjectScope() = %v, want ErrInvalidStep", err)
				}
			})
		}
	})

	t.Run("Apply, Undo, and crash recovery use ProjectFS without legacy Links", func(t *testing.T) {
		e, _ := newEnv(t)
		noOwnLevels(t, e)
		filesSpy := &spyFiles{Files: fsadapter.NewFileOps(e.p.Guard())}
		linksSpy := &spyLinks{Links: fsadapter.NewLinkOps(e.p.Guard())}
		hashSpy := &spyHash{}

		pDir := filepath.Join(filepath.Dir(e.home.Root()), "proj-links")
		sDir := filepath.Join(pDir, ".claude", "skills", "alpha")
		lPath := filepath.Join(pDir, ".agents", "skills", "alpha")
		must(t, os.MkdirAll(sDir, 0o755))
		must(t, os.WriteFile(filepath.Join(sDir, "SKILL.md"), []byte("---\nname: alpha\n---\n"), 0o644))
		must(t, os.MkdirAll(filepath.Dir(lPath), 0o755))
		must(t, os.Symlink(relTarget, lPath))
		beforeTree, err := treehash.Dir(sDir)
		must(t, err)

		eng := newProjectEnabledEngine(e, filesSpy, linksSpy, hashSpy, pDir, nil)
		b := eng.NewBuilder(engine.Meta{
			Action:  "remove",
			Targets: []string{},
			Project: &engine.ProjectScope{ProjectDir: pDir, Root: engine.ProjectRootClaude, SkillName: "alpha"},
		})
		must(t, b.Unlink(lPath, relTarget))
		must(t, b.RemoveTree(sDir))
		plan, err := b.Build()
		must(t, err)

		out, err := eng.Apply(t.Context(), plan)
		must(t, err)
		for _, c := range linksSpy.calls {
			if strings.Contains(c, pDir) {
				t.Fatalf("Apply touched project link via legacy Links: %s", c)
			}
		}
		if _, err := os.Lstat(lPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("link still exists after Apply: %v", err)
		}
		if _, err := os.Lstat(sDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("skill dir still exists after Apply: %v", err)
		}

		// Foreign occupant at link slot blocks UndoPlan with ErrChangedSince.
		must(t, os.WriteFile(lPath, []byte("occupant"), 0o644))
		if _, err := eng.UndoPlan(out.SnapshotID); !errors.Is(err, engine.ErrChangedSince) {
			t.Fatalf("UndoPlan with occupant at link slot = %v, want ErrChangedSince", err)
		}
		must(t, os.Remove(lPath))

		linksSpy.calls = nil
		_, err = eng.Undo(t.Context(), out.SnapshotID)
		must(t, err)
		for _, c := range linksSpy.calls {
			if strings.Contains(c, pDir) {
				t.Fatalf("Undo touched project link via legacy Links: %s", c)
			}
		}
		restoredTree, err := treehash.Dir(sDir)
		must(t, err)
		if restoredTree.SHA != beforeTree.SHA {
			t.Fatalf("restored tree SHA = %s, want %s", restoredTree.SHA, beforeTree.SHA)
		}
		gotTarget, err := os.Readlink(lPath)
		must(t, err)
		if gotTarget != relTarget {
			t.Fatalf("restored link target = %q, want %q", gotTarget, relTarget)
		}

		// Crash recovery after StepUnlink completed (LastCompleted = 0) rolls back the link via ProjectFS.
		run, err := eng.Begin(t.Context(), plan)
		must(t, err)
		must(t, os.Remove(lPath))
		recPath := engine.RecordFile(e.p)
		recBytes, err := os.ReadFile(recPath)
		must(t, err)
		var rec engine.Record
		must(t, json.Unmarshal(recBytes, &rec))
		rec.LastCompleted = 0
		updatedBytes, err := json.MarshalIndent(rec, "", "  ")
		must(t, err)
		must(t, os.WriteFile(recPath, append(updatedBytes, '\n'), 0o600))
		must(t, run.Release())

		linksSpy.calls = nil
		recOutcome, err := eng.Recover(t.Context())
		must(t, err)
		if !recOutcome.Found || !recOutcome.RolledBack {
			t.Fatalf("Recover result = %+v, want Found=true RolledBack=true", recOutcome)
		}
		for _, c := range linksSpy.calls {
			if strings.Contains(c, pDir) {
				t.Fatalf("Recover touched project link via legacy Links: %s", c)
			}
		}
		gotTarget, err = os.Readlink(lPath)
		must(t, err)
		if gotTarget != relTarget {
			t.Fatalf("recovered link target = %q, want %q", gotTarget, relTarget)
		}
	})
}
