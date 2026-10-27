package kick

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"kick-bot/internal/config"
)

func (c *Client) WithWatching(cfg config.Config) *Client {
	c.watchConfig = cfg
	return c
}

func categoryKey(value string) string {
	var b strings.Builder
	separator := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if separator && b.Len() > 0 {
				b.WriteByte('-')
			}
			separator = false
			b.WriteRune(r)
		} else {
			separator = true
		}
	}
	return b.String()
}

func categoryMatches(wanted, name, slug string) bool {
	key := categoryKey(wanted)
	return key != "" && (key == categoryKey(name) || key == categoryKey(slug))
}

func (c *Client) selectCandidates(ctx context.Context, priorities []string) ([]string, error) {
	result := []string{}
	c.ranks = map[string]int{}
	add := func(slug string, rank int) {
		previous, exists := c.ranks[slug]
		if !exists {
			c.ranks[slug] = rank
			result = append(result, slug)
		} else if rank < previous {
			c.ranks[slug] = rank
		}
	}
	for i, slug := range priorities {
		add(slug, i)
	}
	base := len(priorities)
	categoryMode := c.watchConfig.EffectiveWatchMode() == config.CategoryFirst
	var lastErr error
	// The explicit list is an override in every mode.
	if categoryMode {
		category, err := c.liveChannels(ctx, categoryKey(c.watchConfig.Category))
		if err != nil {
			lastErr = err
			c.logger.Printf("[WARNING] Category discovery: %v", err)
		} else {
			c.categorySlugs = map[string]bool{}
			for _, stream := range category {
				c.categorySlugs[stream.Slug] = true
				add(stream.Slug, base)
			}
		}
		base++
	}
	if c.token != "" {
		following, err := c.FollowedChannels(ctx)
		if err != nil {
			// Following needs account authentication even when anonymous viewing works.
			c.logger.Printf("[WARNING] Following unavailable; discovery remains enabled: %v", err)
		} else {
			c.followedSlugs = map[string]bool{}
			c.emit(Event{Kind: "following", Followed: following})
			for _, stream := range following {
				c.followedSlugs[stream.Slug] = true
				if stream.IsLive {
					add(stream.Slug, base)
				}
			}
		}
	}
	live, err := c.LiveChannels(ctx)
	if errors.Is(err, ErrAuth) {
		return nil, err
	}
	if err != nil {
		lastErr = err
	}
	for _, stream := range live {
		rank := base + 1
		if c.followedSlugs[stream.Slug] {
			rank = base
		}
		// A category member discovered through the general list still has the
		// category's rank, even if it fell outside the category's bounded pages.
		if categoryMode && categoryMatches(c.watchConfig.Category, stream.Category, stream.CategorySlug) {
			rank = len(priorities)
		}
		add(stream.Slug, rank)
	}
	// Keep equal-rank order stable; explicit list order is already encoded in rank.
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && c.ranks[result[j]] < c.ranks[result[j-1]]; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result, lastErr
}

func (c *Client) channelRank(slug string) int {
	for i, name := range c.priorities {
		if name == slug {
			return i
		}
	}
	base := len(c.priorities)
	if c.watchConfig.EffectiveWatchMode() == config.CategoryFirst {
		if c.currentInfo.Livestream != nil && len(c.currentInfo.Livestream.Categories) > 0 {
			for _, category := range c.currentInfo.Livestream.Categories {
				if categoryMatches(c.watchConfig.Category, category.Name, category.Slug) {
					return base
				}
			}
		} else if c.categorySlugs[slug] {
			return base
		}
		base++
	}
	if c.followedSlugs[slug] {
		return base
	}
	return base + 1
}
