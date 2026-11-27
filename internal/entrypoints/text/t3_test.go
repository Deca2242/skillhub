package text_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

func TestT3ClosePendingAndRecoverCleanupWarning(t *testing.T) {
	t.Run("ClosePending distinguishes confirmed from unconfirmed and escapes control chars", func(t *testing.T) {
		confirmed := text.ClosePending(true, "snap-1", errors.New("disk\x1b[31m full"))
		if !strings.Contains(confirmed, "ya quedó confirmada") || !strings.Contains(confirmed, "snap-1") || !strings.Contains(confirmed, "skillhub recover") {
			t.Errorf("confirmed ClosePending = %q", confirmed)
		}
		if strings.Contains(confirmed, "\x1b") {
			t.Errorf("confirmed ClosePending leaked ANSI escape: %q", confirmed)
		}

		unconfirmed := text.ClosePending(false, "", errors.New("journal\x1b[31m unreadable"))
		if !strings.Contains(unconfirmed, "no se pudo confirmar") || !strings.Contains(unconfirmed, "skillhub recover") {
			t.Errorf("unconfirmed ClosePending = %q", unconfirmed)
		}
		if strings.Contains(unconfirmed, "\x1b") {
			t.Errorf("unconfirmed ClosePending leaked ANSI escape: %q", unconfirmed)
		}
	})

	t.Run("Recovery of committed undo does not advertise undo of itself", func(t *testing.T) {
		got := text.Recovery(app.ManagedRecovery{Found: true, Resolved: true, Committed: true, Undo: true, SnapshotID: "snap-u"})
		if strings.Contains(got, "Se deshace") || strings.Contains(got, "snap-u") {
			t.Errorf("Recovery of committed undo advertised undo: %q", got)
		}
		applied := text.Recovery(app.ManagedRecovery{Found: true, Resolved: true, Committed: true, Undo: false, SnapshotID: "snap-a"})
		if !strings.Contains(applied, "Se deshace con su snapshot snap-a") {
			t.Errorf("Recovery of committed applied lost undo hint: %q", applied)
		}
	})

	t.Run("UndoLong mentions latest eligible operation", func(t *testing.T) {
		if !strings.Contains(text.UndoLong, "elegible") {
			t.Errorf("UndoLong = %q, want 'elegible'", text.UndoLong)
		}
	})

	t.Run("RecoverCleanupWarning strips ErrRecoverCleanup prefix and escapes control chars", func(t *testing.T) {
		err := fmt.Errorf("%w: empty the install staging: busy\x1b[31m", app.ErrRecoverCleanup)
		got := text.RecoverCleanupWarning(err)
		if !strings.Contains(got, "aviso:") || !strings.Contains(got, "se recuperó") {
			t.Errorf("RecoverCleanupWarning = %q", got)
		}
		if strings.Contains(got, "\x1b") {
			t.Errorf("RecoverCleanupWarning leaked ANSI escape: %q", got)
		}
	})
}
