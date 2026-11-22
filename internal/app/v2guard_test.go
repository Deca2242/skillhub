package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/manage/engine"
	"skillhub/internal/updates"
)

// The background check saves its notification marks under the state lock; a
// pending version 2 operation stops it before anything is checked, sent or
// saved.
func TestCheckRefusesAPendingVersion2OperationWithoutWriting(t *testing.T) {
	for name, opts := range map[string]app.CheckOptions{
		"check":            {},
		"background check": {IfDue: true, Notify: true, Message: message},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.standard()
			p := f.home.Paths()
			record := engine.RecordFile(p)
			if err := os.MkdirAll(filepath.Dir(record), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(record, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := f.app.Check(ctx, opts)

			if !errors.Is(err, updates.ErrV2OperationPending) {
				t.Fatalf("Check() = %v, want ErrV2OperationPending", err)
			}
			if sent := f.notifier.Sent(); len(sent) != 0 {
				t.Errorf("a notification was sent: %v", sent)
			}
			if _, err := os.Stat(p.StateFile()); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("state.json was written: %v", err)
			}
			if entries, _ := os.ReadDir(p.StagingDir()); len(entries) != 0 {
				t.Errorf("staging was written: %v", entries)
			}
		})
	}
}
