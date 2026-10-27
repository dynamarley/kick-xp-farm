package kick

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"
)

func TestFollowingPaginationIncludesOfflineChannels(t *testing.T) {
	var cursors []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dummy-account-token" {
			t.Error("account Bearer header missing")
		}
		cursor := r.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		switch cursor {
		case "":
			io.WriteString(w, `{"channels":[{"channel_slug":"Offline","is_live":false},{"channel_slug":"live-low","is_live":true,"viewer_count":10},{"channel_slug":"bad/url"}],"nextCursor":20}`)
		case "20":
			io.WriteString(w, `{"channels":[{"channel_slug":"offline","is_live":false},{"channel_slug":"live-high","is_live":true,"viewer_count":99,"category_name":"Games"}],"nextCursor":"a+b&c"}`)
		case "a+b&c":
			io.WriteString(w, `{"channels":[],"nextCursor":null}`)
		default:
			t.Error("cursor changed or was not encoded")
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	c := New("dummy-account-token", log.New(io.Discard, "", 0))
	c.http = &fhttp.Client{}
	c.followingURL = server.URL
	channels, err := c.FollowedChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, c := range channels {
		slugs = append(slugs, c.Slug)
	}
	if !reflect.DeepEqual(slugs, []string{"live-high", "live-low", "offline"}) || !reflect.DeepEqual(cursors, []string{"", "20", "a+b&c"}) {
		t.Fatal("following pagination, ordering, deduplication, or offline channels incorrect")
	}
	if channels[0].Category != "Games" {
		t.Fatal("category lost")
	}
}

func TestFollowingFailuresRemainErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		auth       bool
	}{
		{"unauthorized", `{"secret":"dummy-account-token"}`, 401, true},
		{"access restriction", `{}`, 403, false},
		{"unexpected response", `{}`, 200, false},
		{"repeated cursor", `{"channels":[],"nextCursor":20}`, 200, false},
		{"invalid cursor", `{"channels":[],"nextCursor":{"url":"https://invalid.example"}}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer server.Close()
			c := New("dummy-account-token", log.New(io.Discard, "", 0))
			c.http = &fhttp.Client{}
			c.followingURL = server.URL
			_, err := c.FollowedChannels(context.Background())
			if err == nil || errors.Is(err, ErrAuth) != tc.auth || strings.Contains(err.Error(), "dummy-account-token") {
				t.Fatalf("missing, misclassified, or unsafe error: %v", err)
			}
		})
	}
}

func TestFollowingEmptyListAndMissingToken(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		io.WriteString(w, `{"channels":[],"nextCursor":0}`)
	}))
	defer server.Close()
	c := New("", log.New(io.Discard, "", 0))
	c.http = &fhttp.Client{}
	c.followingURL = server.URL
	if _, err := c.FollowedChannels(context.Background()); !errors.Is(err, ErrAuth) || requests != 0 {
		t.Fatal("missing token should fail before a request")
	}
	c.token = "dummy-token"
	channels, err := c.FollowedChannels(context.Background())
	if err != nil || len(channels) != 0 || requests != 1 {
		t.Fatal("empty followed list was not accepted")
	}
}
