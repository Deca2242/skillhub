package data

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"

	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

// Version 2 columns of the agents table (docs/design-v2.md sections 3.2 and
// 11, item 3): how each harness discovers skills, whether it reads the store,
// where npx skills links for it and which harnesses share a directory. Only
// the facts of docs/research/harnesses-v2.md are recorded; the rest is
// Unknown or DepthUnknown, never a guessed value.

// storeDir is the universal store, relative to the home.
const storeDir = ".agents/skills"

// Tri is a fact that is verified true, verified false or not verified.
type Tri string

const (
	Yes     Tri = "yes"
	No      Tri = "no"
	Unknown Tri = "unknown"
)

func parseTri(v any) (Tri, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("must be \"yes\", \"no\" or \"unknown\", not %T", v)
	}
	switch t := Tri(s); t {
	case Yes, No, Unknown:
		return t, nil
	}
	return "", fmt.Errorf("%q: must be \"yes\", \"no\" or \"unknown\"", s)
}

// Depth is how many directory levels below a read root a harness looks for
// SKILL.md: 1 is only <root>/<skill>/SKILL.md. DepthUnlimited has no bound
// and DepthUnknown means that whether and how deep the root is read is not
// verified.
type Depth int

const (
	DepthUnknown   Depth = -1
	DepthUnlimited Depth = 0
)

const (
	depthUnlimitedWord = "unlimited"
	depthUnknownWord   = "unknown"
)

func (d Depth) String() string {
	switch d {
	case DepthUnknown:
		return depthUnknownWord
	case DepthUnlimited:
		return depthUnlimitedWord
	}
	return strconv.Itoa(int(d))
}

// parseDepth reads a depth as a positive whole number (a float64 from JSON,
// an int64 from TOML) or the words "unlimited" and "unknown".
func parseDepth(v any) (Depth, error) {
	const want = `must be a whole number of levels from 1, "unlimited" or "unknown"`
	switch x := v.(type) {
	case string:
		switch x {
		case depthUnlimitedWord:
			return DepthUnlimited, nil
		case depthUnknownWord:
			return DepthUnknown, nil
		}
		return 0, fmt.Errorf("%q: %s", x, want)
	case int64:
		if x >= 1 && x <= 1<<16 {
			return Depth(x), nil
		}
	case float64:
		if x >= 1 && x <= 1<<16 && x == float64(int(x)) {
			return Depth(int(x)), nil
		}
	}
	return 0, fmt.Errorf("%v: %s", v, want)
}

// ReadRoot is a directory a harness reads skills from.
type ReadRoot struct {
	// Dir is relative to the home.
	Dir   string
	Depth Depth
}

// Harness is a row of the agents table with its version 2 columns.
type Harness struct {
	ID string
	// GlobalDir is the version 1 column, relative to the home.
	GlobalDir string
	// ReadsStore tells whether the harness reads ~/.agents/skills directly,
	// so that npx skills does not link for it (a "universal" agent).
	ReadsStore Tri
	// LinkDir is where npx skills links a skill for this harness, relative to
	// the home. It is set exactly when ReadsStore is No.
	LinkDir string
	// ReadRoots are the directories the harness reads, in its order.
	ReadRoots []ReadRoot
	// FollowsLinks tells whether the harness loads a skill through a symlink.
	FollowsLinks Tri
	// SharesDirWith lists, sorted, the other harnesses that read or link in
	// one of this harness's directories, the store left out: a change there
	// reaches them too.
	SharesDirWith []string
}

// DiagnosticProfile is another configuration of a harness that skillhub only
// reads (S7, decision 6): its skills are reported, never managed.
type DiagnosticProfile struct {
	ID string
	// Harness is the id of the harness the profile configures.
	Harness   string
	ReadRoots []ReadRoot
}

// harnessSpec is a harness before its derived columns are filled. A zero
// field was not given.
type harnessSpec struct {
	id           string
	globalDir    string
	readsStore   Tri
	linkDir      *string
	readRoots    []ReadRoot
	followsLinks Tri
}

func rootsFromRecords(records []rootRecord) ([]ReadRoot, error) {
	if records == nil {
		return nil, nil
	}
	out := make([]ReadRoot, 0, len(records))
	for _, r := range records {
		d, err := parseDepth(r.Depth)
		if err != nil {
			return nil, fmt.Errorf("read root %q: %w", r.Dir, err)
		}
		out = append(out, ReadRoot{Dir: r.Dir, Depth: d})
	}
	return out, nil
}

func specFromRecord(r agentRecord) (harnessSpec, error) {
	s := harnessSpec{id: r.ID, globalDir: r.GlobalDir, linkDir: r.LinkDir}
	var err error
	if r.ReadsStore != nil {
		if s.readsStore, err = parseTri(*r.ReadsStore); err != nil {
			return harnessSpec{}, fmt.Errorf("agent %s reads_store: %w", r.ID, err)
		}
	}
	if r.FollowsLinks != nil {
		if s.followsLinks, err = parseTri(*r.FollowsLinks); err != nil {
			return harnessSpec{}, fmt.Errorf("agent %s follows_links: %w", r.ID, err)
		}
	}
	if s.readRoots, err = rootsFromRecords(r.ReadRoots); err != nil {
		return harnessSpec{}, fmt.Errorf("agent %s: %w", r.ID, err)
	}
	return s, nil
}

// errLinkDirWithoutLinks is a link_dir on a harness npx skills does not link
// for.
var errLinkDirWithoutLinks = errors.New(`only a harness with reads_store = "no" has a link_dir`)

