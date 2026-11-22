// Package github implements ports.GitHubPort using the gh CLI through
// ports.ProcessRunner, with conditional requests cached by ETag.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
)

// Client implements ports.GitHubPort over gh api.
type Client struct {
	runner ports.ProcessRunner
	paths  paths.Paths
	cache  *etagCache
}

// New creates a Client that invokes gh via runner and caches ETags at
// p.ETagsFile().
func New(runner ports.ProcessRunner, p paths.Paths) (*Client, error) {
	if runner == nil {
		return nil, errors.New("github: process runner is required")
	}
	etagsPath := p.ETagsFile()
	if err := p.Check(etagsPath); err != nil {
		return nil, fmt.Errorf("github: invalid etags path: %w", err)
	}
	return &Client{
		runner: runner,
		paths:  p,
		cache:  newETagCache(etagsPath, p.Guard()),
	}, nil
}

// DefaultBranch implements ports.GitHubPort.
func (c *Client) DefaultBranch(ctx context.Context, repo string) (string, error) {
	if err := validateRepo(repo); err != nil {
		return "", err
	}
	endpoint := "repos/" + repo
	body, err := c.get(ctx, endpoint, "", true)
	if err != nil {
		return "", err
	}
	var resp struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("github: decode repo %s: %w", repo, err)
	}
	if resp.DefaultBranch == "" {
		return "", fmt.Errorf("github: repo %s has no default_branch", repo)
	}
	return resp.DefaultBranch, nil
}

// ResolveCommit implements ports.GitHubPort.
func (c *Client) ResolveCommit(ctx context.Context, repo, ref string) (string, error) {
	if err := validateRepo(repo); err != nil {
		return "", err
	}
	if err := validateRef(ref); err != nil {
		return "", err
	}
	endpoint := "repos/" + repo + "/commits/" + escapeRef(ref)
	body, err := c.get(ctx, endpoint, "application/vnd.github.sha", false)
	if err != nil {
		return "", c.disambiguateNotFound(ctx, repo, err)
	}
	sha := strings.TrimSpace(string(body))
	if !isHexSHA40(sha) {
		return "", fmt.Errorf("github: %s@%s returned invalid sha %q: %w", repo, ref, sha, ports.ErrRefNotFound)
	}
	return strings.ToLower(sha), nil
}

// Tree implements ports.GitHubPort.
func (c *Client) Tree(ctx context.Context, repo, commit, subPath string) (treehash.Tree, error) {
	if err := validateRepo(repo); err != nil {
		return treehash.Tree{}, err
	}
	if !isHexSHA40(commit) {
		return treehash.Tree{}, fmt.Errorf("github: invalid commit sha %q: %w", commit, ports.ErrRefNotFound)
	}
	cleanPath, err := normalizeTreePath(subPath)
	if err != nil {
		return treehash.Tree{}, err
	}

	rootTreeSHA, err := c.commitTreeSHA(ctx, repo, commit)
	if err != nil {
		return treehash.Tree{}, err
	}

	endpoint := fmt.Sprintf("repos/%s/git/trees/%s?recursive=1", repo, rootTreeSHA)
	body, err := c.get(ctx, endpoint, "", false)
	if err != nil {
		return treehash.Tree{}, c.disambiguateNotFound(ctx, repo, err)
	}
	var resp rawTreeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return treehash.Tree{}, fmt.Errorf("github: decode tree %s@%s: %w", repo, commit, err)
	}
	if !resp.Truncated {
		return buildTreeFromRecursive(resp, rootTreeSHA, cleanPath)
	}
	return c.walkTruncatedTree(ctx, repo, rootTreeSHA, cleanPath)
}

// TreeBySHA implements ports.GitHubPort.
func (c *Client) TreeBySHA(ctx context.Context, repo, treeSHA string) (treehash.Tree, error) {
	if err := validateRepo(repo); err != nil {
		return treehash.Tree{}, err
	}
	if !isHexSHA40(treeSHA) {
		return treehash.Tree{}, fmt.Errorf("github: invalid tree sha %q: %w", treeSHA, ports.ErrRefNotFound)
	}
	treeSHA = strings.ToLower(treeSHA)
	endpoint := fmt.Sprintf("repos/%s/git/trees/%s?recursive=1", repo, treeSHA)
	body, err := c.get(ctx, endpoint, "", false)
	if err != nil {
		return treehash.Tree{}, c.disambiguateNotFound(ctx, repo, err)
	}
	var resp rawTreeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return treehash.Tree{}, fmt.Errorf("github: decode tree %s@%s: %w", repo, treeSHA, err)
	}
	if !resp.Truncated {
		return buildTreeFromRecursive(resp, treeSHA, "")
	}
	return c.walkTruncatedTree(ctx, repo, treeSHA, "")
}

