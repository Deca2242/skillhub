// Package skillssh searches the public skills.sh directory (S14). It runs
// curl through the process runner, so no package of skillhub opens a socket
// itself, and it only reads: nothing here installs anything.
//
// The endpoint is the one npx skills find uses, GET /api/search?q=&limit=,
// which needs no token and answers
// {"skills":[{"id","source","skillId","name","installs"}, ...]} with at most
// 100 results (L76).
package skillssh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"skillhub/internal/shared/ports"
)

// DefaultBaseURL is skills.sh itself. The composition root may replace it
// with SKILLS_API_URL, the variable the official CLI reads.
const DefaultBaseURL = "https://skills.sh"

// MaxLimit is the most results skills.sh returns for one search.
const MaxLimit = 100

// ErrInvalidBaseURL reports a base URL that is not a plain https origin.
var ErrInvalidBaseURL = errors.New("invalid skills.sh base URL")

// curlArgs bound the request: an error status fails, nothing but https is
// spoken, a redirect fails (--max-redirs acts only with --location, so 0
// turns any redirect into error 47 instead of taking its body), no
// ~/.curlrc is read (-q, which must come first), and the whole call takes at most 15 s.
var curlArgs = []string{
	"-q", "--silent", "--show-error", "--fail", "--max-time", "15",
	"--proto", "=https", "--location", "--max-redirs", "0",
}

// Reason says why a search failed. The codes are stable: the facade maps
// them to its own errors through CatalogReason.
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
	// ReasonBadResponse is a body that is not the expected JSON.
	ReasonBadResponse Reason = "bad_response"
	// ReasonBadQuery is a query shorter than two characters, which
	// skills.sh refuses; no request is made.
	ReasonBadQuery Reason = "bad_query"
	// ReasonFailed is any other curl failure.
	ReasonFailed Reason = "failed"
)

// Error is a failed search.
type Error struct {
	Reason Reason
	// Status is the HTTP status, when the server sent one.
	Status int
	// ExitCode is curl's exit code, when it ran.
	ExitCode int
	Err      error
}

func (e *Error) Error() string {
	msg := "skills.sh search: " + string(e.Reason)
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

// Client is a ports.Catalog over curl.
type Client struct {
	runner ports.ProcessRunner
	base   string
}

var _ ports.Catalog = (*Client)(nil)

// New builds a Client. baseURL "" is DefaultBaseURL; any other must be an
// https URL with a host and no credentials, query or fragment.
func New(runner ports.ProcessRunner, baseURL string) (*Client, error) {
	if runner == nil {
		return nil, errors.New("skillssh: process runner is required")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(baseURL, "#") ||
		strings.ContainsFunc(baseURL, unicode.IsControl) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidBaseURL, baseURL)
	}
	return &Client{runner: runner, base: strings.TrimRight(baseURL, "/")}, nil
}

// Search implements ports.Catalog. limit is kept within 1 and MaxLimit.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]ports.CatalogSkill, error) {
	query = strings.TrimSpace(query)
	if utf8.RuneCountInString(query) < 2 {
		return nil, &Error{Reason: ReasonBadQuery, Err: fmt.Errorf("query %q is shorter than two characters", query)}
	}
	limit = min(max(limit, 1), MaxLimit)
	target := c.base + "/api/search?" + url.Values{"q": {query}, "limit": {strconv.Itoa(limit)}}.Encode()
	args := append(append([]string(nil), curlArgs...), target)
	res, err := c.runner.Run(ctx, ports.Command{Name: "curl", Args: args})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("skills.sh search: %w", err)
		}
		return nil, &Error{Reason: ReasonCurlMissing, ExitCode: res.ExitCode, Err: err}
	}
	if res.ExitCode != 0 {
		return nil, curlFailure(res)
	}
	skills, err := decode(res.Stdout)
	if err != nil {
		return nil, &Error{Reason: ReasonBadResponse, Err: err}
	}
	if len(skills) > limit {
		skills = skills[:limit]
	}
	return skills, nil
}

var statusRE = regexp.MustCompile(`returned error: (\d{3})`)

// curlFailure classifies a curl that exited with an error code
// (curl(1), EXIT CODES).
func curlFailure(res ports.Result) error {
	stderr := strings.TrimSpace(string(res.Stderr))
	cause := fmt.Errorf("curl exited with %d: %s", res.ExitCode, stderr)
	e := &Error{Reason: ReasonFailed, ExitCode: res.ExitCode, Err: cause}
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
	}
	return e
}

// decode reads the body strictly: the top level must be an object with a
// skills list and nothing after it. An entry with a missing or mistyped
// field, or an unsafe value, is dropped; the others are kept in order.
func decode(body []byte) ([]ports.CatalogSkill, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var top map[string]json.RawMessage
	if err := dec.Decode(&top); err != nil {
		return nil, fmt.Errorf("decode the response: %w", err)
	}
	if dec.More() {
		return nil, errors.New("decode the response: data after the JSON object")
	}
	raw, ok := top["skills"]
	if !ok {
		return nil, errors.New("decode the response: no skills list")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || entries == nil {
		return nil, errors.New("decode the response: skills is not a list")
	}
	out := make([]ports.CatalogSkill, 0, len(entries))
	for _, entry := range entries {
		if s, ok := decodeSkill(entry); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

func decodeSkill(raw json.RawMessage) (ports.CatalogSkill, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return ports.CatalogSkill{}, false
	}
	var s ports.CatalogSkill
	ok := stringField(fields, "id", &s.ID) &&
		stringField(fields, "source", &s.Source) &&
		stringField(fields, "skillId", &s.SkillID) &&
		stringField(fields, "name", &s.Name) &&
		installsField(fields, &s.Installs)
	if !ok || !safeRef(s.Source) || !safeRef(s.SkillID) {
		return ports.CatalogSkill{}, false
	}
	return s, true
}

// stringField reads a non-empty string without control characters, so
// nothing the server sends can drive the terminal.
func stringField(fields map[string]json.RawMessage, key string, dst *string) bool {
	raw, ok := fields[key]
	if !ok || len(raw) == 0 || raw[0] != '"' {
		return false
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false
	}
	return *dst != "" && utf8.ValidString(*dst) && !strings.ContainsFunc(*dst, unicode.IsControl)
}

// installsField reads a whole number between 0 and the largest int.
func installsField(fields map[string]json.RawMessage, dst *int) bool {
	raw, ok := fields["installs"]
	// json.Number also takes a quoted number; only a bare one is a number.
	if !ok || len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return false
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return false
	}
	v, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil || v < 0 || v > math.MaxInt {
		return false
	}
	*dst = int(v)
	return true
}

// safeRef refuses what could leave a folder or a repository: "..", a
// leading slash or a backslash. Control characters are already refused.
func safeRef(s string) bool {
	return !strings.Contains(s, "..") && !strings.HasPrefix(s, "/") && !strings.Contains(s, `\`)
}
