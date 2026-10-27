package cli

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"

	"kick-bot/internal/config"
	"kick-bot/internal/kick"
)

func TestCredentialLifecycle(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(path, config.Config{PriorityChannels: []string{"example"}}); err != nil {
		t.Fatal(err)
	}
	watchCount := 0
	app := application{verify: verifiedTestAccount, watch: func(ctx context.Context, cfg config.Config, logger *log.Logger) error {
		if cfg.Token != "dummy-account-token" {
			t.Fatal("runner did not receive saved token")
		}
		watchCount++
		return nil
	}}
	run := func(input string, args ...string) (string, error) {
		t.Helper()
		file, err := os.CreateTemp(root, "stdin-*")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if _, err := file.WriteString(input); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = app.run(args, file, &out)
		if strings.Contains(out.String(), "dummy-account-token") {
			t.Fatal("token leaked to output")
		}
		return out.String(), err
	}
	if _, err := run("Bearer dummy-account-token\n", "login"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Token != "dummy-account-token" || len(cfg.PriorityChannels) != 1 {
		t.Fatal("login did not persist credentials and channels")
	}
	out, err := run("", "status")
	if err != nil || !strings.Contains(out, "saved (not verified") {
		t.Fatal("saved token not detected")
	}
	// Default startup needs only a token, even without any channel preferences.
	cfg.PriorityChannels = nil
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	out, err = run("")
	if err != nil || strings.Contains(out, "Kick Bearer token:") || strings.Contains(out, "Channel names") || watchCount != 1 {
		t.Fatal("second launch should start automatically without prompting")
	}
	cfg.PriorityChannels = []string{"example"}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := run("", "logout"); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(path)
	if err != nil || cfg.Token != "" || len(cfg.PriorityChannels) != 1 {
		t.Fatal("logout must remove only the token")
	}
	data, _ := os.ReadFile(path)
	if bytes.Contains(data, []byte("dummy-account-token")) {
		t.Fatal("logout left token in config")
	}
}

func TestHelpDoesNotNeedConfig(t *testing.T) {
	t.Setenv("APPDATA", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	var out bytes.Buffer
	if err := Run([]string{"--help"}, os.Stdin, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "login") {
		t.Fatal("missing help")
	}
}

func TestInvalidPriorityDoesNotSaveToken(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	var out bytes.Buffer
	err := Run([]string{"settings", "--priority", "https://kick.com/example"}, os.Stdin, &out)
	if err == nil || strings.Contains(out.String(), "Bearer token") {
		t.Fatal("bad channels should fail before credential prompt")
	}
	path, _ := config.Path()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unexpected config write")
	}
}

func TestFirstLaunchOnlyAsksForToken(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	file, err := os.CreateTemp(root, "input")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	file.WriteString("dummy-token\n")
	file.Seek(0, 0)
	called := false
	app := application{verify: verifiedTestAccount, watch: func(ctx context.Context, cfg config.Config, logger *log.Logger) error {
		called = true
		if cfg.Token != "dummy-token" || len(cfg.PriorityChannels) != 0 {
			t.Fatal("unexpected startup settings")
		}
		return nil
	}}
	var out bytes.Buffer
	if err := app.run(nil, file, &out); err != nil {
		t.Fatal(err)
	}
	if !called || strings.Contains(out.String(), "Channel names") || strings.Contains(out.String(), "dummy-token") {
		t.Fatal("unexpected prompt or secret disclosure")
	}
}

func TestSettingsSelectionAndClear(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	app := application{live: func(context.Context) ([]kick.LiveChannel, error) {
		return []kick.LiveChannel{{Slug: "first"}, {Slug: "second"}}, nil
	}}
	file, err := os.CreateTemp(root, "input")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	file.WriteString("1\n2,1,2\n")
	file.Seek(0, 0)
	var out bytes.Buffer
	if err := app.run([]string{"settings"}, file, &out); err != nil {
		t.Fatal(err)
	}
	path, _ := config.Path()
	cfg, err := config.Load(path)
	if err != nil || strings.Join(cfg.PriorityChannels, ",") != "second,first" {
		t.Fatal("selection order or deduplication failed")
	}
	if err := app.run([]string{"settings", "--clear-priority"}, file, &out); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(path)
	if err != nil || len(cfg.PriorityChannels) != 0 {
		t.Fatal("priorities not cleared")
	}
}

func TestWatchingSettingsAndLoginRetainPreferences(t *testing.T) {
	path := authConfig(t)
	app := application{verify: verifiedTestAccount}
	if _, err := runAuthTest(t, app, "", "settings", "--mode", "category", "--category", "just-chatting", "--priority", "favorite", "--daily-rewards=false"); err != nil {
		t.Fatal(err)
	}
	if _, err := runAuthTest(t, app, "dummy-good\n", "login"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Token != "dummy-good" || cfg.WatchMode != config.CategoryFirst || cfg.Category != "just-chatting" || strings.Join(cfg.PriorityChannels, ",") != "favorite" || !cfg.DisableDailyRewards {
		t.Fatal("login lost watching preferences")
	}
	if _, err := runAuthTest(t, app, "", "settings", "--mode", "invalid"); err == nil {
		t.Fatal("invalid mode accepted")
	}
	stored, _ := config.Load(path)
	if stored.WatchMode != config.CategoryFirst {
		t.Fatal("invalid settings overwrote saved preferences")
	}
	if _, err := runAuthTest(t, app, "", "settings", "--clear-priority"); err != nil {
		t.Fatal(err)
	}
	stored, _ = config.Load(path)
	if len(stored.PriorityChannels) != 0 || stored.Category != "just-chatting" || stored.WatchMode != config.CategoryFirst {
		t.Fatal("priority changes lost watching preferences")
	}
}
