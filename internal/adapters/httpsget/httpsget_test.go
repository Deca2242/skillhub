package httpsget_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	"skillhub/internal/adapters/httpsget"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// curl is the exact command Get must run for url and maxBytes.
func curl(url string, maxBytes int64) ports.Command {
	return ports.Command{Name: "curl", Args: []string{
		"-q", "--silent", "--show-error", "--fail", "--max-time", "30",
		"--proto", "=https", "--location", "--max-redirs", "0",
		"--max-filesize", strconv.FormatInt(maxBytes, 10), "--globoff", url,
	}}
}

const index = "https://uizze.sh/.well-known/agent-skills/index.json"

func get(t *testing.T, url string, maxBytes int64, res ports.Result, runErr error) ([]byte, error, *fakes.ProcessRunner) {
	t.Helper()
	runner := fakes.NewProcessRunner()
	runner.Expect(curl(url, maxBytes), res, runErr)
	c, err := httpsget.New(runner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body, err := c.Get(t.Context(), url, maxBytes)
	return body, err, runner
}

func TestGetReturnsTheBodyOfOneHTTPSRequest(t *testing.T) {
	want := []byte(`{"skills":[]}`)
	body, err, runner := get(t, index, 1<<20, ports.Result{Stdout: want}, nil)
	if err != nil {
		t.Fatalf("Get = %v", err)
	}
	if !bytes.Equal(body, want) {
		t.Errorf("body = %q, want %q", body, want)
	}
	if got := runner.Commands(); len(got) != 1 || !slices.Equal(got[0].Args, curl(index, 1<<20).Args) {
		t.Errorf("commands = %v, want exactly one curl with the bounded options", got)
	}
}

func TestGetClassifiesEveryFailure(t *testing.T) {
	tests := []struct {
		name   string
		res    ports.Result
		runErr error
		reason httpsget.Reason
		status int
	}{
		{"curl missing", ports.Result{ExitCode: -1}, errors.New(`exec: "curl": executable file not found in $PATH`), httpsget.ReasonCurlMissing, 0},
		{"host not resolved", ports.Result{ExitCode: 6, Stderr: []byte("curl: (6) Could not resolve host: uizze.sh")}, nil, httpsget.ReasonOffline, 0},
		{"timeout", ports.Result{ExitCode: 28}, nil, httpsget.ReasonOffline, 0},
		{"not found", ports.Result{ExitCode: 22, Stderr: []byte("curl: (22) The requested URL returned error: 404")}, nil, httpsget.ReasonHTTP, 404},
		{"rate limited", ports.Result{ExitCode: 22, Stderr: []byte("curl: (22) The requested URL returned error: 429")}, nil, httpsget.ReasonRateLimited, 429},
		{"redirect refused", ports.Result{ExitCode: 47}, nil, httpsget.ReasonHTTP, 0},
		{"declared size over the limit", ports.Result{ExitCode: 63}, nil, httpsget.ReasonTooLarge, 0},
		{"body over the limit", ports.Result{Stdout: bytes.Repeat([]byte("x"), 11)}, nil, httpsget.ReasonTooLarge, 0},
		{"any other curl error", ports.Result{ExitCode: 3}, nil, httpsget.ReasonFailed, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err, _ := get(t, index, 10, tt.res, tt.runErr)
			if body != nil {
				t.Errorf("body = %q, want none", body)
			}
			e, ok := errors.AsType[*httpsget.Error](err)
			if !ok {
				t.Fatalf("Get = %v (%T), want *httpsget.Error", err, err)
			}
			if e.Reason != tt.reason || e.Status != tt.status || e.URL != index {
				t.Errorf("Get = reason %q status %d url %q, want %q %d %q", e.Reason, e.Status, e.URL, tt.reason, tt.status, index)
			}
			if e.CatalogReason() != string(tt.reason) || e.HTTPStatus() != tt.status {
				t.Errorf("CatalogReason/HTTPStatus = %q/%d", e.CatalogReason(), e.HTTPStatus())
			}
		})
	}
}

func TestGetRefusesAnythingButAPlainHTTPSURLWithoutRunningCurl(t *testing.T) {
	for _, url := range []string{
		"http://uizze.sh/.well-known/agent-skills/index.json",
		"file:///etc/passwd",
		"https://user:pw@uizze.sh/index.json",
		"https:///index.json",
		"https://uizze.sh/a b",
		"https://uizze.sh/a\nb",
		`https://uizze.sh/a\b`,
		"https://uizze.sh/index.json#x",
		"-K/etc/passwd",
		"",
	} {
		t.Run(url, func(t *testing.T) {
			runner := fakes.NewProcessRunner()
			c, err := httpsget.New(runner)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Get(t.Context(), url, 10)
			if e, ok := errors.AsType[*httpsget.Error](err); !ok || e.Reason != httpsget.ReasonBadURL {
				t.Errorf("Get(%q) = %v, want ReasonBadURL", url, err)
			}
			if got := runner.Commands(); len(got) != 0 {
				t.Errorf("ran %v, want nothing", got)
			}
		})
	}
	runner := fakes.NewProcessRunner()
	c, _ := httpsget.New(runner)
	if _, err := c.Get(t.Context(), index, 0); err == nil || len(runner.Commands()) != 0 {
		t.Errorf("Get with no size limit = %v, ran %v; want a refusal and nothing run", err, runner.Commands())
	}
}

func TestGetKeepsACanceledContext(t *testing.T) {
	_, err, _ := get(t, index, 10, ports.Result{ExitCode: -1}, context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Get = %v, want context.Canceled", err)
	}
	if _, ok := errors.AsType[*httpsget.Error](err); ok {
		t.Errorf("Get = %v, want the cancellation, not a curl failure", err)
	}
}

func TestNewNeedsARunner(t *testing.T) {
	if _, err := httpsget.New(nil); err == nil {
		t.Error("New(nil) = nil error")
	}
}
