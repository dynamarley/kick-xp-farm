package kick

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/gorilla/websocket"
)

func TestDiscoveryPaginationAndPriorityOrder(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("sort") != "desc" {
			t.Error("missing directory sort")
		}
		if r.URL.Query().Get("page") == "1" {
			io.WriteString(w, `{"data":[{"id":1,"is_live":true,"channel":{"id":11,"slug":"FIRST"}},{"id":2,"is_live":false,"channel":{"id":12,"slug":"offline"}},{"id":3,"channel":{"id":13,"slug":"banned","is_banned":true}},{"id":4,"channel":{"id":14,"slug":"\u001b[31mbad"}}],"next_page_url":"https://untrusted.invalid/not-followed"}`)
		} else {
			io.WriteString(w, `{"data":[{"id":1,"channel":{"id":11,"slug":"first"}},{"id":5,"session_title":"Example","viewer_count":42,"channel":{"id":15,"slug":"second"}}],"next_page_url":null}`)
		}
	}))
	defer server.Close()
	c := New("", log.New(io.Discard, "", 0))
	c.http = &fhttp.Client{}
	c.discoveryURL = server.URL
	got, err := c.candidates(context.Background(), []string{"outside", "second"})
	if err != nil || !reflect.DeepEqual(got, []string{"outside", "second", "first"}) || requests != 2 {
		t.Fatalf("incorrect discovery or ordering: %v %v", got, err)
	}
}

func TestDiscoveryFailurePreservesExplicitPriorities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer server.Close()
	c := New("", log.New(io.Discard, "", 0))
	c.http = &fhttp.Client{}
	c.discoveryURL = server.URL
	got, err := c.candidates(context.Background(), []string{"favorite"})
	if err == nil || !reflect.DeepEqual(got, []string{"favorite"}) {
		t.Fatal("favorites lost on discovery failure")
	}
}

func TestDiscoveryRejectsUnexpectedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"message":"changed response"}`) }))
	defer server.Close()
	c := New("", log.New(io.Discard, "", 0))
	c.http = &fhttp.Client{}
	c.discoveryURL = server.URL
	if _, err := c.LiveChannels(context.Background()); err == nil {
		t.Fatal("unexpected schema accepted as empty directory")
	}
}

func TestRunAutomaticallySwitchesToNewLiveFavorite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var fallbackWatching atomic.Bool
	var mu sync.Mutex
	var watched []string
	var sockets sync.WaitGroup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/following":
			io.WriteString(w, `{"channels":[],"nextCursor":null}`)
		case "/live":
			io.WriteString(w, `{"data":[{"id":20,"is_live":true,"channel":{"id":2,"slug":"fallback"}}],"next_page_url":null}`)
		case "/channels/favorite":
			if fallbackWatching.Load() {
				io.WriteString(w, `{"id":1,"livestream":{"id":10,"is_live":true}}`)
			} else {
				io.WriteString(w, `{"id":1,"livestream":null}`)
			}
		case "/channels/fallback":
			io.WriteString(w, `{"id":2,"livestream":{"id":20,"is_live":true}}`)
		case "/token":
			io.WriteString(w, `{"data":{"token":"dummy-viewer-token"}}`)
		case "/connect":
			conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			sockets.Add(1)
			defer sockets.Done()
			defer conn.Close()
			conn.SetReadDeadline(time.Now().Add(7 * time.Second))
			var handshake struct {
				Data struct {
					Message struct {
						ChannelID string `json:"channelId"`
					} `json:"message"`
				} `json:"data"`
			}
			if err := conn.ReadJSON(&handshake); err != nil {
				t.Error(err)
				return
			}
			var tracking any
			if err := conn.ReadJSON(&tracking); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			watched = append(watched, handshake.Data.Message.ChannelID)
			mu.Unlock()
			if handshake.Data.Message.ChannelID == "2" {
				fallbackWatching.Store(true)
			} else {
				cancel()
			}
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := New("dummy-account-token", log.New(io.Discard, "", 0))
	c.http = &fhttp.Client{Timeout: time.Second}
	c.apiURL = server.URL + "/channels/"
	c.discoveryURL = server.URL + "/live"
	c.followingURL = server.URL + "/following"
	c.tokenURL = server.URL + "/token"
	c.socketURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/connect"
	c.pollInterval = 10 * time.Millisecond
	c.watchConfig.DisableDailyRewards = true
	if err := c.Run(ctx, []string{"favorite"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("automatic switch failed: %v", err)
	}
	sockets.Wait()
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(watched, []string{"2", "1"}) {
		t.Fatalf("unexpected watch order: %v", watched)
	}
}

func TestHigherPrioritySkipsUnavailableFavorite(t *testing.T) {
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		if r.URL.Path == "/broken" {
			w.WriteHeader(404)
			return
		}
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		json.NewEncoder(w).Encode(map[string]any{"id": id, "livestream": map[string]any{"id": id * 10, "is_live": true}})
	}))
	defer server.Close()
	c := New("", log.New(io.Discard, "", 0))
	c.http = &fhttp.Client{}
	c.apiURL = server.URL + "/"
	c.priorities = []string{"broken", "1", "2"}
	slug, err := c.higherPriority(context.Background(), "2")
	if err != nil || slug != "1" || len(requested) != 2 {
		t.Fatal("unavailable favorite blocked another favorite")
	}
}
