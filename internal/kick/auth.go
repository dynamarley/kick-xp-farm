package kick

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"kick-bot/internal/config"
)

type Account struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// VerifyViewerConnection tests the same token request and WebSocket upgrade
// used by the watcher. It does not establish an authenticated account identity.
func (c *Client) VerifyViewerConnection(ctx context.Context) error {
	cleanup, err := c.initHTTP()
	if err != nil {
		return err
	}
	defer cleanup()
	conn, err := c.openViewer(ctx)
	if err != nil {
		return err
	}
	return conn.Close()
}

// WithSession supplies optional browser session cookies in addition to Bearer auth.
func (c *Client) WithSession(cfg config.Config) *Client {
	c.session = config.Config{XSRFToken: cfg.XSRFToken, KickSession: cfg.KickSession, KickSessionID: cfg.KickSessionID}
	return c
}

func (c *Client) applyAuth(header interface{ Set(string, string) }) error {
	if err := c.session.ValidateSession(); err != nil {
		return err
	}
	var cookies []string
	if c.token != "" {
		token, err := config.NormalizeToken(c.token)
		if err != nil {
			return err
		}
		header.Set("Authorization", "Bearer "+token)
		cookies = append(cookies, "sessiontoken="+url.QueryEscape(token))
	}
	if c.session.XSRFToken != "" {
		decoded, _ := url.PathUnescape(c.session.XSRFToken)
		header.Set("X-XSRF-TOKEN", decoded)
		cookies = append(cookies, "XSRF-TOKEN="+c.session.XSRFToken)
	}
	if c.session.KickSession != "" {
		cookies = append(cookies, "kick_session="+c.session.KickSession)
	}
	if c.session.KickSessionID != "" {
		cookies = append(cookies, "kick_session_id="+c.session.KickSessionID)
	}
	if len(cookies) > 0 {
		header.Set("Cookie", strings.Join(cookies, "; "))
	}
	return nil
}

// VerifyAccount requires an identity. An empty legacy response is inconclusive;
// a protected account endpoint distinguishes rejected credentials from a change
// to the identity response. Viewer tokens and browser session IDs are not proof
// of account authentication.
func (c *Client) VerifyAccount(ctx context.Context) (Account, error) {
	if c.token == "" {
		return Account{}, ErrAuth
	}
	cleanup, err := c.initHTTP()
	if err != nil {
		return Account{}, err
	}
	defer cleanup()
	var response struct {
		Account
		Data Account `json:"data"`
		User Account `json:"user"`
	}
	if err := c.getJSON(ctx, c.accountURL, &response, false); err != nil {
		return Account{}, fmt.Errorf("account identity request: %w", err)
	}
	for _, account := range []Account{response.Account, response.Data, response.User} {
		if account.ID > 0 && strings.TrimSpace(account.Username) != "" {
			return account, nil
		}
	}
	var status any
	if err := c.getJSON(ctx, c.accountCheckURL, &status, false); err != nil {
		return Account{}, fmt.Errorf("protected account check: %w", err)
	}
	return Account{}, ErrAccountIdentity
}
