package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/staging"
)

// A website can publish skills at /.well-known/agent-skills/ (RFC 8615;
// S14, L84). npx skills 1.9.0 installs them (src/providers/wellknown.ts,
// tag v1.9.0), and so does skillhub, with the same review, plan and undo as
// a GitHub install. What this file reads and checks is what that provider
// reads and checks; where skillhub is stricter, the comment says so.

// Download limits of a well-known skill. A skill is a handful of text
// files; anything beyond these is refused rather than downloaded.
const (
	// MaxIndexBytes bounds the index.json download.
	MaxIndexBytes int64 = 1 << 20
	// MaxWebFiles bounds the files of one skill, SKILL.md included.
	MaxWebFiles = 500
	// MaxWebFileBytes bounds each file.
	MaxWebFileBytes int64 = 8 << 20
	// MaxWebBytes bounds all the files of one skill together.
	MaxWebBytes int64 = 32 << 20
)

var (
	// ErrNoIndex reports a site that publishes no valid index where npx
	// skills looks for one.
	ErrNoIndex = errors.New("the site publishes no well-known skills index")
	// ErrUnsafeFile reports a file the index names that skillhub will not
	// download or write: a path that is not a plain relative path of safe
	// characters, or an artifact on another host or outside the site.
	ErrUnsafeFile = errors.New("the index names a file skillhub cannot download or write safely")
	// ErrWebLimits reports a skill beyond the download limits.
	ErrWebLimits = errors.New("the skill exceeds the download limits")
	// ErrDigestMismatch reports an artifact whose content is not the digest
	// the index publishes.
	ErrDigestMismatch = errors.New("the downloaded skill does not match the index digest")
	// ErrUnsupportedArtifact reports a skill published as an archive, which
	// skillhub does not unpack.
	ErrUnsupportedArtifact = errors.New("the index publishes the skill as an archive")
)

const (
	// discoverySchemaV2 is DISCOVERY_SCHEMA_V2 (wellknown.ts:10).
	discoverySchemaV2 = "https://schemas.agentskills.io/discovery/0.2.0/schema.json"
	indexFile         = "index.json"
	typeSkillMD       = "skill-md"
	typeArchive       = "archive"
	// sourceTypeWellKnown is the lock entry's sourceType of a website.
	sourceTypeWellKnown = "well-known"
)

// wellKnownPaths are tried in this order (WELL_KNOWN_PATHS, wellknown.ts:119).
var wellKnownPaths = []string{".well-known/agent-skills", ".well-known/skills"}

var (
	digestRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	// webElemRE is the characters skillhub accepts in a file path of a
	// well-known skill: stricter than npx, so that the path is the same in
	// the URL and on disk.
	webElemRE = regexp.MustCompile(`^[A-Za-z0-9._~+=,@-]+$`)
)

// WebIndex is the index a site publishes, normalized as npx skills
// normalizes it (normalizeIndex, wellknown.ts:245-304).
type WebIndex struct {
	// URL is where it was read.
	URL string
	// Base is the URL the well-known path is below, with no trailing slash
	// (resolvedBaseUrl).
	Base string
	// WellKnownPath is .well-known/agent-skills or .well-known/skills.
	WellKnownPath string
	Entries       []WebEntry
}

// WebEntry is one skill of a WebIndex: an old entry lists Files; a v0.2.0
// one has a Type, the artifact URL resolved against the index, and its
// Digest.
type WebEntry struct {
	Name        string
	Description string
	Files       []string
	Type        string
	URL         string
	Digest      string
}

// parseSite reads https://host[/path][@skill]. The @skill suffix selects a
// skill by its index name, in any case. The URL must be an https URL with a
// host, and no credentials, query, fragment, percent sign, space, backslash
// or control character; its path is plain names. A git host npx skills does
// not read as a website, or a URL ending in .git, is KindURL.
func parseSite(s string, fail func(SourceKind) (Source, error)) (Source, error) {
	site, skill := s, ""
	if i := strings.LastIndex(s, "@"); i >= 0 {
		site, skill = s[:i], s[i+1:]
		if !validSkillName(strings.ToLower(skill)) {
			return fail(KindMalformed)
		}
	}
	if strings.ContainsAny(site, "?#%@") || strings.ContainsFunc(site, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r) || r == '\\'
	}) {
		return fail(KindMalformed)
	}
	u, err := url.Parse(site)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.Hostname() == "" || u.User != nil {
		return fail(KindMalformed)
	}
	switch strings.ToLower(u.Hostname()) {
	case "github.com", "gitlab.com", "raw.githubusercontent.com", "huggingface.co":
		return fail(KindURL)
	}
	if strings.HasSuffix(site, ".git") {
		return fail(KindURL)
	}
	if p := strings.Trim(u.EscapedPath(), "/"); p != "" && (!validPath(p) || strings.Contains(u.EscapedPath(), "//")) {
		return fail(KindMalformed)
	}
	return Source{Site: site, Skill: skill}, nil
}

