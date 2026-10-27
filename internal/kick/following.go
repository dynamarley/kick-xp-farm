package kick

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"

	"kick-bot/internal/config"
)

type FollowedChannel struct {
	Slug     string `json:"channel_slug"`
	Username string `json:"user_username"`
	IsLive   bool   `json:"is_live"`
	Viewers  int    `json:"viewer_count"`
	Title    string `json:"session_title"`
	Category string `json:"category_name"`
}

// FollowedChannels fetches the account's complete followed list, including offline
// channels. Only cursor values are reused; remote URLs are never followed.
func (c *Client) FollowedChannels(ctx context.Context) ([]FollowedChannel, error) {
	if c.token == "" {
		return nil, ErrAuth
	}
	cleanup, err := c.initHTTP()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	result := []FollowedChannel{}
	seen := map[string]bool{}
	seenCursor := map[string]bool{}
	cursor := ""
	for page := 0; page < 100; page++ {
		endpoint, err := url.Parse(c.followingURL)
		if err != nil {
			return nil, errors.New("invalid followed channels address")
		}
		if cursor != "" {
			q := endpoint.Query()
			q.Set("cursor", cursor)
			endpoint.RawQuery = q.Encode()
		}
		var response struct {
			Channels []FollowedChannel `json:"channels"`
			Next     json.RawMessage   `json:"nextCursor"`
		}
		if err := c.getJSON(ctx, endpoint.String(), &response, false); err != nil {
			return nil, err
		}
		if response.Channels == nil {
			return nil, errors.New("followed channels response did not include a channels array")
		}
		for _, channel := range response.Channels {
			slugs, err := config.ParseChannels(channel.Slug)
			if err != nil || len(slugs) != 1 || seen[slugs[0]] {
				continue
			}
			channel.Slug = slugs[0]
			seen[channel.Slug] = true
			result = append(result, channel)
		}
		cursor, err = followingCursor(response.Next)
		if err != nil {
			return nil, err
		}
		if cursor == "" {
			sort.SliceStable(result, func(i, j int) bool {
				if result[i].IsLive != result[j].IsLive {
					return result[i].IsLive
				}
				if result[i].IsLive && result[i].Viewers != result[j].Viewers {
					return result[i].Viewers > result[j].Viewers
				}
				return result[i].Slug < result[j].Slug
			})
			return result, nil
		}
		if seenCursor[cursor] {
			return nil, errors.New("followed channels API repeated a pagination cursor")
		}
		seenCursor[cursor] = true
	}
	return nil, errors.New("followed channels exceeded the pagination limit")
}

func followingCursor(raw json.RawMessage) (string, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" || value == "false" || value == "0" {
		return "", nil
	}
	var cursor string
	if json.Unmarshal(raw, &cursor) == nil {
		return cursor, nil
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		if _, err := number.Int64(); err == nil {
			return string(number), nil
		}
	}
	return "", errors.New("followed channels API returned an invalid pagination cursor")
}