// derive fills the columns a spec leaves out: ReadsStore is Yes for a
// harness whose global directory is the store and Unknown otherwise;
// LinkDir is the global directory of a harness that does not read the
// store; the read roots are the global directory, plus the store for a
// harness that reads it, both of unknown depth; FollowsLinks is Unknown.
func derive(s harnessSpec) (Harness, error) {
	h := Harness{ID: s.id, GlobalDir: s.globalDir, ReadsStore: s.readsStore, FollowsLinks: s.followsLinks}
	if h.ReadsStore == "" {
		h.ReadsStore = Unknown
		if h.GlobalDir == storeDir {
			h.ReadsStore = Yes
		}
	}
	switch {
	case s.linkDir != nil && h.ReadsStore != No:
		return Harness{}, errLinkDirWithoutLinks
	case s.linkDir != nil:
		h.LinkDir = *s.linkDir
	case h.ReadsStore == No:
		h.LinkDir = h.GlobalDir
	}
	h.ReadRoots = slices.Clone(s.readRoots)
	if h.ReadRoots == nil {
		h.ReadRoots = []ReadRoot{{Dir: h.GlobalDir, Depth: DepthUnknown}}
		if h.ReadsStore == Yes && h.GlobalDir != storeDir {
			h.ReadRoots = append(h.ReadRoots, ReadRoot{Dir: storeDir, Depth: DepthUnknown})
		}
	}
	if h.FollowsLinks == "" {
		h.FollowsLinks = Unknown
	}
	return h, nil
}

// fillShared sets SharesDirWith on every harness.
func fillShared(hs []Harness) {
	dirs := make([]map[string]bool, len(hs))
	for i, h := range hs {
		dirs[i] = map[string]bool{}
		if h.LinkDir != "" {
			dirs[i][h.LinkDir] = true
		}
		for _, r := range h.ReadRoots {
			dirs[i][r.Dir] = true
		}
		delete(dirs[i], storeDir)
	}
	for i := range hs {
		hs[i].SharesDirWith = nil
		for j := range hs {
			if i == j || hs[j].ID == hs[i].ID {
				continue
			}
			for d := range dirs[i] {
				if dirs[j][d] {
					hs[i].SharesDirWith = append(hs[i].SharesDirWith, hs[j].ID)
					break
				}
			}
		}
		slices.Sort(hs[i].SharesDirWith)
	}
}

func embeddedSpecs() ([]harnessSpec, error) {
	f, err := parse()
	if err != nil {
		return nil, err
	}
	specs := make([]harnessSpec, 0, len(f.Agents))
	for _, r := range f.Agents {
		s, err := specFromRecord(r)
		if err != nil {
			return nil, fmt.Errorf("embedded agents.json: %w", err)
		}
		specs = append(specs, s)
	}
	return specs, nil
}

func build(specs []harnessSpec) ([]Harness, error) {
	out := make([]Harness, 0, len(specs))
	for _, s := range specs {
		h, err := derive(s)
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", s.id, err)
		}
		out = append(out, h)
	}
	fillShared(out)
	return out, nil
}

// Harnesses returns the embedded agents table with its version 2 columns, in
// the order of Agents. Each call returns fresh slices.
func Harnesses() ([]Harness, error) {
	specs, err := embeddedSpecs()
	if err != nil {
		return nil, err
	}
	return build(specs)
}

// DiagnosticProfiles returns the read-only profiles of agents.json. Each call
// returns fresh slices.
func DiagnosticProfiles() ([]DiagnosticProfile, error) {
	f, err := parse()
	if err != nil {
		return nil, err
	}
	out := make([]DiagnosticProfile, 0, len(f.DiagnosticProfiles))
	for _, r := range f.DiagnosticProfiles {
		roots, err := rootsFromRecords(r.ReadRoots)
		if err != nil {
			return nil, fmt.Errorf("embedded agents.json: profile %s: %w", r.ID, err)
		}
		out = append(out, DiagnosticProfile{ID: r.ID, Harness: r.Harness, ReadRoots: roots})
	}
	return out, nil
}

// LoadHarnesses returns the agents table with its version 2 columns,
// corrected and extended by agents.local.toml exactly as LoadAgents does. A
// row replaces each version 2 key it sets; the columns it leaves out keep the
// embedded value or are derived again from the new global_dir. A missing
// file leaves the embedded table; an invalid one is an *AgentsLocalError and
// no harnesses are returned.
func LoadHarnesses(p paths.Paths, files ports.FileOps) ([]Harness, error) {
	file := p.AgentsLocalFile()
	rows, err := readLocal(file, files)
	if err != nil {
		return nil, err
	}
	return mergeLocal(file, rows)
}

// readLocal reads and parses agents.local.toml; a missing file has no rows.
func readLocal(file string, files ports.FileOps) ([]localAgent, error) {
	content, err := files.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	return parseLocal(file, content)
}

// mergeLocal applies the overlay rows to the embedded specs and derives the
// table.
func mergeLocal(file string, rows []localAgent) ([]Harness, error) {
	specs, err := embeddedSpecs()
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		i := slices.IndexFunc(specs, func(s harnessSpec) bool { return s.id == row.ID })
		if i < 0 {
			specs = append(specs, harnessSpec{id: row.ID})
			i = len(specs) - 1
		}
		s := &specs[i]
		s.globalDir = row.GlobalDir
		if row.ReadsStore != "" {
			s.readsStore = row.ReadsStore
		}
		if row.LinkDir != nil {
			s.linkDir = row.LinkDir
		}
		if row.ReadRoots != nil {
			s.readRoots = row.ReadRoots
		}
		if row.FollowsLinks != "" {
			s.followsLinks = row.FollowsLinks
		}
		if _, err := derive(*s); err != nil {
			return nil, &AgentsLocalError{Path: file, Key: keyAgent + "." + keyLinkDir, Reason: fmt.Sprintf("agent %q: %v", row.ID, err), Err: err}
		}
	}
	return build(specs)
}
