package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"time"

	"kick-bot/internal/config"
	"kick-bot/internal/kick"
	"kick-bot/internal/ui"
)

func readCredential(reader *bufio.Reader, view *ui.UI, label string) (string, error) {
	view.Prompt(label)
	defer view.Blank()
	return readLine(reader)
}

func (app application) authenticate(ctx context.Context, reader *bufio.Reader, view *ui.UI, path string, saved config.Config, force, withSession bool) (config.Config, kick.Account, error) {
	candidate := saved
	prompt := force || candidate.Token == ""
	for {
		entered := prompt
		if prompt {
			if app.viewerMode {
				view.Hint("Viewer mode: settings are saved after a successful WebSocket connection. Account sign-in is not verified.")
			} else {
				view.Hint("Credentials are saved locally only after your account is verified.")
			}
			raw, err := readCredential(reader, view, "Kick Bearer token:")
			if err != nil {
				return saved, kick.Account{}, err
			}
			token, err := config.NormalizeToken(raw)
			if err != nil {
				view.Line("WARNING", err.Error())
				continue
			}
			candidate = saved
			candidate.Token = token
			candidate.XSRFToken, candidate.KickSession, candidate.KickSessionID = "", "", ""
		}
		candidate.ViewerMode = app.viewerMode
		if app.viewerMode {
			view.Line("CONNECT", "Checking the viewer token and WebSocket connection...")
		} else {
			view.Line("ACCOUNT", "Connecting to your Kick account...")
		}
		account, err := app.verifyAccount(ctx, candidate)
		if entered && withSession && (!app.viewerMode || err != nil) && (err == nil || errors.Is(err, kick.ErrAuth) || errors.Is(err, kick.ErrAccess) || errors.Is(err, kick.ErrAccountIdentity)) {
			bearerAccount := account
			if err == nil {
				if app.viewerMode {
					view.Line("CONNECT", "Viewer connection succeeded. You can now add the session cookies from your working script.")
				} else {
					view.Line("ACCOUNT", "Bearer alone verified the account. You can now test extra session cookies.")
				}
			} else {
				view.Line("ACCOUNT", "Bearer alone did not verify the account. Trying optional session cookies.")
			}
			view.Hint("Paste cookie values only. Leave an optional value blank to skip it.")
			for _, field := range []struct {
				label  string
				target *string
			}{
				{"XSRF-TOKEN (optional):", &candidate.XSRFToken},
				{"kick_session (optional):", &candidate.KickSession},
				{"kick_session_id (optional):", &candidate.KickSessionID},
			} {
				value, readErr := readCredential(reader, view, field.label)
				if readErr != nil {
					return saved, kick.Account{}, readErr
				}
				*field.target = value
			}
			if validationErr := candidate.ValidateSession(); validationErr != nil {
				view.Line("WARNING", validationErr.Error())
				prompt = true
				continue
			}
			account, err = app.verifyAccount(ctx, candidate)
			if err == nil && bearerAccount.ID > 0 && account.ID != bearerAccount.ID {
				return saved, kick.Account{}, errors.New("session cookies belong to a different account; saved credentials were kept")
			}
		}
		if errors.Is(err, kick.ErrAuth) {
			view.Line("WARNING", err.Error())
			view.Line("ACCOUNT", "Account sign-in was rejected. Enter a current Kick account Bearer token again.")
			view.Hint("Use the Authorization header from a signed-in Kick account API request. A viewer token or x-client-token is not an account token.")
			if !app.viewerMode {
				view.Hint("To use the original WebSocket workflow: kick-level-farm run --viewer --with-session")
			}
			if !withSession {
				view.Hint("To test browser session cookies, use: kick-level-farm login --with-session")
			}
			prompt = true
			continue
		}
		if err != nil {
			return saved, kick.Account{}, fmt.Errorf("connection verification failed: %w; saved credentials were kept", err)
		}
		if entered || candidate.ViewerMode != saved.ViewerMode {
			if err := config.Save(path, candidate); err != nil {
				return saved, kick.Account{}, err
			}
			view.Line("SAVED", "Connection settings saved for your next launch.")
		}
		if app.viewerMode {
			view.Line("CONNECTED", "Viewer WebSocket connection verified. Account sign-in is unverified.")
		} else {
			view.Line("CONNECTED", "Signed in as "+ui.Sanitize(account.Username)+".")
		}
		return candidate, account, nil
	}
}

func (app application) verifyAccount(ctx context.Context, cfg config.Config) (kick.Account, error) {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	if app.viewerMode {
		return kick.Account{}, app.viewer(ctx, cfg)
	}
	return app.verify(ctx, cfg)
}
