package statefile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"skillhub/internal/packs"
)

const (
	packsFileName = "packs.json"
	packsVersion  = 1
)

// ErrUnsupportedPacksVersion reports a packs.json whose version is not 1.
var ErrUnsupportedPacksVersion = errors.New("unsupported packs version")

var _ packs.Store = (*Store)(nil)

type packsFile struct {
	Version int          `json:"version"`
	Packs   []packs.Pack `json:"packs"`
}

func (s *Store) packsFilePath() string {
	return filepath.Join(s.paths.StateDir(), packsFileName)
}

// LoadPacks reads all draft packs from Skillhub's versioned packs.json sidecar.
func (s *Store) LoadPacks() ([]packs.Pack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pf, _, err := s.readPacksFile()
	if err != nil {
		return nil, err
	}
	if pf.Packs == nil {
		return []packs.Pack{}, nil
	}
	return pf.Packs, nil
}

// SavePacks atomically writes all draft packs to Skillhub's versioned packs.json sidecar.
func (s *Store) SavePacks(in []packs.Pack) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.packsFilePath()
	if err := s.paths.Check(path); err != nil {
		return fmt.Errorf("write packs: %w", err)
	}
	if err := validatePacks(in); err != nil {
		return err
	}
	if _, _, err := s.readPacksFile(); err != nil {
		return err
	}

	normalized := make([]packs.Pack, len(in))
	for i, p := range in {
		if p.Members == nil {
			p.Members = []string{}
		}
		normalized[i] = p
	}
	pf := packsFile{
		Version: packsVersion,
		Packs:   normalized,
	}
	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return fmt.Errorf("encode packs file: %w", err)
	}
	data = append(data, '\n')
	if _, err := decodePacksFile(data); err != nil {
		return fmt.Errorf("verify encoded packs: %w", err)
	}
	return writeFileAtomic(path, data, 0o644)
}

func (s *Store) readPacksFile() (packsFile, bool, error) {
	path := s.packsFilePath()
	if err := s.paths.Check(path); err != nil {
		return packsFile{}, false, fmt.Errorf("read packs: %w", err)
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return packsFile{Version: packsVersion, Packs: []packs.Pack{}}, false, nil
	}
	if err != nil {
		return packsFile{}, false, fmt.Errorf("read packs: %w", err)
	}
	pf, err := decodePacksFile(raw)
	if err != nil {
		return packsFile{}, false, err
	}
	return pf, true, nil
}

func decodePacksFile(raw []byte) (packsFile, error) {
	var pf packsFile
	if err := json.Unmarshal(raw, &pf); err != nil {
		return packsFile{}, fmt.Errorf("%w: %w", packs.ErrCorruptSidecar, err)
	}
	if pf.Version != packsVersion {
		return packsFile{}, fmt.Errorf("%w: %w: %d", ErrUnsupportedPacksVersion, packs.ErrUnsupportedSidecarVersion, pf.Version)
	}
	for i := range pf.Packs {
		if pf.Packs[i].Members == nil {
			pf.Packs[i].Members = []string{}
		}
	}
	if err := validatePacks(pf.Packs); err != nil {
		return packsFile{}, err
	}
	return pf, nil
}

func validatePacks(in []packs.Pack) error {
	seenPacks := make(map[string]struct{}, len(in))
	for _, p := range in {
		if p.Name == "" {
			return fmt.Errorf("%w: empty pack name", packs.ErrCorruptSidecar)
		}
		if _, dup := seenPacks[p.Name]; dup {
			return fmt.Errorf("%w: duplicate pack name %q", packs.ErrCorruptSidecar, p.Name)
		}
		seenPacks[p.Name] = struct{}{}

		seenMembers := make(map[string]struct{}, len(p.Members))
		for _, m := range p.Members {
			if m == "" {
				return fmt.Errorf("%w: empty member identity in pack %q", packs.ErrCorruptSidecar, p.Name)
			}
			if _, dup := seenMembers[m]; dup {
				return fmt.Errorf("%w: duplicate member %q in pack %q", packs.ErrCorruptSidecar, m, p.Name)
			}
			seenMembers[m] = struct{}{}
		}
	}
	return nil
}
