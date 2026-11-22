package data

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

// Keys of agents.local.toml. The file holds one array of tables, [[agent]],
// whose rows correct an embedded agent with the same id or add a new one.
const (
	keyAgent          = "agent"
	keyID             = "id"
	keyGlobalDir      = "global_dir"
	keyReadsUniversal = "reads_universal"
	// Version 2 keys (see Harness). They are optional, so a version 1 file
	// stays valid.
	keyReadsStore   = "reads_store"
	keyLinkDir      = "link_dir"
	keyFollowsLinks = "follows_links"
	keyReadRoots    = "read_roots"
	keyRootDir      = "dir"
	keyRootDepth    = "depth"
)

var rowKeys = []string{keyID, keyGlobalDir, keyReadsUniversal, keyReadsStore, keyLinkDir, keyFollowsLinks, keyReadRoots}

// AgentsLocalError reports an agents.local.toml that cannot be used. Key is
// the dotted key at fault, such as agent.global_dir; it is empty when the
// file is not valid TOML.
type AgentsLocalError struct {
	Path   string
	Key    string
	Reason string
	// Err is the underlying error, if any.
	Err error
}

func (e *AgentsLocalError) Error() string {
	if e.Key == "" {
		return fmt.Sprintf("invalid %s: %s", e.Path, e.Reason)
	}
	return fmt.Sprintf("invalid %s: key %q: %s", e.Path, e.Key, e.Reason)
}

func (e *AgentsLocalError) Unwrap() error { return e.Err }

// localAgent is one [[agent]] row. A pointer, slice or Tri field is nil or
// empty when the row leaves its key out.
type localAgent struct {
	ID             string
	GlobalDir      string
	ReadsUniversal *bool
	ReadsStore     Tri
	LinkDir        *string
	FollowsLinks   Tri
	ReadRoots      []ReadRoot
}

// LoadAgents returns the embedded agents table corrected and extended by the
// user's agents.local.toml (p.AgentsLocalFile()). A row whose id is in the
// embedded table replaces that agent's global_dir, and its reads_universal
// when the row sets it, keeping the agent's place; a row with a new id is
// added after the embedded table, in file order. A missing file leaves the
// embedded table as it is. An invalid file, or an unknown key or type, is an
// *AgentsLocalError naming the key, and no agents are returned. The version 2
// keys are checked as LoadHarnesses checks them and otherwise ignored here.
func LoadAgents(p paths.Paths, files ports.FileOps) ([]domain.AgentProfile, error) {
	agents, err := Agents()
	if err != nil {
		return nil, err
	}
	file := p.AgentsLocalFile()
	rows, err := readLocal(file, files)
	if err != nil {
		return nil, err
	}
	if _, err := mergeLocal(file, rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		i := slices.IndexFunc(agents, func(a domain.AgentProfile) bool { return a.ID == row.ID })
		if i < 0 {
			agents = append(agents, domain.AgentProfile{ID: row.ID, GlobalDir: row.GlobalDir})
			i = len(agents) - 1
		}
		agents[i].GlobalDir = row.GlobalDir
		if row.ReadsUniversal != nil {
			agents[i].ReadsUniversal = *row.ReadsUniversal
		}
	}
	return agents, nil
}

// parseLocal decodes agents.local.toml into rows. It decodes into generic
// values and checks every key and type itself, so that each error names its
// key exactly.
func parseLocal(file string, content []byte) ([]localAgent, error) {
	fail := func(key, reason string, err error) error {
		return &AgentsLocalError{Path: file, Key: key, Reason: reason, Err: err}
	}
	var raw map[string]any
	if _, err := toml.Decode(string(content), &raw); err != nil {
		return nil, fail("", err.Error(), err)
	}
	for _, k := range sortedKeys(raw) {
		if k != keyAgent {
			return nil, fail(k, "unknown key; the file only holds [[agent]] tables", nil)
		}
	}
	value, ok := raw[keyAgent]
	if !ok {
		return nil, nil
	}
	tables, ok := value.([]map[string]any)
	if !ok {
		return nil, fail(keyAgent, "must be an array of tables, written [[agent]]", nil)
	}
	rows := make([]localAgent, 0, len(tables))
	seen := map[string]bool{}
	for n, table := range tables {
		row, err := parseRow(table, n+1, fail)
		if err != nil {
			return nil, err
		}
		if seen[row.ID] {
			return nil, fail(keyAgent+"."+keyID, fmt.Sprintf("agent %d repeats id %q", n+1, row.ID), nil)
		}
		seen[row.ID] = true
		rows = append(rows, row)
	}
	return rows, nil
}

