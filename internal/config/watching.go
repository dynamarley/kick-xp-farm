package config

import (
	"errors"
	"strings"
	"unicode"
)

const (
	FollowingFirst = "following"
	CategoryFirst  = "category"
	PriorityFirst  = "priority"
)

func (cfg Config) EffectiveWatchMode() string {
	if cfg.WatchMode == "" {
		return FollowingFirst
	}
	return cfg.WatchMode
}

func (cfg Config) WatchModeLabel() string {
	switch cfg.EffectiveWatchMode() {
	case CategoryFirst:
		return "Category first"
	case PriorityFirst:
		return "Priority channels first"
	default:
		return "Following first"
	}
}

func (cfg Config) ValidateWatching() error {
	switch cfg.EffectiveWatchMode() {
	case FollowingFirst, CategoryFirst, PriorityFirst:
	default:
		return errors.New("watch mode must be following, category, or priority")
	}
	if len(cfg.Category) > 120 || strings.ContainsFunc(cfg.Category, unicode.IsControl) {
		return errors.New("category must be at most 120 characters and contain no control characters")
	}
	if cfg.Category != "" && !strings.ContainsFunc(cfg.Category, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return errors.New("category must include letters or numbers; use the slug from Kick's category URL")
	}
	if cfg.EffectiveWatchMode() == CategoryFirst && strings.TrimSpace(cfg.Category) == "" {
		return errors.New("choose a category before enabling category mode")
	}
	return nil
}
