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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/gorilla/websocket"
	"kick-bot/internal/config"
)

func TestAutomaticTierSwitchingOverWebSocket(t *testing.T) {
	for _, scenario := range []string{"followed reopens", "category reopens", "ended to next followed", "ended to discovery"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			var phase atomic.Int32
			var active, peak atomic.Int32
			var mu sync.Mutex
			var watched []string
			var sockets sync.WaitGroup
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ready := phase.Load() > 0
				ended := strings.HasPrefix(scenario, "ended")
				switch r.URL.Path {
				case "/following":
					list := []FollowedChannel{}
					switch scenario {
					case "followed reopens":
						list = append(list, FollowedChannel{Slug: "next", IsLive: ready})
					case "ended to next followed":
						list = append(list, FollowedChannel{Slug: "current", IsLive: !ready}, FollowedChannel{Slug: "next", IsLive: true})
					case "ended to discovery":
						list = append(list, FollowedChannel{Slug: "current", IsLive: !ready})
					}
					json.NewEncoder(w).Encode(map[string]any{"channels": list, "nextCursor": nil})
				case "/live":
					data := []any{}
					if r.URL.Query().Get("subcategory") != "" {
						if scenario == "category reopens" && ready {
							data = append(data, map[string]any{"id": 20, "channel": map[string]any{"id": 2, "slug": "next"}, "categories": []CategoryInfo{{Name: "Just Chatting", Slug: "just-chatting"}}})
						}
					} else {
						if !ended || !ready {
							data = append(data, map[string]any{"id": 10, "channel": map[string]any{"id": 1, "slug": "current"}})
						}
						if scenario == "ended to discovery" && ready {
							data = append(data, map[string]any{"id": 20, "channel": map[string]any{"id": 2, "slug": "next"}})
						}
					}
					json.NewEncoder(w).Encode(map[string]any{"data": data, "next_page_url": nil})
				case "/channels/current":
					if ended && ready {
						io.WriteString(w, `{"id":1,"livestream":null}`)
					} else {
						io.WriteString(w, `{"id":1,"livestream":{"id":10,"categories":[{"slug":"other"}]}}`)
					}
				case "/channels/next":
					io.WriteString(w, `{"id":2,"livestream":{"id":20,"categories":[{"slug":"just-chatting"}]}}`)
				case "/token":
					io.WriteString(w, `{"token":"dummy-viewer"}`)
				case "/connect":
					conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					sockets.Add(1)
					defer sockets.Done()
					defer conn.Close()
					// Read all client messages through closure; each replacement handshake
					// must arrive after the previous connection has closed on the server.
					conn.SetReadDeadline(time.Now().Add(5 * time.Second))
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
					n := active.Add(1)
					defer active.Add(-1)
					for old := peak.Load(); n > old; old = peak.Load() {
						if peak.CompareAndSwap(old, n) {
							break
						}
					}
					mu.Lock()
					watched = append(watched, handshake.Data.Message.ChannelID)
					mu.Unlock()
					if handshake.Data.Message.ChannelID == "1" {
						phase.Store(1)
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
			cfg := config.Config{DisableDailyRewards: true}
			if scenario == "category reopens" {
				cfg.WatchMode = config.CategoryFirst
				cfg.Category = "just-chatting"
			}
			c := New("dummy-account", log.New(io.Discard, "", 0)).WithWatching(cfg)
			c.http = &fhttp.Client{Timeout: time.Second}
			c.apiURL = server.URL + "/channels/"
			c.discoveryURL = server.URL + "/live"
			c.followingURL = server.URL + "/following"
			c.tokenURL = server.URL + "/token"
			c.socketURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/connect"
			c.pollInterval = 10 * time.Millisecond
			if err := c.Run(ctx, nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("switch did not complete: %v", err)
			}
			sockets.Wait()
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(watched, []string{"1", "2"}) {
				t.Fatalf("watch order: %v", watched)
			}
			if peak.Load() != 1 || active.Load() != 0 {
				t.Fatal("stream connections overlapped or remained open")
			}
		})
	}
}