func parseRow(table map[string]any, n int, fail func(key, reason string, err error) error) (localAgent, error) {
	key := func(k string) string { return keyAgent + "." + k }
	for _, k := range sortedKeys(table) {
		if !slices.Contains(rowKeys, k) {
			return localAgent{}, fail(key(k), fmt.Sprintf("unknown key in agent %d", n), nil)
		}
	}
	var row localAgent
	id, err := requiredString(table, keyID)
	if err != nil {
		return localAgent{}, fail(key(keyID), fmt.Sprintf("agent %d: %v", n, err), nil)
	}
	row.ID = id
	dir, err := requiredString(table, keyGlobalDir)
	if err != nil {
		return localAgent{}, fail(key(keyGlobalDir), fmt.Sprintf("agent %d: %v", n, err), nil)
	}
	if row.GlobalDir, err = homeRelative(dir); err != nil {
		return localAgent{}, fail(key(keyGlobalDir), fmt.Sprintf("agent %d: %v", n, err), nil)
	}
	if v, ok := table[keyReadsUniversal]; ok {
		b, ok := v.(bool)
		if !ok {
			return localAgent{}, fail(key(keyReadsUniversal), fmt.Sprintf("agent %d: must be true or false, not %T", n, v), nil)
		}
		row.ReadsUniversal = &b
	}
	if err := parseV2Keys(table, n, &row, key, fail); err != nil {
		return localAgent{}, err
	}
	return row, nil
}

// parseV2Keys reads the optional version 2 keys of an [[agent]] row.
func parseV2Keys(table map[string]any, n int, row *localAgent, key func(string) string, fail func(key, reason string, err error) error) error {
	for _, k := range []string{keyReadsStore, keyFollowsLinks} {
		v, ok := table[k]
		if !ok {
			continue
		}
		t, err := parseTri(v)
		if err != nil {
			return fail(key(k), fmt.Sprintf("agent %d: %v", n, err), nil)
		}
		if k == keyReadsStore {
			row.ReadsStore = t
		} else {
			row.FollowsLinks = t
		}
	}
	if _, ok := table[keyLinkDir]; ok {
		dir, err := requiredString(table, keyLinkDir)
		if err == nil {
			dir, err = homeRelative(dir)
		}
		if err != nil {
			return fail(key(keyLinkDir), fmt.Sprintf("agent %d: %v", n, err), nil)
		}
		row.LinkDir = &dir
	}
	v, ok := table[keyReadRoots]
	if !ok {
		return nil
	}
	tables, ok := v.([]map[string]any)
	if !ok {
		return fail(key(keyReadRoots), fmt.Sprintf("agent %d: must be an array of tables, written [[agent.read_roots]]", n), nil)
	}
	rootKey := func(k string) string { return key(keyReadRoots) + "." + k }
	row.ReadRoots = make([]ReadRoot, 0, len(tables))
	for _, t := range tables {
		for _, k := range sortedKeys(t) {
			if k != keyRootDir && k != keyRootDepth {
				return fail(rootKey(k), fmt.Sprintf("unknown key in a read root of agent %d", n), nil)
			}
		}
		dir, err := requiredString(t, keyRootDir)
		if err == nil {
			dir, err = homeRelative(dir)
		}
		if err != nil {
			return fail(rootKey(keyRootDir), fmt.Sprintf("agent %d: %v", n, err), nil)
		}
		raw, ok := t[keyRootDepth]
		if !ok {
			return fail(rootKey(keyRootDepth), fmt.Sprintf("agent %d: missing", n), nil)
		}
		depth, err := parseDepth(raw)
		if err != nil {
			return fail(rootKey(keyRootDepth), fmt.Sprintf("agent %d: %v", n, err), nil)
		}
		row.ReadRoots = append(row.ReadRoots, ReadRoot{Dir: dir, Depth: depth})
	}
	return nil
}

func requiredString(table map[string]any, k string) (string, error) {
	v, ok := table[k]
	if !ok {
		return "", errors.New("missing")
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("must be a string, not %T", v)
	}
	if s == "" {
		return "", errors.New("must not be empty")
	}
	return s, nil
}

// homeRelative turns a global_dir written as ~/dir or dir into the
// slash-separated path relative to the home that the embedded table uses.
// It refuses an absolute path, the home itself and a path that climbs out
// of the home.
func homeRelative(dir string) (string, error) {
	rel := dir
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		rel = strings.TrimPrefix(strings.TrimPrefix(dir, "~"), "/")
	} else if strings.HasPrefix(dir, "~") {
		return "", fmt.Errorf("%q: only ~/ is understood, as the home", dir)
	}
	if path.IsAbs(rel) {
		return "", fmt.Errorf("%q: write it relative to the home, as ~/dir", dir)
	}
	rel = path.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%q: must be a directory inside the home", dir)
	}
	return rel, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
