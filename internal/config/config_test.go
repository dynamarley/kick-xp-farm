package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestSaveLoadAndReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings", "config.json")
	cfg := Config{Token: "test-account-token", XSRFToken: "sample%3D", KickSession: "sample-session", KickSessionID: "sample-id", PriorityChannels: []string{"channel_a", "channel-b"}, WatchMode: CategoryFirst, Category: "just-chatting", DisableDailyRewards: true}
	for _, token := range []string{"test-account-token", "replacement-token"} {
		cfg.Token = token
		if err := Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil || !reflect.DeepEqual(got, cfg) {
			t.Fatalf("round trip mismatch: %v", err)
		}
	}
	if runtime.GOOS != "windows" {
		for name, mode := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != mode {
				t.Fatalf("permissions: %o, want %o", info.Mode().Perm(), mode)
			}
		}
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".config-*.tmp"))
	if len(files) != 0 {
		t.Fatal("temporary credential file left behind")
	}
}

func TestRejectInvalidSessionCookies(t *testing.T) {
	for _, bad := range []string{"value;other=secret", "value\r\nHeader: secret", "\"quoted\"", "a b", "a\\b"} {
		if err := (Config{KickSession: bad}).ValidateSession(); err == nil {
			t.Fatal("invalid session cookie accepted")
		}
	}
	for _, bad := range []string{"bad%", "secret%0aHeader", "secret%00"} {
		if err := (Config{XSRFToken: bad}).ValidateSession(); err == nil {
			t.Fatal("invalid XSRF token accepted")
		}
	}
}

func TestLegacyChannelsBecomePriorities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"token":"dummy-token","channels":["Example","example","second"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || strings.Join(cfg.PriorityChannels, ",") != "example,second" || len(cfg.Channels) != 0 {
		t.Fatal("legacy migration failed")
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), `"channels"`) {
		t.Fatal("legacy field persisted")
	}
}

func TestInvalidConfigDoesNotExposeToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{"token":"test-secret",broken}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected invalid config error")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(data) {
		t.Fatal("invalid config was overwritten")
	}
}

func TestNormalizeToken(t *testing.T) {
	got, err := NormalizeToken("  Bearer example|token  ")
	if err != nil || got != "example|token" {
		t.Fatal("Bearer prefix not normalized")
	}
	for _, bad := range []string{"", "Bearer ", "a\nb", "a b", "a\x00b"} {
		if _, err := NormalizeToken(bad); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
}

func TestParseChannels(t *testing.T) {
	got, err := ParseChannels(" Channel_A,channel-b\nchannel_a\r\n")
	if err != nil || !reflect.DeepEqual(got, []string{"channel_a", "channel-b"}) {
		t.Fatal("channel normalization failed")
	}
	for _, bad := range []string{"", " ,\n", "https://kick.com/example", "a/b"} {
		if _, err := ParseChannels(bad); err == nil {
			t.Fatal("invalid channel accepted")
		}
	}
}
