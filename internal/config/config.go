package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type Config struct {
	Token               string   `json:"token,omitempty"`
	XSRFToken           string   `json:"xsrf_token,omitempty"`
	KickSession         string   `json:"kick_session,omitempty"`
	KickSessionID       string   `json:"kick_session_id,omitempty"`
	ViewerMode          bool     `json:"viewer_mode,omitempty"`
	PriorityChannels    []string `json:"priority_channels,omitempty"`
	WatchMode           string   `json:"watch_mode,omitempty"`
	Category            string   `json:"category,omitempty"`
	DisableDailyRewards bool     `json:"disable_daily_rewards,omitempty"`
	// Channels is read only to migrate settings from the first CLI version.
	Channels []string `json:"channels,omitempty"`
}

func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("could not locate the user config directory: %w", err)
	}
	return filepath.Join(dir, "kick-level-farm", "config.json"), nil
}

func Load(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("could not read settings: %w", err)
	}
	if json.Unmarshal(data, &cfg) != nil {
		return Config{}, fmt.Errorf("config file contains invalid JSON: %s; fix it or back it up and remove it", path)
	}
	if cfg.Token != "" {
		cfg.Token, err = NormalizeToken(cfg.Token)
		if err != nil {
			return Config{}, err
		}
	}
	if err := cfg.ValidateSession(); err != nil {
		return Config{}, err
	}
	if err := cfg.ValidateWatching(); err != nil {
		return Config{}, err
	}
	if len(cfg.PriorityChannels) == 0 && len(cfg.Channels) > 0 {
		cfg.PriorityChannels = cfg.Channels
	}
	cfg.Channels = nil
	if len(cfg.PriorityChannels) > 0 {
		cfg.PriorityChannels, err = ParseChannels(strings.Join(cfg.PriorityChannels, ","))
	}
	return cfg, err
}

func Save(path string, cfg Config) error {
	if err := cfg.ValidateSession(); err != nil {
		return err
	}
	if err := cfg.ValidateWatching(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("could not create the config directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("could not create the config file: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("could not save settings: %w", err)
	}
	return nil
}

// ValidateSession rejects values that could alter HTTP headers or cookie boundaries.
func (cfg Config) ValidateSession() error {
	for _, value := range []string{cfg.XSRFToken, cfg.KickSession, cfg.KickSessionID} {
		for _, r := range value {
			if r < 0x21 || r > 0x7e || strings.ContainsRune("\";,\\", r) {
				return errors.New("session cookies contain invalid characters; paste only the cookie value")
			}
		}
	}
	decoded, err := url.PathUnescape(cfg.XSRFToken)
	if err != nil || strings.ContainsFunc(decoded, unicode.IsControl) {
		return errors.New("XSRF token contains invalid URL encoding or control characters")
	}
	return nil
}

func NormalizeToken(raw string) (string, error) {
	token := strings.TrimSpace(raw)
	if len(token) >= 7 && strings.EqualFold(token[:7], "bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	if token == "" || strings.EqualFold(token, "bearer") || strings.ContainsFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", errors.New("Bearer token must not be empty or contain whitespace or control characters")
	}
	return token, nil
}

func ParseChannels(raw string) ([]string, error) {
	var channels []string
	seen := make(map[string]bool)
	for _, slug := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		slug = strings.ToLower(strings.TrimSpace(slug))
		if slug == "" {
			continue
		}
		for _, r := range slug {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				return nil, errors.New("channel names may contain only letters, numbers, underscores, and hyphens; enter a channel name, not a URL")
			}
		}
		if !seen[slug] {
			seen[slug] = true
			channels = append(channels, slug)
		}
	}
	if len(channels) == 0 {
		return nil, errors.New("at least one channel name is required")
	}
	return channels, nil
}