// validSkillName is isValidSkillName (wellknown.ts:312-319).
func validSkillName(name string) bool {
	if name == "" || len(name) > 64 || strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") ||
		strings.Contains(name, "--") {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// siteID is the lock entry's source for a site: getSourceIdentifier
// (wellknown.ts:833-840), the host name without www.
func siteID(site string) string {
	u, err := url.Parse(site)
	if err != nil {
		return "unknown"
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// FetchIndex reads the index of site as npx skills add does
// (fetchIndexCandidates and discoverSkills, wellknown.ts:168-243, 623-656):
// .well-known/agent-skills/index.json, then .well-known/skills/index.json,
// below the site's path, which for a bare host is the root. npx also tries
// the root of a site with a path, but discoverSkills, which add uses, drops
// those candidates: a path is a scope, and its root is never used. The
// first valid index wins. A place
// that answers an HTTP error or an index npx refuses is skipped; any other
// failure, such as no network, is returned. With no index left it is
// ErrNoIndex.
func FetchIndex(ctx context.Context, web ports.HTTPSFetcher, site string) (WebIndex, error) {
	u, err := url.Parse(site)
	if err != nil {
		return WebIndex{}, fmt.Errorf("parse %s: %w", site, err)
	}
	origin := "https://" + strings.ToLower(u.Host)
	basePath := strings.TrimSuffix(u.EscapedPath(), "/")
	for _, wk := range wellKnownPaths {
		// With a path, npx also reads the root, but discoverSkills drops it
		// (a scope never widens to the whole site); it is not even asked.
		idx := WebIndex{URL: origin + basePath + "/" + wk + "/" + indexFile, Base: origin + basePath, WellKnownPath: wk}
		body, err := web.Get(ctx, idx.URL, MaxIndexBytes)
		if err != nil {
			if reason(err) == "http" {
				continue
			}
			return WebIndex{}, fetchFailed(idx.URL, err)
		}
		if entries, ok := parseIndex(body, idx.URL); ok {
			idx.Entries = entries
			return idx, nil
		}
	}
	return WebIndex{}, fmt.Errorf("%s: %w", site, ErrNoIndex)
}

// reason is the stable code of a fetcher's failure (the curl adapter's
// Reason), or "".
func reason(err error) string {
	var r interface{ CatalogReason() string }
	if errors.As(err, &r) {
		return r.CatalogReason()
	}
	return ""
}

// fetchFailed wraps a fetcher's failure, a body over its limit as
// ErrWebLimits.
func fetchFailed(u string, err error) error {
	if reason(err) == "too_large" {
		return fmt.Errorf("get %s: %w: %w", u, ErrWebLimits, err)
	}
	return fmt.Errorf("get %s: %w", u, err)
}

// parseIndex is normalizeIndex (wellknown.ts:245-304): the top level must be
// an object with a skills list. With the v0.2.0 $schema, invalid entries are
// skipped and at least one must be left; any other $schema is refused; with
// none, the old format is all or nothing.
func parseIndex(body []byte, indexURL string) ([]WebEntry, bool) {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil || top == nil {
		return nil, false
	}
	var raws []json.RawMessage
	if json.Unmarshal(top["skills"], &raws) != nil || raws == nil {
		return nil, false
	}
	var out []WebEntry
	schemaRaw, hasSchema := top["$schema"]
	if hasSchema {
		if schema, ok := jsonString(schemaRaw); !ok || schema != discoverySchemaV2 {
			return nil, false
		}
		for _, raw := range raws {
			if e, ok := entryV2(raw, indexURL); ok {
				out = append(out, e)
			}
		}
		return out, len(out) > 0
	}
	for _, raw := range raws {
		e, ok := entryV1(raw)
		if !ok {
			return nil, false
		}
		out = append(out, e)
	}
	return out, true
}

// entryV1 is isValidSkillEntryV1 (wellknown.ts:321-347).
func entryV1(raw json.RawMessage) (WebEntry, bool) {
	var f map[string]json.RawMessage
	if json.Unmarshal(raw, &f) != nil || f == nil {
		return WebEntry{}, false
	}
	var e WebEntry
	var ok bool
	if e.Name, ok = jsonString(f["name"]); !ok || !validSkillName(e.Name) {
		return WebEntry{}, false
	}
	if e.Description, ok = jsonString(f["description"]); !ok || e.Description == "" {
		return WebEntry{}, false
	}
	var files []json.RawMessage
	if json.Unmarshal(f["files"], &files) != nil || len(files) == 0 {
		return WebEntry{}, false
	}
	hasSkillMD := false
	for _, fr := range files {
		p, ok := jsonString(fr)
		if !ok || p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) ||
			strings.Contains(p, "..") || strings.Contains(p, "\x00") {
			return WebEntry{}, false
		}
		hasSkillMD = hasSkillMD || strings.ToLower(p) == "skill.md"
		e.Files = append(e.Files, p)
	}
	return e, hasSkillMD
}

// entryV2 is isValidSkillEntryV2 (wellknown.ts:349-370), with the artifact
// URL resolved against the index URL (:264).
func entryV2(raw json.RawMessage, indexURL string) (WebEntry, bool) {
	var f map[string]json.RawMessage
	if json.Unmarshal(raw, &f) != nil || f == nil {
		return WebEntry{}, false
	}
	var e WebEntry
	var ok bool
	if e.Name, ok = jsonString(f["name"]); !ok || !validSkillName(e.Name) {
		return WebEntry{}, false
	}
	// JavaScript counts the length in UTF-16 code units.
	if e.Description, ok = jsonString(f["description"]); !ok || e.Description == "" ||
		len(utf16.Encode([]rune(e.Description))) > 1024 {
		return WebEntry{}, false
	}
	if e.Type, ok = jsonString(f["type"]); !ok || (e.Type != typeSkillMD && e.Type != typeArchive) {
		return WebEntry{}, false
	}
	ref, ok := jsonString(f["url"])
	if !ok || ref == "" {
		return WebEntry{}, false
	}
	if e.Digest, ok = jsonString(f["digest"]); !ok || !digestRE.MatchString(e.Digest) {
		return WebEntry{}, false
	}
	base, err := url.Parse(indexURL)
	if err != nil {
		return WebEntry{}, false
	}
	rel, err := url.Parse(ref)
	if err != nil {
		return WebEntry{}, false
	}
	e.URL = base.ResolveReference(rel).String()
	return e, true
}

// jsonString reads a JSON string; anything else is not one.
func jsonString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// WebResolver resolves a website source to one skill, downloading its
// files into memory. It only reads; Prepare stages what it downloaded.
type WebResolver struct {
	Web ports.HTTPSFetcher
}

// Resolve reads the index of src.Site (FetchIndex), selects the skill
// src.Skill names, in any case, or the only one, and downloads its files
// within the limits. The result's Repo is the lock entry's source (the
// host), Site its sourceBaseUrl, SourceURL its sourceUrl and Digest its
// wellKnownDigest. Several skills and none named is an
// *AmbiguousSkillError; a name the index lacks, or a SKILL.md without a
// name and a description, is ErrNoSkill.
func (r WebResolver) Resolve(ctx context.Context, src Source) (Resolved, error) {
	if src.Site == "" {
		return Resolved{}, fmt.Errorf("resolve: %q is not a website source", src.Repo)
	}
	idx, err := FetchIndex(ctx, r.Web, src.Site)
	if err != nil {
		return Resolved{}, err
	}
	entry, err := pickEntry(idx, src)
	if err != nil {
		return Resolved{}, err
	}
	res := Resolved{Repo: siteID(src.Site), Site: src.Site, Name: entry.Name, Key: StoreKey(entry.Name)}
	left := MaxWebBytes
	switch entry.Type {
	case typeArchive:
		return Resolved{}, fmt.Errorf("%s of %s: %w", entry.Name, src.Site, ErrUnsupportedArtifact)
	case typeSkillMD:
		err = r.artifact(ctx, idx, entry, &res, &left)
	default:
		err = r.legacy(ctx, idx, entry, &res, &left)
	}
	if err != nil {
		return Resolved{}, err
	}
	return res, nil
}

// pickEntry selects src.Skill, compared in any case as npx compares
// --skill (add.ts:1038-1045), or the only skill of idx.
func pickEntry(idx WebIndex, src Source) (WebEntry, error) {
	if src.Skill == "" {
		if len(idx.Entries) == 1 {
			return idx.Entries[0], nil
		}
		ae := &AmbiguousSkillError{}
		for _, e := range idx.Entries {
			ae.Folders = append(ae.Folders, e.Name)
		}
		slices.Sort(ae.Folders)
		return WebEntry{}, ae
	}
	for _, e := range idx.Entries {
		if strings.EqualFold(e.Name, src.Skill) {
			return e, nil
		}
	}
	return WebEntry{}, fmt.Errorf("%s has no skill named %q: %w", src.Site, src.Skill, ErrNoSkill)
}

// legacy downloads an old entry: SKILL.md, then every other file it lists,
// from <base>/<well-known path>/<name>/ (discoverLegacySkillByEntry,
// wellknown.ts:451-505). Every path is checked before anything is
// downloaded.
func (r WebResolver) legacy(ctx context.Context, idx WebIndex, e WebEntry, res *Resolved, left *int64) error {
	skillBase := idx.Base + "/" + idx.WellKnownPath + "/" + e.Name
	var others []string
	for _, f := range e.Files {
		if strings.ToLower(f) != "skill.md" && !slices.Contains(others, f) {
			others = append(others, f)
		}
	}
	if len(others)+1 > MaxWebFiles {
		return fmt.Errorf("%s lists %d files, the limit is %d: %w", e.Name, len(others)+1, MaxWebFiles, ErrWebLimits)
	}
	if err := checkWebPaths(append([]string{skillFile}, others...)); err != nil {
		return fmt.Errorf("%s: %w", e.Name, err)
	}
	res.SourceURL = skillBase + "/" + skillFile
	md, err := r.get(ctx, idx, res.SourceURL, left)
	if err != nil {
		return err
	}
	text, err := skillText(e.Name, md)
	if err != nil {
		return err
	}
	files := map[string][]byte{skillFile: text}
	for _, f := range others {
		body, err := r.get(ctx, idx, skillBase+"/"+f, left)
		if err != nil {
			return err
		}
		files[f] = body
	}
	res.files, res.Digest = files, legacyDigest(files)
	return nil
}

// artifact downloads a v0.2.0 skill-md entry: one SKILL.md that must match
// the digest of the index (fetchArtifactSkillByEntry, wellknown.ts:507-543).
func (r WebResolver) artifact(ctx context.Context, idx WebIndex, e WebEntry, res *Resolved, left *int64) error {
	body, err := r.get(ctx, idx, e.URL, left)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != e.Digest {
		return fmt.Errorf("%s: %s, the index says %s: %w", e.URL, got, e.Digest, ErrDigestMismatch)
	}
	text, err := skillText(e.Name, body)
	if err != nil {
		return err
	}
	res.SourceURL, res.Digest = e.URL, e.Digest
	res.files = map[string][]byte{skillFile: text}
	return nil
}

// get downloads u, which must be on the index's host and below its base,
// within the per-file limit and what is left of the total.
func (r WebResolver) get(ctx context.Context, idx WebIndex, u string, left *int64) ([]byte, error) {
	if err := within(idx.Base, u); err != nil {
		return nil, err
	}
	limit := min(MaxWebFileBytes, *left)
	if limit <= 0 {
		return nil, fmt.Errorf("%s: %w", u, ErrWebLimits)
	}
	body, err := r.Web.Get(ctx, u, limit)
	if err != nil {
		return nil, fetchFailed(u, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s: %d bytes, the limit is %d: %w", u, len(body), limit, ErrWebLimits)
	}
	*left -= int64(len(body))
	return body, nil
}

// within refuses a URL that is not https, on the base's host and port, with
// no credentials, query or fragment, and a path below the base's.
func within(base, target string) error {
	b, errB := url.Parse(base)
	u, errU := url.Parse(target)
	if errB != nil || errU != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || strings.Contains(target, "#") || !strings.EqualFold(u.Host, b.Host) ||
		!strings.HasPrefix(u.EscapedPath(), b.EscapedPath()+"/") {
		return fmt.Errorf("%s is not below %s: %w", target, base, ErrUnsafeFile)
	}
	return nil
}

// checkWebPaths refuses a path that skillhub would not request and write as
// is: every element of safe characters, none ".", ".." or ".git", and no
// file that is also the folder of another.
func checkWebPaths(paths []string) error {
	for _, p := range paths {
		for elem := range strings.SplitSeq(p, "/") {
			if !webElemRE.MatchString(elem) || elem == "." || elem == ".." || strings.EqualFold(elem, ".git") {
				return fmt.Errorf("%q: %w", p, ErrUnsafeFile)
			}
		}
		for _, q := range paths {
			if strings.HasPrefix(q, p+"/") {
				return fmt.Errorf("%q is a file and a folder: %w", p, ErrUnsafeFile)
			}
		}
	}
	return nil
}

// skillText is a SKILL.md as npx reads it, with fetch's text() or a
// TextDecoder, which drop a leading byte order mark; that text is what npx
// writes and hashes. skillhub refuses one that is not UTF-8, which npx would
// alter, and one npx would skip: no name or no description.
func skillText(name string, body []byte) ([]byte, error) {
	text := bytes.TrimPrefix(body, []byte("\xEF\xBB\xBF"))
	if !utf8.Valid(text) {
		return nil, fmt.Errorf("%s: SKILL.md is not UTF-8 text: %w", name, ErrNoSkill)
	}
	if !parseSkillMD(text).ok {
		return nil, fmt.Errorf("%s: SKILL.md has no name or description: %w", name, ErrNoSkill)
	}
	return text, nil
}

// legacyDigest is computeWellKnownSkillDigest (wellknown.ts:849-861) for an
// old entry: SHA-256 over each path, in JavaScript's sort order, then a NUL,
// its content and a NUL.
func legacyDigest(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.SortFunc(paths, func(a, b string) int {
		return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
	})
	h := sha256.New()
	for _, p := range paths {
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write(files[p])
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// hashWeb gives a website skill the tree its downloaded files make, which
// no server publishes: it checks the paths and limits as staging does,
// writes the files with mode 0644, as npx writes them, to a scratch folder
// of the staging root, hashes it and removes it. It returns r with its
// FolderSHA and Nodes, and the blobs the stager writes the copy from.
func (p Preparer) hashWeb(r Resolved) (Resolved, staging.Blobs, error) {
	paths := make([]string, 0, len(r.files))
	nodes := make([]treehash.Node, 0, len(r.files))
	byBlob := memBlobs{}
	for path, data := range r.files {
		paths = append(paths, path)
		nodes = append(nodes, treehash.Node{Path: path, Mode: treehash.ModeFile, Size: int64(len(data))})
		byBlob[treehash.BlobSHA(data)] = data
	}
	if len(paths) == 0 {
		return r, nil, fmt.Errorf("%s: no files were downloaded: %w", r.Key, ErrNoSkill)
	}
	if err := staging.CheckNodes(nodes); err != nil {
		return r, nil, err
	}
	slices.Sort(paths)
	scratch := filepath.Join(StagingRoot(p.Paths), r.Key+"-download")
	defer func() { _ = p.Files.RemoveTree(scratch) }() //nolint:errcheck // staging is a disposable cache.
	if err := p.Files.RemoveTree(scratch); err != nil {
		return r, nil, fmt.Errorf("clear %s: %w", scratch, err)
	}
	if err := p.Files.MkdirAll(scratch, 0o755); err != nil {
		return r, nil, fmt.Errorf("create %s: %w", scratch, err)
	}
	for _, path := range paths {
		if err := p.Files.WriteFileAtomic(filepath.Join(scratch, filepath.FromSlash(path)), r.files[path], 0o644); err != nil {
			return r, nil, fmt.Errorf("write %s: %w", path, err)
		}
	}
	hashDir := p.HashDir
	if hashDir == nil {
		hashDir = treehash.Dir
	}
	tree, err := hashDir(scratch)
	if err != nil {
		return r, nil, fmt.Errorf("hash %s: %w", scratch, err)
	}
	r.FolderSHA, r.Nodes = tree.SHA, tree.Nodes
	return r, byBlob, nil
}

// memBlobs serves the downloaded files of a website skill by blob SHA.
type memBlobs map[string][]byte

func (m memBlobs) Blob(_ context.Context, _, sha string) ([]byte, error) {
	data, ok := m[sha]
	if !ok {
		return nil, fmt.Errorf("blob %s was not downloaded", sha)
	}
	return slices.Clone(data), nil
}
