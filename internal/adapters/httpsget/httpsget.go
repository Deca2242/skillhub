// Package httpsget reads one https URL by running curl through the process
// runner (ports.HTTPSFetcher), so no package of skillhub opens a socket
// itself. Well-known skill sources (/.well-known/agent-skills/, S14, L84) are
// read through it. It only reads.
package httpsget

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"skillhub/internal/shared/ports"
)

// curlArgs bound every request: an error status fails, nothing but https is
// spoken, a redirect fails (--max-redirs acts only with --location, so 0
// turns any redirect into error 47 instead of taking its body), no
// ~/.curlrc is read (-q, which must come first), the whole call takes at most 30 s, and
// the URL is taken literally (--globoff: no [] or {} expansion, so one URL
// an index names is exactly one request).
var curlArgs = []string{
	"-q", "--silent", "--show-error", "--fail", "--max-time", "30",
	"--proto", "=https", "--location", "--max-redirs", "0",
}

// Reason says why a request failed. The codes are stable and the ones the
// skills.sh catalog uses where they mean the same; the facade reads them
// through CatalogReason.
type Reason string

const (
	// ReasonCurlMissing is a curl that could not be started.
	ReasonCurlMissing Reason = "curl_missing"
	// ReasonOffline is a host that could not be resolved or reached in time.
	ReasonOffline Reason = "offline"
	// ReasonRateLimited is HTTP 429.
	ReasonRateLimited Reason = "rate_limited"
	// ReasonHTTP is any other HTTP error status, or a refused redirect.
	ReasonHTTP Reason = "http"
	// ReasonTooLarge is a body larger than the limit the caller set.
	ReasonTooLarge Reason = "too_large"
	// ReasonBadURL is a URL that is not a plain https URL; nothing is run.
	ReasonBadURL Reason = "bad_url"
	// ReasonFailed is any other curl failure.
	ReasonFailed Reason = "failed"
)

// Error is a failed request.
type Error struct {
	Reason Reason
	URL    string
	// Status is the HTTP status, when the server sent one.
	Status int
	// ExitCode is curl's exit code, when it ran.
	ExitCode int
	Err      error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("get %s: %s", e.URL, e.Reason)
	if e.Status != 0 {
		msg += fmt.Sprintf(" (HTTP %d)", e.Status)
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// CatalogReason is the stable code of the failure.
func (e *Error) CatalogReason() string { return string(e.Reason) }

// HTTPStatus is the HTTP status the server sent, or 0.
func (e *Error) HTTPStatus() int { return e.Status }

// Client is a ports.HTTPSFetcher over curl.
type Client struct {
	runner ports.ProcessRunner
}

var _ ports.HTTPSFetcher = (*Client)(nil)

// New builds a Client.
func New(runner ports.ProcessRunner) (*Client, error) {
	if runner == nil {
		return nil, errors.New("httpsget: process runner is required")
	}
	return &Client{runner: runner}, nil
}

// Get implements ports.HTTPSFetcher. curl refuses a body whose declared size
// is over maxBytes (--max-filesize), and Get refuses one that turns out
// larger anyway.
func (c *Client) Get(ctx context.Context, target string, maxBytes int64) ([]byte, error) {
	if err := checkURL(target); err != nil {
		return nil, &Error{Reason: ReasonBadURL, URL: target, Err: err}
	}
	if maxBytes <= 0 {
		return nil, &Error{Reason: ReasonBadURL, URL: target, Err: fmt.Errorf("size limit %d is not positive", maxBytes)}
	}
	args := append(append([]string(nil), curlArgs...),
		"--max-filesize", strconv.FormatInt(maxBytes, 10), "--globoff", target)
	res, err := c.runner.Run(ctx, ports.Command{Name: "curl", Args: args})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("get %s: %w", target, err)
		}
		return nil, &Error{Reason: ReasonCurlMissing, URL: target, ExitCode: res.ExitCode, Err: err}
	}
	if res.ExitCode != 0 {
		return nil, curlFailure(target, res)
	}
	if int64(len(res.Stdout)) > maxBytes {
		return nil, &Error{Reason: ReasonTooLarge, URL: target, Err: fmt.Errorf("%d bytes, the limit is %d", len(res.Stdout), maxBytes)}
	}
	return res.Stdout, nil
}

// checkURL accepts an absolute https URL with a host and no credentials,
// fragment, space, backslash or control character.
func checkURL(target string) error {
	if strings.ContainsFunc(target, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) || r == '\\' }) {
		return errors.New("the URL holds a space, a backslash or a control character")
	}
	u, err := url.Parse(target)
	switch {
	case err != nil:
		return fmt.Errorf("parse the URL: %w", err)
	case u.Scheme != "https":
		return errors.New("the URL is not https")
	case u.Host == "" || u.Hostname() == "":
		return errors.New("the URL has no host")
	case u.User != nil:
		return errors.New("the URL holds credentials")
	case u.Fragment != "" || strings.Contains(target, "#"):
		return errors.New("the URL holds a fragment")
	}
	return nil
}

var statusRE = regexp.MustCompile(`returned error: (\d{3})`)

// curlFailure classifies a curl that exited with an error code (curl(1),
// EXIT CODES).
func curlFailure(target string, res ports.Result) error {
	stderr := strings.TrimSpace(string(res.Stderr))
	e := &Error{Reason: ReasonFailed, URL: target, ExitCode: res.ExitCode,
		Err: fmt.Errorf("curl exited with %d: %s", res.ExitCode, stderr)}
	switch res.ExitCode {
	case 5, 6, 7, 28, 35, 52, 55, 56:
		// Proxy or host not resolved, no connection, timeout, TLS handshake,
		// empty reply, send or receive failure.
		e.Reason = ReasonOffline
	case 22:
		e.Reason = ReasonHTTP
		if m := statusRE.FindStringSubmatch(stderr); m != nil {
			e.Status, _ = strconv.Atoi(m[1]) //nolint:errcheck // three digits always parse.
		}
		if e.Status == 429 {
			e.Reason = ReasonRateLimited
		}
	case 47:
		// A redirect, which --max-redirs 0 refuses.
		e.Reason = ReasonHTTP
	case 63:
		// --max-filesize refused the declared size.
		e.Reason = ReasonTooLarge
	}
	return e
}