func (c *Client) commitTreeSHA(ctx context.Context, repo, commit string) (string, error) {
	endpoint := fmt.Sprintf("repos/%s/git/commits/%s", repo, commit)
	body, err := c.get(ctx, endpoint, "", false)
	if err != nil {
		return "", c.disambiguateNotFound(ctx, repo, err)
	}
	var resp struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("github: decode git commit %s@%s: %w", repo, commit, err)
	}
	if !isHexSHA40(resp.Tree.SHA) {
		return "", fmt.Errorf("github: git commit %s@%s returned invalid tree sha %q: %w", repo, commit, resp.Tree.SHA, ports.ErrRefNotFound)
	}
	return strings.ToLower(resp.Tree.SHA), nil
}

// Blob implements ports.GitHubPort.
func (c *Client) Blob(ctx context.Context, repo, sha string) ([]byte, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if !isHexSHA40(sha) {
		return nil, fmt.Errorf("github: invalid blob sha %q: %w", sha, ports.ErrRefNotFound)
	}
	endpoint := "repos/" + repo + "/git/blobs/" + sha
	body, err := c.get(ctx, endpoint, "", false)
	if err != nil {
		return nil, c.disambiguateNotFound(ctx, repo, err)
	}
	var resp struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("github: decode blob %s/%s: %w", repo, sha, err)
	}
	if resp.Encoding != "base64" {
		return nil, fmt.Errorf("github: blob %s/%s has unsupported encoding %q", repo, sha, resp.Encoding)
	}
	decoded, err := base64.StdEncoding.DecodeString(resp.Content)
	if err != nil {
		return nil, fmt.Errorf("github: decode base64 blob %s/%s: %w", repo, sha, err)
	}
	return decoded, nil
}

func (c *Client) get(ctx context.Context, endpoint, accept string, isRepoRoot bool) ([]byte, error) {
	args := []string{"api", "-i"}
	if accept != "" {
		args = append(args, "-H", "Accept: "+accept)
	}
	cacheable := isETagCachedEndpoint(endpoint)
	var cached cachedEntry
	var hasCached bool
	if cacheable {
		cached, hasCached = c.cache.get(endpoint)
		if hasCached && cached.ETag != "" {
			args = append(args, "-H", "If-None-Match: "+cached.ETag)
		}
	}
	args = append(args, endpoint)

	res, err := c.runner.Run(ctx, ports.Command{
		Name: "gh",
		Args: args,
		Dir:  c.paths.Home(),
	})
	if err != nil {
		return nil, fmt.Errorf("gh api %s: %w", endpoint, err)
	}

	resp, hasHTTP := parseHTTPResponse(res.Stdout)
	if hasHTTP && resp.status == 304 {
		if cacheable && hasCached {
			return append([]byte(nil), cached.Body...), nil
		}
		return nil, fmt.Errorf("gh api %s: unexpected HTTP 304 without cached entry", endpoint)
	}

	if res.ExitCode != 0 {
		if hasHTTP && resp.status >= 400 {
			return nil, mapHTTPError(endpoint, resp, res.Stderr, isRepoRoot)
		}
		return nil, mapCLIError(endpoint, res.Stderr, res.ExitCode)
	}

	if hasHTTP {
		if resp.status >= 200 && resp.status < 300 {
			if cacheable && resp.etag != "" {
				_ = c.cache.set(endpoint, resp.etag, resp.body)
			}
			return resp.body, nil
		}
		return nil, mapHTTPError(endpoint, resp, res.Stderr, isRepoRoot)
	}
	return res.Stdout, nil
}

func (c *Client) disambiguateNotFound(ctx context.Context, repo string, err error) error {
	if !errors.Is(err, ports.ErrRefNotFound) {
		return err
	}
	if _, repoErr := c.DefaultBranch(ctx, repo); errors.Is(repoErr, ports.ErrRepoNotFound) {
		return repoErr
	}
	return err
}

func validateRepo(repo string) error {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || !isValidRepoPart(owner) || !isValidRepoPart(name) {
		return fmt.Errorf("invalid repository %q: %w", repo, ports.ErrRepoNotFound)
	}
	return nil
}

func isValidRepoPart(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlphaNum && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

func validateRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("github: empty ref: %w", ports.ErrRefNotFound)
	}
	for _, seg := range strings.Split(ref, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("github: invalid ref %q: %w", ref, ports.ErrRefNotFound)
		}
	}
	return nil
}

