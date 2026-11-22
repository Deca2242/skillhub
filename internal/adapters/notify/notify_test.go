package notify

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// TestNotifierContract runs the shared ports.Notifier contract suite against
// both the in-memory fake and the notify-send adapter (backed by fake ProcessRunner).
func TestNotifierContract(t *testing.T) {
	boom := errors.New("notify failed")

	impls := []struct {
		name string
		new  func(title, body string, fail bool) ports.Notifier
	}{
		{
			name: "fakes.Notifier",
			new: func(_ string, _ string, fail bool) ports.Notifier {
				n := fakes.NewNotifier()
				if fail {
					n.Fail = func(fakes.Call) error { return boom }
				}
				return n
			},
		},
		{
			name: "notify.Notifier",
			new: func(title, body string, fail bool) ports.Notifier {
				runner := fakes.NewProcessRunner()
				res := ports.Result{ExitCode: 0}
				var runErr error
				if fail {
					res = ports.Result{ExitCode: 1, Stderr: []byte("no dbus")}
					runErr = boom
				}
				runner.Expect(
					ports.Command{Name: "notify-send", Args: []string{"--", title, body}},
					res,
					runErr,
				)
				return New(runner)
			},
		},
	}

	for _, impl := range impls {
		t.Run(impl.name, func(t *testing.T) {
			t.Run("success", func(t *testing.T) {
				n := impl.new("Actualizaciones disponibles", "Hay 2 skills con cambios", false)
				if err := n.Notify(context.Background(), "Actualizaciones disponibles", "Hay 2 skills con cambios"); err != nil {
					t.Fatalf("Notify: %v", err)
				}
			})

			t.Run("failure", func(t *testing.T) {
				n := impl.new("t", "b", true)
				if err := n.Notify(context.Background(), "t", "b"); err == nil {
					t.Fatal("Notify() = nil, want error")
				}
			})
		})
	}
}

func TestNotifySendsExpectedCommand(t *testing.T) {
	runner := fakes.NewProcessRunner()
	wantCmd := ports.Command{
		Name: "notify-send",
		Args: []string{"--", "Actualizaciones disponibles", "Hay 2 skills con cambios"},
	}
	runner.Expect(wantCmd, ports.Result{ExitCode: 0}, nil)

	n := New(runner)
	if err := n.Notify(context.Background(), "Actualizaciones disponibles", "Hay 2 skills con cambios"); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	got := runner.Commands()
	if len(got) != 1 {
		t.Fatalf("Commands() count = %d, want 1", len(got))
	}
	if got[0].Name != wantCmd.Name || !reflect.DeepEqual(got[0].Args, wantCmd.Args) {
		t.Fatalf("Command = %+v, want %+v", got[0], wantCmd)
	}
	if unused := runner.Unused(); len(unused) != 0 {
		t.Fatalf("Unused expectations: %v", unused)
	}
}

func TestNotifyPropagatesErrors(t *testing.T) {
	boom := errors.New("runner failed")

	tests := []struct {
		name   string
		result ports.Result
		runErr error
	}{
		{name: "runner error", runErr: boom},
		{name: "non-zero exit", result: ports.Result{ExitCode: 1, Stderr: []byte("no dbus")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := fakes.NewProcessRunner()
			runner.Expect(
				ports.Command{Name: "notify-send", Args: []string{"--", "t", "b"}},
				tt.result,
				tt.runErr,
			)
			n := New(runner)
			err := n.Notify(context.Background(), "t", "b")
			if err == nil {
				t.Fatal("Notify() = nil, want error")
			}
			if tt.runErr != nil && !errors.Is(err, tt.runErr) {
				t.Fatalf("Notify() err = %v, want %v", err, tt.runErr)
			}
		})
	}
}
