package kick

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"kick-bot/internal/config"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

func dailyTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := New("dummy-reward-token", log.New(io.Discard, "", 0))
	c.http = &fhttp.Client{Timeout: time.Second}
	c.dailyURL = server.URL + "/challenges"
	return c
}

func dailyTestChallenge(now time.Time, state string) dailyChallenge {
	challenge := dailyChallenge{ID: "daily-watch", Recurrence: "daily", Status: state, Window: &rewardWindow{StartsAt: now.UTC().Truncate(24 * time.Hour), EndsAt: now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)}}
	challenge.Condition.Type = "watch_time_minutes"
	challenge.Condition.Progress, challenge.Condition.Threshold = 60, 60
	return challenge
}

func TestDailyRewardCollectsOncePerServerWindow(t *testing.T) {
	now := time.Date(2026, 10, 27, 23, 50, 0, 0, time.UTC)
	challenge := dailyTestChallenge(now, "in_progress")
	challenge.Condition.Progress = 59
	posts := 0
	c := dailyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dummy-reward-token" || r.Header.Get("Origin") != "https://kick.com" || r.Header.Get("X-App-Platform") != "web" {
			t.Error("daily request missing authentication or platform headers")
		}
		if r.Method == http.MethodPost {
			if r.URL.Path != "/challenges/daily-watch/claim" {
				t.Error("unexpected claim path")
			}
			body, _ := io.ReadAll(r.Body)
			if len(body) != 0 {
				t.Error("website's claim request has no body")
			}
			posts++
			io.WriteString(w, `{"data":{"reveal_type":"standard","winner":{"id":"card"}}}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []dailyChallenge{challenge}})
	})
	status, err := c.checkDailyRewards(context.Background(), now)
	if err != nil || status.State != "in_progress" || status.Progress != 59 || posts != 0 {
		t.Fatal("unready reward was claimed or progress lost")
	}
	challenge.Status = "claimable"
	challenge.Condition.Progress = 60
	for i := 0; i < 2; i++ {
		status, err = c.checkDailyRewards(context.Background(), now)
		if err != nil || status.State != "claimed" || posts != 1 {
			t.Fatal("ready reward not collected exactly once")
		}
	}
	// Server can return the same challenge ID after its UTC window resets.
	// The local timezone is already the next day before that reset.
	now = now.Add(20 * time.Minute).In(time.FixedZone("Istanbul", 3*60*60))
	challenge = dailyTestChallenge(now, "claimable")
	status, err = c.checkDailyRewards(context.Background(), now)
	if err != nil || status.Claimed != 1 || posts != 2 || len(c.claimedRewards) != 1 {
		t.Fatal("new server window did not reset claims")
	}
}

func TestDailyRewardsSkipOtherConditionsExpiredAndAlreadyClaimed(t *testing.T) {
	now := time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC)
	for _, kind := range []string{"claimed", "expired", "future", "missing window", "one off", "other condition", "unknown state", "invalid ID"} {
		t.Run(kind, func(t *testing.T) {
			challenge := dailyTestChallenge(now, "claimable")
			wantError := false
			switch kind {
			case "claimed":
				challenge.Status = "claimed"
			case "expired":
				challenge.Window.EndsAt = now
			case "future":
				challenge.Window.StartsAt = now.Add(time.Minute)
			case "missing window":
				challenge.Window = nil
			case "one off":
				challenge.Recurrence = "one_off"
			case "other condition":
				challenge.Condition.Type = "onboarding_completed"
			case "unknown state":
				challenge.Status = "new-schema"
				wantError = true
			case "invalid ID":
				challenge.ID = "../user/logout"
				wantError = true
			}
			c := dailyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("non-eligible challenge sent a mutation")
					w.WriteHeader(500)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"data": []dailyChallenge{challenge}})
			})
			_, err := c.checkDailyRewards(context.Background(), now)
			if (err != nil) != wantError {
				t.Fatalf("unexpected eligibility result: %v", err)
			}
		})
	}
}

func TestDailyClaimFailureRefetchesBeforeRetry(t *testing.T) {
	now := time.Now()
	challenge := dailyTestChallenge(now, "claimable")
	posts, gets := 0, 0
	c := dailyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets++
			json.NewEncoder(w).Encode(map[string]any{"data": []dailyChallenge{challenge}})
			return
		}
		posts++
		// Simulate a server-side success followed by an invalid/missing response.
		challenge.Status = "claimed"
		io.WriteString(w, `{"data":null}`)
	})
	if _, err := c.checkDailyRewards(context.Background(), now); err == nil {
		t.Fatal("unconfirmed claim was reported successful")
	}
	if len(c.claimedRewards) != 0 {
		t.Fatal("unconfirmed claim was cached as successful")
	}
	status, err := c.checkDailyRewards(context.Background(), now)
	if err != nil || status.State != "claimed" || posts != 1 || gets != 2 {
		t.Fatal("ambiguous mutation replayed before checking server status")
	}
	// Restart also uses the server's claimed status, without relying on memory.
	c.claimedRewards = nil
	if _, err := c.checkDailyRewards(context.Background(), now); err != nil || posts != 1 {
		t.Fatal("restart repeated an already collected reward")
	}
}

func TestDailyAPIRejectsBadResponsesAndDoesNotLeakSecrets(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"unauthorized", 401, `dummy-reward-token`}, {"forbidden", 403, `dummy-reward-token`}, {"rate limited", 429, `dummy-reward-token`}, {"server failure", 500, `dummy-reward-token`}, {"invalid JSON", 200, `dummy-reward-token`}, {"missing data", 200, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := dailyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "600")
				w.WriteHeader(tc.code)
				io.WriteString(w, tc.body)
			})
			_, err := c.checkDailyRewards(context.Background(), time.Now())
			if err == nil || strings.Contains(err.Error(), "dummy-reward-token") {
				t.Fatal("failed API accepted or secret exposed")
			}
			if tc.code == 401 && !errors.Is(err, ErrAuth) {
				t.Fatal("authentication failure lost")
			}
			if tc.code == 429 {
				var limited *dailyRateLimit
				if !errors.As(err, &limited) || limited.after != 10*time.Minute {
					t.Fatal("Retry-After ignored")
				}
			}
		})
	}
}

func TestDailyLoopRetriesAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var gets, posts atomic.Int32
	c := dailyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			n := gets.Add(1)
			if n == 1 {
				w.WriteHeader(500)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []dailyChallenge{dailyTestChallenge(time.Now(), "claimable")}})
			return
		}
		posts.Add(1)
		io.WriteString(w, `{"data":{"winner":{"id":"card"}}}`)
	})
	var logs bytes.Buffer
	c.logger = log.New(&logs, "", 0)
	c.dailyInterval = time.Millisecond
	c.WithEvents(func(e Event) {
		if e.Kind == "reward" && e.Reward.State == "claimed" {
			cancel()
		}
	})
	c.runDailyRewards(ctx)
	if gets.Load() != 2 || posts.Load() != 1 || !strings.Contains(logs.String(), "collected successfully") {
		t.Fatal("reward loop did not recover from a transient error")
	}
	if strings.Contains(logs.String(), "dummy-reward-token") {
		t.Fatal("reward logs exposed credentials")
	}
}

func TestRunDailyRewardsWhileWaitingAndDisabled(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprint(disabled), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var posts atomic.Int32
			c := dailyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/challenges":
					json.NewEncoder(w).Encode(map[string]any{"data": []dailyChallenge{dailyTestChallenge(time.Now(), "claimable")}})
				case "/challenges/daily-watch/claim":
					posts.Add(1)
					io.WriteString(w, `{"data":{"winner":{"id":"card"}}}`)
				case "/live":
					io.WriteString(w, `{"data":[]}`)
				case "/following":
					io.WriteString(w, `{"channels":[]}`)
				default:
					t.Error("unexpected network endpoint")
					http.NotFound(w, r)
				}
			})
			base := strings.TrimSuffix(c.dailyURL, "/challenges")
			c.discoveryURL = base + "/live"
			c.followingURL = base + "/following"
			c.WithWatching(config.Config{DisableDailyRewards: disabled})
			c.WithEvents(func(e Event) {
				if e.Kind == "reward" && (e.Reward.State == "claimed" || e.Reward.State == "disabled") {
					cancel()
				}
			})
			if err := c.Run(ctx, nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("run did not shut down cleanly: %v", err)
			}
			if disabled && posts.Load() != 0 || !disabled && posts.Load() != 1 {
				t.Fatal("daily reward preference was ignored")
			}
		})
	}
}

func TestDailyLoopCancellationInterruptsPendingRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	c := dailyTestClient(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
	done := make(chan struct{})
	go func() { defer close(done); c.runDailyRewards(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reward request did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reward loop blocked shutdown")
	}
}
