package kick

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"kick-bot/internal/config"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/gorilla/websocket"
)

func TestAccountTokenAndWebsocketLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var received []map[string]any
	complete := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dummy-account-token" {
			t.Error("account token not forwarded")
		}
		cookie, err := r.Cookie("sessiontoken")
		if err != nil || cookie.Value != "dummy-account-token" {
			t.Error("session cookie missing")
		}
		for name, expected := range map[string]string{"XSRF-TOKEN": "sample%3D", "kick_session": "sample-session", "kick_session_id": "sample-id"} {
			cookie, err := r.Cookie(name)
			if err != nil || cookie.Value != expected {
				t.Errorf("missing session credential: %s", name)
			}
		}
		if r.Header.Get("X-XSRF-TOKEN") != "sample=" {
			t.Error("missing decoded XSRF header")
		}
		switch r.URL.Path {
		case "/channels/example":
			io.WriteString(w, `{"id":123,"livestream":{"id":456,"is_live":true}}`)
		case "/token":
			if r.Header.Get("X-Client-Token") != clientToken || r.Header.Get("X-Request-Id") == "" {
				t.Error("viewer headers missing")
			}
			io.WriteString(w, `{"data":{"token":"dummy-viewer-token+/?="}}`)
		case "/connect":
			if r.URL.Query().Get("token") != "dummy-viewer-token+/?=" {
				t.Error("viewer token not URL encoded")
			}
			conn, err := (&websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "https://kick.com" }}).Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			defer close(complete)
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			for i := 0; i < 2; i++ {
				var msg map[string]any
				if err := conn.ReadJSON(&msg); err != nil {
					t.Error(err)
					return
				}
				received = append(received, msg)
			}
			cancel()
			// The client must close its socket when the context is cancelled.
			if _, _, err := conn.ReadMessage(); err == nil {
				t.Error("expected closed socket")
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := New("dummy-account-token", log.New(io.Discard, "", 0)).WithSession(config.Config{XSRFToken: "sample%3D", KickSession: "sample-session", KickSessionID: "sample-id"})
	var events []Event
	c.WithEvents(func(event Event) { events = append(events, event) })
	c.http = &fhttp.Client{Timeout: time.Second}
	c.apiURL, c.tokenURL = server.URL+"/channels/", server.URL+"/token"
	c.socketURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/connect"
	info, live, err := c.checkChannel(ctx, "example")
	if err != nil || !live {
		t.Fatalf("live check failed: %v", err)
	}
	if err := c.watch(ctx, "example", info); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	select {
	case <-complete:
	case <-time.After(5 * time.Second):
		t.Fatal("socket did not close")
	}
	if len(received) != 2 || received[0]["type"] != "channel_handshake" || received[1]["type"] != "user_event" {
		t.Fatal("missing initial events")
	}
	message := received[1]["data"].(map[string]any)["message"].(map[string]any)
	if message["channel_id"] != float64(123) || message["livestream_id"] != float64(456) {
		t.Fatal("incorrect tracking identifiers")
	}
	watched, sent := false, 0
	for _, event := range events {
		if event.Kind == "state" && event.State == Watching {
			watched = true
		}
		if event.Kind == "sent" {
			sent++
		}
	}
	if !watched || sent != 2 {
		t.Fatal("connection observer did not receive real socket events")
	}
}

func TestHTTPFailuresDoNotExposeSecrets(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				json.NewEncoder(w).Encode(map[string]string{"token": "dummy-account-token"})
			}))
			defer server.Close()
			c := New("dummy-account-token", log.New(io.Discard, "", 0))
			c.http = &fhttp.Client{}
			var result any
			err := c.getJSON(context.Background(), server.URL, &result, true)
			if err == nil || strings.Contains(err.Error(), "dummy-account-token") {
				t.Fatal("missing or unsafe HTTP error")
			}
			if errors.Is(err, ErrAuth) != (status == 401) {
				t.Fatal("incorrect authentication classification")
			}
		})
	}
}

func TestWebsocketErrorDoesNotExposeViewerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			io.WriteString(w, `{"token":"dummy-viewer-secret"}`)
			return
		}
		http.Error(w, "upgrade rejected", http.StatusForbidden)
	}))
	defer server.Close()
	c := New("dummy-account-token", log.New(io.Discard, "", 0))
	c.http = &fhttp.Client{}
	c.tokenURL = server.URL + "/token"
	c.socketURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/connect"
	err := c.watch(context.Background(), "example", channel{})
	if err == nil || strings.Contains(err.Error(), "dummy-viewer-secret") {
		t.Fatal("missing or unsafe websocket error")
	}
}
