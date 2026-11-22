// Package settings implements ports.SettingsStore using settings.toml.
package settings

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/BurntSushi/toml"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

var _ ports.SettingsStore = (*Store)(nil)

type tomlDuration time.Duration

func (d tomlDuration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

func (d *tomlDuration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = tomlDuration(parsed)
	return nil
}

type tomlSettings struct {
	CheckInterval tomlDuration `toml:"check_interval"`
	Notifications bool         `toml:"notifications"`
	Ignore        []string     `toml:"ignore"`
}

// Store loads and saves settings.toml under paths.Paths.
type Store struct {
	paths paths.Paths
}

// New builds a Store using locations from p.
func New(p paths.Paths) *Store {
	return &Store{paths: p}
}

// Load reads settings.toml, returning domain.DefaultSettings when the file is absent.
func (s *Store) Load() (domain.Settings, error) {
	path := s.paths.SettingsFile()
	if err := s.paths.Check(path); err != nil {
		return domain.Settings{}, fmt.Errorf("load settings: %w", err)
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return domain.DefaultSettings(), nil
	}
	if err != nil {
		return domain.Settings{}, fmt.Errorf("read settings file: %w", err)
	}

	return decodeSettings(data)
}

func decodeSettings(data []byte) (domain.Settings, error) {
	defaults := domain.DefaultSettings()
	raw := tomlSettings{
		CheckInterval: tomlDuration(defaults.CheckInterval),
		Notifications: defaults.Notifications,
		Ignore:        slices.Clone(defaults.Ignore),
	}
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return domain.Settings{}, fmt.Errorf("decode settings: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return domain.Settings{}, fmt.Errorf("unknown settings key %q", undecoded[0].String())
	}
	if raw.Ignore == nil {
		raw.Ignore = []string{}
	}

	out := domain.Settings{
		CheckInterval: time.Duration(raw.CheckInterval),
		Notifications: raw.Notifications,
		Ignore:        raw.Ignore,
	}
	if err := out.Validate(); err != nil {
		return domain.Settings{}, fmt.Errorf("check_interval: %w", err)
	}
	return out, nil
}

// Save validates and atomically writes settings.toml.
func (s *Store) Save(settings domain.Settings) error {
	path := s.paths.SettingsFile()
	if err := s.paths.Check(path); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("check_interval: %w", err)
	}

	ignore := slices.Clone(settings.Ignore)
	if ignore == nil {
		ignore = []string{}
	}
	raw := tomlSettings{
		CheckInterval: tomlDuration(settings.CheckInterval),
		Notifications: settings.Notifications,
		Ignore:        ignore,
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(raw); err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if _, err := decodeSettings(buf.Bytes()); err != nil {
		return fmt.Errorf("verify encoded settings: %w", err)
	}
	return writeFileAtomic(path, buf.Bytes(), 0o644)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	cleanup = false
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync parent dir: %w", err)
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	return errors.Join(syncErr, closeErr)
}
