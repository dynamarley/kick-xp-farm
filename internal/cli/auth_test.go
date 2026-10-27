package cli

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"

	"kick-bot/internal/config"
	"kick-bot/internal/kick"
)

func verifiedTestAccount(context.Context, config.Config) (kick.Account, error) {
	return kick.Account{ID: 42, Username: "sample"}, nil
}

func TestViewerModeUsesConnectionCheckAndPersists(t *testing.T) {
	path := authConfig(t)
	checks, starts := 0, 0
	app := application{
		verify: func(context.Context, config.Config) (kick.Account, error) {
			t.Fatal("viewer mode called account sign-in")
			return kick.Account{}, kick.ErrAuth
		},
		viewer: func(ctx context.Context, cfg config.Config) error {
			checks++
			if cfg.Token != "dummy-good" {
				t.Fatal("supplied token lost")
			}
			if cfg.XSRFToken != "" || cfg.KickSession != "" || cfg.KickSessionID != "" {
				t.Fatal("viewer mode should only require the Bearer token when it succeeds")
			}
			return nil
		},
		watch: func(ctx context.Context, cfg config.Config, logger *log.Logger) error {
			starts++
			if !cfg.ViewerMode || cfg.Token != "dummy-good" {
				t.Fatal("viewer configuration not passed to watcher")
			}
			return nil
		},
	}
	out, err := runAuthTest(t, app, "dummy-good\n", "run", "--viewer", "--with-session", "--plain")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Signed in as") || !strings.Contains(out, "Account sign-in is unverified") {
		t.Fatal("viewer mode misrepresented account authentication")
	}
	for _, label := range []string{"XSRF-TOKEN (optional):", "kick_session (optional):", "kick_session_id (optional):"} {
		if strings.Contains(out, label) {
			t.Fatal("unnecessary cookie prompt after successful Bearer connection")
		}
	}
	cfg, err := config.Load(path)
	if err != nil || !cfg.ViewerMode || cfg.Token != "dummy-good" || cfg.XSRFToken != "" || cfg.KickSession != "" || cfg.KickSessionID != "" {
		t.Fatal("viewer mode not saved")
	}
	if _, err := runAuthTest(t, app, ""); err != nil {
		t.Fatal(err)
	}
	if starts != 2 || checks != 2 {
		t.Fatal("viewer startup did not reuse saved settings")
	}
}

func TestViewerFailureDoesNotSaveOrStart(t *testing.T) {
	path := authConfig(t)
	app := application{viewer: func(context.Context, config.Config) error { return errors.New("WebSocket upgrade rejected") }, watch: func(context.Context, config.Config, *log.Logger) error {
		t.Fatal("failed connection started watcher")
		return nil
	}}
	if _, err := runAuthTest(t, app, "dummy-good\n", "run", "--viewer"); err == nil {
		t.Fatal("expected failed connection")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unverified connection saved")
	}
}

func runAuthTest(t *testing.T, app application, input string, args ...string) (string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = app.run(args, f, &out)
	for _, secret := range []string{"dummy-old", "dummy-bad", "dummy-good", "dummy-xsrf", "dummy-session", "dummy-id"} {
		if strings.Contains(out.String(), secret) {
			t.Fatal("secret leaked")
		}
	}
	return out.String(), err
}

func authConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRejectedSavedTokenRetriesBeforeWatching(t *testing.T) {
	path := authConfig(t)
	if err := config.Save(path, config.Config{Token: "dummy-old", PriorityChannels: []string{"sample"}}); err != nil {
		t.Fatal(err)
	}
	var attempts []string
	app := application{
		verify: func(ctx context.Context, cfg config.Config) (kick.Account, error) {
			attempts = append(attempts, cfg.Token)
			stored, _ := config.Load(path)
			if stored.Token != "dummy-old" {
				t.Fatal("unverified token saved")
			}
			if cfg.Token != "dummy-good" {
				return kick.Account{}, kick.ErrAuth
			}
			return verifiedTestAccount(ctx, cfg)
		},
		watch: func(ctx context.Context, cfg config.Config, logger *log.Logger) error {
			if cfg.Token != "dummy-good" || len(attempts) != 3 {
				t.Fatal("worker started before verification")
			}
			return nil
		},
	}
	if _, err := runAuthTest(t, app, "dummy-bad\ndummy-good\n"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Token != "dummy-good" || len(cfg.PriorityChannels) != 1 {
		t.Fatal("verified login not persisted")
	}
}

func TestSessionLoginComparesBearerAndCookies(t *testing.T) {
	path := authConfig(t)
	attempts := 0
	app := application{verify: func(ctx context.Context, cfg config.Config) (kick.Account, error) {
		attempts++
		if attempts == 1 {
			if cfg.XSRFToken != "" || cfg.KickSession != "" {
				t.Fatal("first request must test Bearer alone")
			}
			return kick.Account{}, kick.ErrAuth
		}
		if cfg.XSRFToken != "dummy-xsrf" || cfg.KickSession != "dummy-session" || cfg.KickSessionID != "dummy-id" {
			t.Fatal("missing optional session values")
		}
		return verifiedTestAccount(ctx, cfg)
	}}
	if _, err := runAuthTest(t, app, "dummy-good\ndummy-xsrf\ndummy-session\ndummy-id\n", "login", "--with-session"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(path)
	if attempts != 2 || cfg.KickSession == "" {
		t.Fatal("session comparison or save failed")
	}
	if _, err := runAuthTest(t, app, "", "logout"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load(path)
	if cfg.Token != "" || cfg.XSRFToken != "" || cfg.KickSession != "" || cfg.KickSessionID != "" {
		t.Fatal("logout left credentials")
	}
}

func TestVerificationFailurePreservesConfigAndDoesNotStart(t *testing.T) {
	for _, failure := range []error{errors.New("network unavailable"), kick.ErrAccess, kick.ErrAuth, kick.ErrAccountIdentity} {
		t.Run(failure.Error(), func(t *testing.T) {
			path := authConfig(t)
			if err := config.Save(path, config.Config{Token: "dummy-old"}); err != nil {
				t.Fatal(err)
			}
			app := application{verify: func(context.Context, config.Config) (kick.Account, error) { return kick.Account{}, failure }, watch: func(context.Context, config.Config, *log.Logger) error {
				t.Fatal("worker started without account")
				return nil
			}}
			if _, err := runAuthTest(t, app, "dummy-bad\n", "login"); err == nil {
				t.Fatal("expected failure or EOF")
			}
			cfg, _ := config.Load(path)
			if cfg.Token != "dummy-old" {
				t.Fatal("saved credentials overwritten")
			}
		})
	}
}

func TestRuntimeAuthFailurePromptsAndRestarts(t *testing.T) {
	path := authConfig(t)
	if err := config.Save(path, config.Config{Token: "dummy-old"}); err != nil {
		t.Fatal(err)
	}
	starts := 0
	app := application{verify: verifiedTestAccount, watch: func(ctx context.Context, cfg config.Config, logger *log.Logger) error {
		starts++
		if starts == 1 {
			return kick.ErrAuth
		}
		if starts > 2 || cfg.Token != "dummy-good" {
			t.Fatal("incorrect restart")
		}
		return nil
	}}
	if _, err := runAuthTest(t, app, "dummy-good\n"); err != nil {
		t.Fatal(err)
	}
	if starts != 2 {
		t.Fatal("did not restart after login")
	}
}

func TestSessionComparisonWithWorkingBearer(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "same account", true: "different account"}[mismatch], func(t *testing.T) {
			path := authConfig(t)
			attempts := 0
			app := application{verify: func(ctx context.Context, cfg config.Config) (kick.Account, error) {
				attempts++
				account, _ := verifiedTestAccount(ctx, cfg)
				if attempts == 2 {
					if cfg.KickSession != "dummy-session" {
						t.Fatal("cookie comparison skipped after successful Bearer test")
					}
					if mismatch {
						account.ID++
					}
				}
				return account, nil
			}}
			_, err := runAuthTest(t, app, "dummy-good\ndummy-xsrf\ndummy-session\ndummy-id\n", "login", "--with-session")
			if (err != nil) != mismatch || attempts != 2 {
				t.Fatalf("unexpected comparison result: %v", err)
			}
			cfg, _ := config.Load(path)
			if mismatch && cfg.Token != "" {
				t.Fatal("different account cookies were saved")
			}
			if !mismatch && cfg.KickSession == "" {
				t.Fatal("valid extra cookies were not saved")
			}
		})
	}
}
