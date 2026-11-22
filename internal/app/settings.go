package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

// Setting keys, as settings.toml and `settings set` name them.
const (
	KeyCheckInterval = "check_interval"
	KeyNotifications = "notifications"
	KeyIgnore        = "ignore"
)

var (
	// ErrUnknownSetting reports a key that is not a setting.
	ErrUnknownSetting = errors.New("unknown setting")
	// ErrInvalidSettingValue reports a value the setting cannot take.
	ErrInvalidSettingValue = errors.New("invalid setting value")
)

// Settings returns the user's settings.
func (a *App) Settings() (domain.Settings, error) {
	s, err := a.d.Settings.Load()
	if err != nil {
		return domain.Settings{}, fmt.Errorf("load settings: %w", err)
	}
	return s, nil
}

// SetSetting sets key to value and saves the settings, unless dryRun; it
// returns the settings as they are, or would be, after the change.
func (a *App) SetSetting(key, value string, dryRun bool) (domain.Settings, error) {
	s, err := a.Settings()
	if err != nil {
		return domain.Settings{}, err
	}
	switch key {
	case KeyCheckInterval:
		d, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return domain.Settings{}, fmt.Errorf("%s %q: %w", key, value, ErrInvalidSettingValue)
		}
		s.CheckInterval = d
	case KeyNotifications:
		b, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return domain.Settings{}, fmt.Errorf("%s %q: %w", key, value, ErrInvalidSettingValue)
		}
		s.Notifications = b
	case KeyIgnore:
		s.Ignore = []string{}
		for _, item := range strings.Split(value, ",") {
			if item = strings.TrimSpace(item); item != "" && !slices.Contains(s.Ignore, item) {
				s.Ignore = append(s.Ignore, item)
			}
		}
	default:
		return domain.Settings{}, fmt.Errorf("%q: %w", key, ErrUnknownSetting)
	}
	if err := s.Validate(); err != nil {
		return domain.Settings{}, err
	}
	if dryRun {
		return s, nil
	}
	if err := a.d.Settings.Save(s); err != nil {
		return domain.Settings{}, fmt.Errorf("save settings: %w", err)
	}
	return s, nil
}

// BackgroundStatus reports the state of the background check's timer.
func (a *App) BackgroundStatus(ctx context.Context) (ports.BackgroundStatus, error) {
	st, err := a.d.Background.Status(ctx)
	if err != nil {
		return ports.BackgroundStatus{}, fmt.Errorf("background status: %w", err)
	}
	return st, nil
}

// EnableBackground installs and enables the background check's timer,
// pointing at binary. Only the user's own command calls it.
func (a *App) EnableBackground(ctx context.Context, binary string) error {
	if err := a.d.Background.Enable(ctx, binary); err != nil {
		return fmt.Errorf("enable the background check: %w", err)
	}
	return nil
}

// DisableBackground disables the timer and removes its units.
func (a *App) DisableBackground(ctx context.Context) error {
	if err := a.d.Background.Disable(ctx); err != nil {
		return fmt.Errorf("disable the background check: %w", err)
	}
	return nil
}
