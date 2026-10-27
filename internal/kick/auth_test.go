package kick

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/gorilla/websocket"
	"kick-bot/internal/config"
)

func TestViewerVerificationRequiresWebSocketUpgrade(t *testing.T) {
	for _, status := range []int{101, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			closed := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					io.WriteString(w, `{"data":{"token":"sample-viewer-secret"}}`)
					return
				}
				if r.URL.Query().Get("token") != "sample-viewer-secret" {
					t.Error("viewer token missing")
				}
				cookie, err := r.Cookie("kick_session")
				if err != nil || cookie.Value != "dummy-session" {
					t.Error("session cookie missing")
				}
				if status != 101 {
					w.WriteHeader(status)
					return
				}
				conn, err := (&websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				conn.SetReadDeadline(time.Now().Add(3 * time.Second))
				conn.ReadMessage()
				closed <- struct{}{}
			}))
			defer server.Close()
			c := New("dummy-token", log.New(io.Discard, "", 0)).WithSession(config.Config{KickSession: "dummy-session"})
			c.http = &fhttp.Client{}
			c.tokenURL = server.URL + "/token"
			c.socketURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/socket"
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := c.VerifyViewerConnection(ctx)
			if (err == nil) != (status == 101) {
				t.Fatalf("incorrect connection result: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "sample-viewer-secret") {
				t.Fatal("viewer token leaked")
			}
			if status == 101 {
				select {
				case <-closed:
				case <-ctx.Done():
					t.Fatal("probe did not close its WebSocket")
				}
			}
		})
	}
}

func TestVerifyRequiresAccountIdentity(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{}`, false}, {`{"id":0,"username":"sample"}`, false},
		{`{"id":42}`, false}, {`{"id":42,"username":"sample"}`, true},
		{`{"data":{"id":42,"username":"sample"}}`, true},
		{`{"user":{"id":42,"username":"sample"}}`, true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/protected" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			c := New("dummy-account-token", log.New(io.Discard, "", 0))
			c.accountURL, c.http = server.URL, &fhttp.Client{}
			c.accountCheckURL = server.URL + "/protected"
			account, err := c.VerifyAccount(context.Background())
			if tc.valid && (err != nil || account.ID != 42 || account.Username != "sample") {
				t.Fatalf("valid identity rejected: %v", err)
			}
			if !tc.valid && !errors.Is(err, ErrAuth) {
				t.Fatalf("empty identity accepted: %v", err)
			}
		})
	}
}

func TestVerifyBearerThenSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/protected" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer dummy-account-token" {
			t.Error("missing Bearer header")
		}
		cookie, err := r.Cookie("kick_session")
		if err != nil {
			io.WriteString(w, `{}`)
			return
		}
		if cookie.Value != "dummy-session%3D" {
			t.Error("session value changed")
		}
		for name, expected := range map[string]string{"XSRF-TOKEN": "dummy+xsrf%3D", "kick_session_id": "dummy-id", "sessiontoken": "dummy-account-token"} {
			cookie, err := r.Cookie(name)
			if err != nil || cookie.Value != expected {
				t.Errorf("incorrect %s cookie", name)
			}
		}
		if r.Header.Get("X-XSRF-TOKEN") != "dummy+xsrf=" {
			t.Error("XSRF header must be URL-decoded without changing plus signs")
		}
		io.WriteString(w, `{"id":42,"username":"sample"}`)
	}))
	defer server.Close()
	c := New("dummy-account-token", log.New(io.Discard, "", 0))
	c.http, c.accountURL = &fhttp.Client{}, server.URL
	c.accountCheckURL = server.URL + "/protected"
	if _, err := c.VerifyAccount(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatal("expected Bearer rejection")
	}
	c.WithSession(config.Config{XSRFToken: "dummy+xsrf%3D", KickSession: "dummy-session%3D", KickSessionID: "dummy-id"})
	if _, err := c.VerifyAccount(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyIdentityDoesNotImplyInvalidToken(t *testing.T) {
	for _, status := range []int{200, 401, 403, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			checks := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/protected" {
					checks++
					w.WriteHeader(status)
					io.WriteString(w, `{"verified":false}`)
					return
				}
				io.WriteString(w, `{}`)
			}))
			defer server.Close()
			c := New("dummy-token", log.New(io.Discard, "", 0))
			c.http, c.accountURL, c.accountCheckURL = &fhttp.Client{}, server.URL, server.URL+"/protected"
			_, err := c.VerifyAccount(context.Background())
			if checks != 1 || err == nil {
				t.Fatal("protected account check was skipped")
			}
			if errors.Is(err, ErrAuth) != (status == 401) {
				t.Fatal("incorrectly classified account rejection")
			}
			if errors.Is(err, ErrAccountIdentity) != (status == 200) {
				t.Fatal("missing identity must remain inconclusive after successful protected request")
			}
			if errors.Is(err, ErrAccess) != (status == 403) {
				t.Fatal("incorrect access classification")
			}
		})
	}
}

func TestInvalidSessionNeverReachesNetwork(t *testing.T) {
	c := New("dummy-account-token", log.New(io.Discard, "", 0)).WithSession(config.Config{XSRFToken: "secret%0d%0aInjected%3Ayes"})
	c.http = &fhttp.Client{}
	if _, err := c.VerifyAccount(context.Background()); err == nil {
		t.Fatal("encoded header injection accepted")
	}
}