func isHexSHA40(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

type httpResponse struct {
	status  int
	headers map[string]string
	etag    string
	body    []byte
}

func parseHTTPResponse(raw []byte) (httpResponse, bool) {
	rest := raw
	for bytes.HasPrefix(rest, []byte("HTTP/")) {
		headerBlock, body, found := splitHeaderBody(rest)
		if !found {
			return httpResponse{}, false
		}
		status, headers, ok := parseHeaders(headerBlock)
		if !ok {
			return httpResponse{}, false
		}
		// Follow intermediate 3xx redirect headers if gh included them, except 304.
		if status >= 300 && status < 400 && status != 304 && bytes.HasPrefix(body, []byte("HTTP/")) {
			rest = body
			continue
		}
		return httpResponse{
			status:  status,
			headers: headers,
			etag:    headers["etag"],
			body:    body,
		}, true
	}
	return httpResponse{}, false
}

func splitHeaderBody(b []byte) (headers, body []byte, ok bool) {
	// gh api -i ends the status line with \n and headers + blank line with \r\n,
	// so a headerless response separates status and body with \n\r\n.
	type sep struct {
		idx int
		len int
	}
	candidates := []sep{
		{idx: bytes.Index(b, []byte("\r\n\r\n")), len: 4},
		{idx: bytes.Index(b, []byte("\n\r\n")), len: 3},
		{idx: bytes.Index(b, []byte("\n\n")), len: 2},
	}
	best := sep{idx: -1}
	for _, c := range candidates {
		if c.idx >= 0 && (best.idx < 0 || c.idx < best.idx) {
			best = c
		}
	}
	if best.idx < 0 {
		return nil, nil, false
	}
	return b[:best.idx], b[best.idx+best.len:], true
}

func parseHeaders(headerBlock []byte) (int, map[string]string, bool) {
	lines := strings.Split(strings.ReplaceAll(string(headerBlock), "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return 0, nil, false
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return 0, nil, false
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, nil, false
	}
	headers := make(map[string]string, len(lines)-1)
	for _, line := range lines[1:] {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		headers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return code, headers, true
}

func mapHTTPError(endpoint string, resp httpResponse, stderr []byte, isRepoRoot bool) error {
	combined := strings.ToLower(string(resp.body) + " " + string(stderr))
	switch resp.status {
	case 401:
		return fmt.Errorf("gh api %s (HTTP 401): %w", endpoint, ports.ErrNotAuthenticated)
	case 429:
		return fmt.Errorf("gh api %s (HTTP 429): %w", endpoint, ports.ErrRateLimited)
	case 403:
		if resp.headers["x-ratelimit-remaining"] == "0" ||
			resp.headers["retry-after"] != "" ||
			strings.Contains(combined, "rate limit") {
			return fmt.Errorf("gh api %s (HTTP 403): %w", endpoint, ports.ErrRateLimited)
		}
		if isAuthMessage(combined) {
			return fmt.Errorf("gh api %s (HTTP 403): %w", endpoint, ports.ErrNotAuthenticated)
		}
		return fmt.Errorf("gh api %s: HTTP 403: %s", endpoint, strings.TrimSpace(string(stderr)))
	case 404:
		if isRepoRoot {
			return fmt.Errorf("gh api %s (HTTP 404): %w", endpoint, ports.ErrRepoNotFound)
		}
		return fmt.Errorf("gh api %s (HTTP 404): %w", endpoint, ports.ErrRefNotFound)
	case 422:
		// A tree SHA GitHub cannot read is a tree the repository no longer
		// holds, the same as a 404.
		if strings.Contains(string(resp.body), "No commit found") || strings.Contains(string(stderr), "No commit found") ||
			strings.Contains(endpoint, "/git/trees/") {
			return fmt.Errorf("gh api %s (HTTP 422): %w", endpoint, ports.ErrRefNotFound)
		}
		return fmt.Errorf("gh api %s: HTTP 422: %s", endpoint, strings.TrimSpace(string(stderr)))
	default:
		return fmt.Errorf("gh api %s: HTTP %d: %s", endpoint, resp.status, strings.TrimSpace(string(stderr)))
	}
}

func mapCLIError(endpoint string, stderr []byte, exitCode int) error {
	msg := strings.ToLower(string(stderr))
	if isAuthMessage(msg) {
		return fmt.Errorf("gh api %s: %w", endpoint, ports.ErrNotAuthenticated)
	}
	if strings.Contains(msg, "rate limit") || strings.Contains(msg, "429") {
		return fmt.Errorf("gh api %s: %w", endpoint, ports.ErrRateLimited)
	}
	return fmt.Errorf("gh api %s exited with %d: %s", endpoint, exitCode, strings.TrimSpace(string(stderr)))
}

func isAuthMessage(lower string) bool {
	return strings.Contains(lower, "gh auth login") ||
		strings.Contains(lower, "not logged in") ||
		strings.Contains(lower, "requires authentication") ||
		strings.Contains(lower, "bad credentials") ||
		strings.Contains(lower, "set gh_token")
}
