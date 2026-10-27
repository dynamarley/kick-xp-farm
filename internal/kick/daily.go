package kick

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

type rewardWindow struct {
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
}

type dailyChallenge struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Recurrence string `json:"recurrence"`
	Status     string `json:"status"`
	Condition  struct {
		Type      string  `json:"type"`
		Progress  float64 `json:"progress"`
		Threshold float64 `json:"threshold"`
	} `json:"condition"`
	Window *rewardWindow `json:"window"`
}

// DailyRewardStatus contains server progress only, never a local watch timer.
type DailyRewardStatus struct {
	State              string
	Progress, Required float64
	ResetsAt           time.Time
	Claimed            int
}

type dailyRateLimit struct{ after time.Duration }

func (e *dailyRateLimit) Error() string { return "daily reward API rate limited; retrying later" }

// Daily rewards use Kick's web API, separate from the legacy channel API.
// The list and claim paths match the website's gamification client.
func (c *Client) dailyRequest(ctx context.Context, method, endpoint string, dst any) error {
	req, err := fhttp.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return errors.New("could not create the daily reward request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-App-Platform", "web")
	req.Header.Set("Origin", "https://kick.com")
	req.Header.Set("Referer", "https://kick.com/")
	req.Header.Set("Cache-Control", "no-cache")
	if err := c.applyAuth(req.Header); err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("could not reach the daily reward API")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return ErrAuth
	case http.StatusForbidden:
		return ErrAccess
	case http.StatusTooManyRequests:
		after := time.Duration(0)
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 && seconds <= 86400 {
			after = time.Duration(seconds) * time.Second
		} else if at, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil {
			after = time.Until(at)
		}
		return &dailyRateLimit{after: after}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("daily reward API returned HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(dst); err != nil {
		return errors.New("daily reward API returned an invalid or empty response")
	}
	return nil
}

func (c *Client) dailyChallenges(ctx context.Context) ([]dailyChallenge, error) {
	var response struct {
		Data []dailyChallenge `json:"data"`
	}
	if err := c.dailyRequest(ctx, fhttp.MethodGet, c.dailyURL, &response); err != nil {
		return nil, err
	}
	if response.Data == nil {
		return nil, errors.New("daily reward API did not include a challenges array")
	}
	return response.Data, nil
}

func validChallengeID(id string) bool {
	return id != "" && len(id) <= 128 && !strings.ContainsFunc(id, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	})
}

func (c *Client) claimDailyReward(ctx context.Context, id string) error {
	if !validChallengeID(id) {
		return errors.New("daily reward API returned an invalid challenge ID")
	}
	var response struct {
		Data json.RawMessage `json:"data"`
	}
	endpoint := strings.TrimRight(c.dailyURL, "/") + "/" + url.PathEscape(id) + "/claim"
	if err := c.dailyRequest(ctx, fhttp.MethodPost, endpoint, &response); err != nil {
		return err
	}
	// HTTP success alone is insufficient: the website also requires a claim
	// payload before marking a reward collected. Never log the raw payload.
	var data map[string]json.RawMessage
	if json.Unmarshal(response.Data, &data) != nil || len(data) == 0 {
		return errors.New("daily reward claim was not confirmed by Kick")
	}
	return nil
}

func (c *Client) checkDailyRewards(ctx context.Context, now time.Time) (DailyRewardStatus, error) {
	status := DailyRewardStatus{State: "unavailable"}
	challenges, err := c.dailyChallenges(ctx)
	if err != nil {
		return status, err
	}
	if c.claimedRewards == nil {
		c.claimedRewards = map[string]time.Time{}
	}
	for key, expires := range c.claimedRewards {
		if !now.Before(expires) {
			delete(c.claimedRewards, key)
		}
	}
	status.State = "none"
	for _, challenge := range challenges {
		if challenge.Recurrence != "daily" || challenge.Condition.Type != "watch_time_minutes" {
			continue
		}
		window := challenge.Window
		// Use the server's daily window, including its timezone, across midnight
		// and channel switches. Expired/stale challenge responses cannot be claimed.
		if window == nil || window.StartsAt.IsZero() || !window.EndsAt.After(window.StartsAt) || now.Before(window.StartsAt) || !now.Before(window.EndsAt) {
			continue
		}
		key := challenge.ID + "|" + window.StartsAt.UTC().Format(time.RFC3339Nano) + "|" + window.EndsAt.UTC().Format(time.RFC3339Nano)
		state := challenge.Status
		if state != "in_progress" && state != "claimable" && state != "claimed" {
			return DailyRewardStatus{State: "unavailable"}, errors.New("daily reward API returned an unknown challenge status")
		}
		if _, ok := c.claimedRewards[key]; ok {
			state = "claimed"
		}
		if state == "claimable" {
			if err := c.claimDailyReward(ctx, challenge.ID); err != nil {
				return DailyRewardStatus{State: "unavailable"}, err
			}
			c.claimedRewards[key] = window.EndsAt
			state = "claimed"
			status.Claimed++
			c.logger.Print("[REWARD] Daily watch reward collected successfully.")
		}
		// Show the soonest unfinished watch goal; claimed event rewards must not
		// hide an ordinary daily reward that still needs watch time.
		if status.State == "none" || state != "claimed" && (status.State == "claimed" || challenge.Condition.Threshold < status.Required) {
			status.State = state
			status.Progress = challenge.Condition.Progress
			status.Required = challenge.Condition.Threshold
			status.ResetsAt = window.EndsAt
		}
	}
	return status, nil
}

func (c *Client) runDailyRewards(ctx context.Context) {
	interval := c.dailyInterval
	if interval <= 0 {
		interval = time.Minute
	}
	failures := 0
	previous := ""
	for ctx.Err() == nil {
		requestCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		status, err := c.checkDailyRewards(requestCtx, time.Now())
		cancel()
		if ctx.Err() != nil {
			return
		}
		delay := interval
		if err != nil {
			failures++
			// Back off failed requests. Each retry re-reads status first, including
			// after an ambiguous POST timeout, so a successful claim is not replayed.
			delay = interval * time.Duration(1<<min(failures, 4))
			var rateLimit *dailyRateLimit
			if errors.As(err, &rateLimit) {
				delay = max(delay, rateLimit.after)
			}
			if err.Error() != previous {
				c.logger.Printf("[WARNING] Daily rewards: %v. Watching continues; retrying later.", err)
			}
			previous = err.Error()
		} else {
			failures = 0
			previous = ""
		}
		c.emit(Event{Kind: "reward", Reward: &status})
		if err := wait(ctx, delay); err != nil {
			return
		}
	}
}
