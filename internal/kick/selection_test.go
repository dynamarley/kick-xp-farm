package kick

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"
	"kick-bot/internal/config"
)

type selectionStream struct {
	slug, category string
	live           bool
}
type selectionFixture struct {
	streams        []selectionStream
	following      []FollowedChannel
	followingError bool
	categoryError  bool
	// Some streams are only available in the general directory. This verifies
	// category promotion even when a filtered scan is bounded or incomplete.
	generalOnly map[string]bool
}

func selectionClient(t *testing.T, fixture *selectionFixture, cfg config.Config) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/following":
			if fixture.followingError {
				w.WriteHeader(500)
				return
			}
			list := fixture.following
			if list == nil {
				list = []FollowedChannel{}
			}
			json.NewEncoder(w).Encode(map[string]any{"channels": list, "nextCursor": nil})
		case r.URL.Path == "/live":
			filter := r.URL.Query().Get("subcategory")
			if filter != "" && fixture.categoryError {
				w.WriteHeader(500)
				return
			}
			data := []any{}
			for i, stream := range fixture.streams {
				if !stream.live || filter != "" && (fixture.generalOnly[stream.slug] || filter != stream.category) {
					continue
				}
				data = append(data, map[string]any{"id": i + 1, "channel": map[string]any{"id": i + 1, "slug": stream.slug}, "categories": []CategoryInfo{{Name: stream.category, Slug: stream.category}}})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data, "next_page_url": nil})
		case strings.HasPrefix(r.URL.Path, "/channels/"):
			slug := strings.TrimPrefix(r.URL.Path, "/channels/")
			for i, stream := range fixture.streams {
				if stream.slug != slug {
					continue
				}
				var livestream any
				if stream.live {
					livestream = map[string]any{"id": (i + 1) * 10, "is_live": true, "categories": []CategoryInfo{{Name: stream.category, Slug: stream.category}}}
				}
				json.NewEncoder(w).Encode(map[string]any{"id": i + 1, "livestream": livestream})
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	c := New("dummy-token", log.New(io.Discard, "", 0)).WithWatching(cfg)
	c.http = &fhttp.Client{}
	c.discoveryURL = server.URL + "/live"
	c.followingURL = server.URL + "/following"
	c.apiURL = server.URL + "/channels/"
	c.priorities = cfg.PriorityChannels
	return c
}

func TestWatchingModeOrderAndCategoryFallback(t *testing.T) {
	for _, tc := range []struct {
		name, mode    string
		categoryLive  bool
		followingLive bool
		want          []string
	}{
		{"following", config.FollowingFirst, true, true, []string{"favorite", "followed", "discovery", "category"}},
		{"priority", config.PriorityFirst, true, true, []string{"favorite", "followed", "discovery", "category"}},
		{"category", config.CategoryFirst, true, true, []string{"favorite", "category", "followed", "discovery"}},
		{"category falls outside category to following", config.CategoryFirst, false, true, []string{"favorite", "followed", "discovery"}},
		{"category falls to discovery", config.CategoryFirst, false, false, []string{"favorite", "discovery"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &selectionFixture{streams: []selectionStream{{"discovery", "other", true}, {"followed", "other", tc.followingLive}, {"category", "just-chatting", tc.categoryLive}}, following: []FollowedChannel{{Slug: "followed", IsLive: tc.followingLive}}}
			c := selectionClient(t, fixture, config.Config{WatchMode: tc.mode, Category: "just-chatting", PriorityChannels: []string{"favorite"}})
			got, err := c.candidates(context.Background(), c.priorities)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v (%v), want %v", got, err, tc.want)
			}
		})
	}
}

func TestCategoryPromotionDeduplicationAndFollowingFailure(t *testing.T) {
	fixture := &selectionFixture{streams: []selectionStream{{"popular", "other", true}, {"matching-followed", "just-chatting", true}, {"matching", "just-chatting", true}}, following: []FollowedChannel{{Slug: "matching-followed", IsLive: true}}, generalOnly: map[string]bool{"matching-followed": true}}
	c := selectionClient(t, fixture, config.Config{WatchMode: config.CategoryFirst, Category: "Just Chatting"})
	got, err := c.candidates(context.Background(), nil)
	if err != nil || !reflect.DeepEqual(got, []string{"matching", "matching-followed", "popular"}) || c.ranks["matching-followed"] != 0 {
		t.Fatalf("category promotion failed: %v %v", got, err)
	}
	fixture.followingError = true
	fixture.categoryError = true
	got, _ = c.candidates(context.Background(), []string{"popular"})
	if !reflect.DeepEqual(got, []string{"popular", "matching-followed", "matching"}) {
		t.Fatalf("fallback lost usable candidates: %v", got)
	}
}

func TestHigherTierReopensAndSameTierStays(t *testing.T) {
	fixture := &selectionFixture{streams: []selectionStream{{"discovery", "other", true}, {"followed", "other", false}, {"favorite", "other", false}, {"category", "just-chatting", false}, {"other-followed", "other", true}}, following: []FollowedChannel{{Slug: "followed", IsLive: false}, {Slug: "other-followed", IsLive: true}}}
	c := selectionClient(t, fixture, config.Config{WatchMode: config.CategoryFirst, Category: "just-chatting", PriorityChannels: []string{"favorite"}})
	check := func(current, want string) {
		t.Helper()
		info, _, err := c.checkChannel(context.Background(), current)
		if err != nil {
			t.Fatal(err)
		}
		c.currentInfo = info
		got, err := c.higherPriority(context.Background(), current)
		if err != nil || got != want {
			t.Fatalf("from %s got %q (%v), want %q", current, got, err, want)
		}
	}
	check("discovery", "other-followed")
	fixture.streams[1].live = true
	fixture.following[0].IsLive = true
	check("other-followed", "") // A different followed channel cannot interrupt it.
	fixture.streams[3].live = true
	check("other-followed", "category")
	fixture.streams[2].live = true
	check("category", "favorite")
	check("favorite", "") // Explicit overrides never yield to a lower tier.
	fixture.streams[2].live = false
	fixture.streams = append(fixture.streams, selectionStream{"second-category", "just-chatting", true})
	check("category", "")
	fixture.streams[3].category = "other"
	check("category", "second-category") // Category changes demote the current stream.
}

func TestCategoryDirectoryRejectsMismatchedResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("subcategory") != "just-chatting" {
			t.Error("category filter missing")
		}
		io.WriteString(w, `{"data":[{"id":1,"channel":{"id":1,"slug":"wrong-category"},"categories":[{"name":"Other","slug":"other"}]}],"next_page_url":null}`)
	}))
	defer server.Close()
	c := New("", log.New(io.Discard, "", 0))
	c.http = &fhttp.Client{}
	c.discoveryURL = server.URL
	got, err := c.liveChannels(context.Background(), "just-chatting")
	if err != nil || len(got) != 0 {
		t.Fatalf("mismatched category accepted: %v %v", got, err)
	}
}
