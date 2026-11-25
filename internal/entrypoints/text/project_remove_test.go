package text_test

import (
	"errors"
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

func TestProjectRemovalPreviewAndResult(t *testing.T) {
	p := app.Proposal{
		Request:   app.Request{Action: app.ActionRemove, Key: "project:.agents/skills/solo"},
		PlanID:    "plan-1",
		Operation: "v2:remove",
		Remove: &app.RemovePreview{
			Key:         "project:.agents/skills/solo",
			Project:     true,
			ProjectRoot: ".agents/skills",
			SkillName:   "solo",
			Folder:      "/work/demo/.agents/skills/solo",
			TreeSHA:     "deadbeef",
			Homonyms:    []app.Homonym{{Path: "/home/u/.claude/skills/solo", Harnesses: []string{"claude-code"}}},
		},
	}
	got := text.Proposal(p)
	for _, want := range []string{
		"Eliminar project:.agents/skills/solo",
		"Carpeta (proyecto (.agents/skills)): /work/demo/.agents/skills/solo",
		"/home/u/.claude/skills/solo",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Proposal lacks %q:\n%s", want, got)
		}
	}

	done := text.Done(app.Result{
		Proposal: app.Proposal{
			Request:   app.Request{Action: app.ActionRemove, Key: "project:.agents/skills/solo"},
			Operation: "v2:remove",
		},
		SnapshotID: "snap-proj",
	})
	if !strings.Contains(done, "skillhub undo v2:remove --to snap-proj") {
		t.Errorf("Done lacks undo command:\n%s", done)
	}
}

func TestProjectRemovalRefusalsAndTUIMessages(t *testing.T) {
	tests := []struct {
		name string
		err  error
		req  app.Request
		want string
	}{
		{
			name: "no project",
			err:  &app.RefusalError{Kind: app.ErrNoProject, Err: errors.New("no project")},
			req:  app.Request{Action: app.ActionRemove, Key: "project:.agents/skills/solo"},
			want: "proyecto",
		},
		{
			name: "project linked",
			err:  &app.RefusalError{Kind: app.ErrProjectLinked, Err: errors.New("linked"), Related: []string{"/work/demo/.claude/skills/solo"}},
			req:  app.Request{Action: app.ActionRemove, Key: "project:.claude/skills/solo"},
			want: "enlace",
		},
		{
			name: "wrong project",
			err:  &app.RefusalError{Kind: app.ErrWrongProject, Err: errors.New("wrong"), Related: []string{"/work/a", "/work/b"}},
			req:  app.Request{Action: app.ActionUndo, Snapshot: "snap-1"},
			want: "/work/a",
		},
		{
			name: "cross device",
			err:  &app.RefusalError{Kind: app.ErrCrossDevice, Err: app.ErrCrossDevice, Related: []string{"/work/demo/.agents/skills/solo"}},
			req:  app.Request{Action: app.ActionRemove, Key: "project:.agents/skills/solo"},
			want: "sistemas de archivos distintos",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cliMsg, ok := text.Refusal(tt.err, tt.req)
			if !ok {
				t.Fatalf("Refusal returned ok=false for %v", tt.err)
			}
			if !strings.Contains(cliMsg, tt.want) {
				t.Errorf("Refusal = %q, want substring %q", cliMsg, tt.want)
			}
			tuiMsg, ok := text.TUIRefusal(tt.err, tt.req)
			if !ok {
				t.Fatalf("TUIRefusal returned ok=false for %v", tt.err)
			}
			if !strings.Contains(tuiMsg, tt.want) {
				t.Errorf("TUIRefusal = %q, want substring %q", tuiMsg, tt.want)
			}
		})
	}
}

func TestProjectRemovalRefusalEscapesControlAndANSIChars(t *testing.T) {
	tests := []struct {
		name string
		err  error
		req  app.Request
	}{
		{
			name: "wrong project dir",
			err:  &app.RefusalError{Kind: app.ErrWrongProject, Err: app.ErrWrongProject, Related: []string{"/work/evil\x1b[31mproj\nnext\u202e"}},
			req:  app.Request{Action: app.ActionUndo, Snapshot: "snap-1"},
		},
		{
			name: "project not found key",
			err:  &app.RefusalError{Kind: app.ErrNotFound, Err: app.ErrNotFound},
			req:  app.Request{Action: app.ActionRemove, Key: "project:.agents/skills/bad\x1b[2J\u202e"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cliMsg, ok := text.Refusal(tt.err, tt.req)
			if !ok {
				t.Fatalf("Refusal returned ok=false")
			}
			if strings.ContainsAny(cliMsg, "\x1b\n\r\u202e") {
				t.Errorf("Refusal left unescaped control/bidi chars: %q", cliMsg)
			}
			tuiMsg, ok := text.TUIRefusal(tt.err, tt.req)
			if !ok {
				t.Fatalf("TUIRefusal returned ok=false")
			}
			if strings.ContainsAny(tuiMsg, "\x1b\n\r\u202e") {
				t.Errorf("TUIRefusal left unescaped control/bidi chars: %q", tuiMsg)
			}
		})
	}
}
