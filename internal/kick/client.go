package kick

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"kick-bot/internal/config"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/gorilla/websocket"
)

const (
	userAgent   = "Kick/1.0.0 (iPhone; iOS 16.5; Scale/3.00)"
	clientToken = "e1393935a959b4020a4491574f6490129f678acdaa92760471263db43487f823"
)

var ErrAuth = errors.New("Kick did not authenticate the account; enter a valid Bearer token")
var ErrAccess = errors.New("Kick denied API access (HTTP 403); this may be an access restriction or missing session cookies")
var ErrAccountIdentity = errors.New("Kick returned no account identity; account sign-in could not be confirmed")

type httpDoer interface {
	Do(*fhttp.Request) (*fhttp.Response, error)
}

type Client struct {
	token           string
	session         config.Config
	logger          *log.Logger
	http            httpDoer
	apiURL          string
	accountURL      string
	accountCheckURL string
	tokenURL        string
	socketURL       string
	discoveryURL    string
	followingURL    string
	dailyURL        string
	dailyInterval   time.Duration
	claimedRewards  map[string]time.Time
	priorities      []string
	pollInterval    time.Duration
	observer        func(Event)
	eventMu         sync.Mutex
	watchConfig     config.Config
	ranks           map[string]int
	followedSlugs   map[string]bool
	categorySlugs   map[string]bool
	currentInfo     channel
}

func New(token string, logger *log.Logger) *Client {
	return &Client{
		token: token, logger: logger,
		apiURL:          "https://kick.com/api/v2/channels/",
		accountURL:      "https://kick.com/api/v1/user",
		accountCheckURL: "https://kick.com/api/v2/user/verified-status",
		tokenURL:        "https://websockets.kick.com/viewer/v1/token",
		socketURL:       "wss://websockets.kick.com/viewer/v1/connect",
		discoveryURL:    "https://kick.com/stream/livestreams/en",
		followingURL:    "https://kick.com/api/v2/channels/followed-page",
		dailyURL:        "https://web.kick.com/api/v1/gamification/challenges",
	}
}

type channel struct {
	ID         int64 `json:"id"`
	Livestream *struct {
		ID         int64          `json:"id"`
		IsLive     *bool          `json:"is_live"`
		Categories []CategoryInfo `json:"categories"`
	} `json:"livestream"`
}

func (c *Client) initHTTP() (func(), error) {
	if c.http != nil {
		return func() {}, nil
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(),
		tlsclient.WithTimeoutSeconds(30),
		tlsclient.WithClientProfile(profiles.Safari_IOS_16_0),
		tlsclient.WithNotFollowRedirects(),
		tlsclient.WithCookieJar(tlsclient.NewCookieJar()),
	)
	if err != nil {
		return nil, errors.New("could not create the HTTP client")
	}
	c.http = client
	return func() { client.CloseIdleConnections(); c.http = nil }, nil
}

var errPriority = errors.New("a priority channel went live")

func (c *Client) Run(ctx context.Context, priorities []string) error {
	if err := c.watchConfig.ValidateWatching(); err != nil {
		return err
	}
	cleanup, err := c.initHTTP()
	if err != nil {
		return err
	}
	defer cleanup()
	if !c.watchConfig.DisableDailyRewards && c.token != "" {
		rewardCtx, stopRewards := context.WithCancel(ctx)
		rewardDone := make(chan struct{})
		go func() { defer close(rewardDone); c.runDailyRewards(rewardCtx) }()
		defer func() { stopRewards(); <-rewardDone }()
	} else {
		c.emit(Event{Kind: "reward", Reward: &DailyRewardStatus{State: "disabled"}})
	}
	c.priorities = priorities
	for ctx.Err() == nil {
		c.emit(Event{Kind: "state", State: Discovering})
		c.logger.Print("[DISCOVER] Fetching live channels from Kick...")
		channels, err := c.candidates(ctx, priorities)
		if errors.Is(err, ErrAuth) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			c.logger.Printf("[WARNING] Could not fetch the live list: %v", err)
		}
		if len(channels) > 0 {
			c.logger.Printf("[LIVE] Found %d candidates. Mode: %s.", len(channels), c.watchConfig.WatchModeLabel())
		}
		switchPriority := false
		for _, slug := range channels {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			info, live, err := c.checkChannel(ctx, slug)
			if errors.Is(err, ErrAuth) {
				return err
			}
			if err != nil {
				c.logger.Printf("[WARNING] %s: %v", slug, err)
			} else if !live {
				c.logger.Printf("[INFO] %s is currently offline.", slug)
			} else {
				c.emit(Event{Kind: "state", State: Connecting, Channel: slug})
				c.logger.Printf("[CONNECT] %s is live; connecting...", slug)
				if err := c.watch(ctx, slug, info); errors.Is(err, ErrAuth) {
					return err
				} else if errors.Is(err, errPriority) || err == nil {
					switchPriority = true
					break
				} else if err != nil && ctx.Err() == nil {
					c.logger.Printf("[WARNING] %s: %v", slug, err)
				}
			}
			if err := wait(ctx, 2*time.Second); err != nil {
				return err
			}
		}
		if switchPriority {
			continue
		}
		c.logger.Print("[WAIT] No available stream. Checking again in 30 seconds.")
		c.emit(Event{Kind: "state", State: Waiting})
		if err := wait(ctx, 30*time.Second); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (c *Client) getJSON(ctx context.Context, endpoint string, dst any, viewerToken bool) error {
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("could not create the API request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-App-Platform", "web")
	req.Header.Set("Cache-Control", "no-cache")
	if err := c.applyAuth(req.Header); err != nil {
		return err
	}
	if viewerToken {
		var random [6]byte
		if _, err := rand.Read(random[:]); err != nil {
			return errors.New("could not generate a request ID")
		}
		req.Header.Set("Origin", "https://kick.com")
		req.Header.Set("Referer", "https://kick.com/")
		req.Header.Set("X-Client-Token", clientToken)
		req.Header.Set("X-Request-Id", fmt.Sprintf("%d.%s", time.Now().UnixMilli(), hex.EncodeToString(random[:])))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("could not reach the API; check your network connection")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w (HTTP 401 Unauthorized)", ErrAuth)
	}
	if resp.StatusCode == http.StatusForbidden {
		return ErrAccess
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API returned HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(dst); err != nil {
		return errors.New("API response is not valid JSON")
	}
	return nil
}

func (c *Client) checkChannel(ctx context.Context, slug string) (channel, bool, error) {
	var info channel
	err := c.getJSON(ctx, c.apiURL+url.PathEscape(slug), &info, false)
	if err != nil {
		return info, false, err
	}
	if info.ID <= 0 {
		return info, false, errors.New("API response did not include a channel ID")
	}
	live := info.Livestream != nil && info.Livestream.ID > 0 && (info.Livestream.IsLive == nil || *info.Livestream.IsLive)
	return info, live, nil
}

func (c *Client) openViewer(ctx context.Context) (*websocket.Conn, error) {
	var result struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
		Token string `json:"token"`
	}
	if err := c.getJSON(ctx, c.tokenURL, &result, true); err != nil {
		return nil, err
	}
	token := result.Data.Token
	if token == "" {
		token = result.Token
	}
	if token == "" {
		return nil, errors.New("API did not return a viewer connection token")
	}
	endpoint, err := url.Parse(c.socketURL)
	if err != nil {
		return nil, errors.New("invalid WebSocket address")
	}
	query := endpoint.Query()
	query.Set("token", token)
	endpoint.RawQuery = query.Encode()
	headers := http.Header{}
	headers.Set("Origin", "https://kick.com")
	headers.Set("User-Agent", userAgent)
	if err := c.applyAuth(headers); err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 30 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, endpoint.String(), headers)
	if err != nil {
		if resp != nil {
			if resp.Body != nil {
				resp.Body.Close()
			}
			if resp.StatusCode == http.StatusUnauthorized {
				return nil, fmt.Errorf("%w (WebSocket HTTP 401 Unauthorized)", ErrAuth)
			}
			return nil, fmt.Errorf("WebSocket upgrade rejected (HTTP %d)", resp.StatusCode)
		}
		// Dial errors can contain the full URL, including its secret token.
		return nil, errors.New("could not establish the WebSocket connection")
	}
	return conn, nil
}

func (c *Client) watch(ctx context.Context, slug string, info channel) error {
	conn, err := c.openViewer(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	defer c.emit(Event{Kind: "state", State: Waiting})
	conn.SetReadLimit(2 << 20)
	// Closing on cancellation also interrupts a pending network write.
	stopClose := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopClose()
	send := func(message any) error {
		if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return errors.New("could not set the WebSocket write deadline")
		}
		if err := conn.WriteJSON(message); err != nil {
			return errors.New("could not send the WebSocket message")
		}
		kind := "message"
		if value, ok := message.(map[string]any); ok {
			kind, _ = value["type"].(string)
		}
		if value, ok := message.(map[string]string); ok {
			kind = value["type"]
		}
		c.emit(Event{Kind: "sent", Channel: slug, Message: kind})
		return nil
	}
	handshake := map[string]any{"type": "channel_handshake", "data": map[string]any{"message": map[string]string{"channelId": strconv.FormatInt(info.ID, 10)}}}
	tracking := map[string]any{"type": "user_event", "data": map[string]any{"message": map[string]any{"name": "tracking.user.watch.livestream", "channel_id": info.ID, "livestream_id": info.Livestream.ID}}}
	if err := send(handshake); err != nil {
		return err
	}
	if err := send(tracking); err != nil {
		return err
	}
	c.logger.Printf("[WATCH] Connected to %s. Watching the stream.", slug)
	c.emit(Event{Kind: "state", State: Watching, Channel: slug})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	defer func() { conn.Close(); <-done }()
	ping := time.NewTicker(30 * time.Second)
	handshakeTick := time.NewTicker(15 * time.Second)
	trackingTick := time.NewTicker(2 * time.Minute)
	defer trackingTick.Stop()
	pollInterval := c.pollInterval
	if pollInterval <= 0 {
		pollInterval = 30 * time.Second
	}
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	checks, monitorDone := c.monitor(monitorCtx, slug, pollInterval)
	defer func() { stopMonitor(); <-monitorDone }()
	defer ping.Stop()
	defer handshakeTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("WebSocket connection closed")
		case <-ping.C:
			if err := send(map[string]string{"type": "ping"}); err != nil {
				return err
			}
		case <-handshakeTick.C:
			if err := send(handshake); err != nil {
				return err
			}
		case <-trackingTick.C:
			if err := send(tracking); err != nil {
				return err
			}
		case check := <-checks:
			current, live, err := check.current, check.live, check.err
			if errors.Is(err, ErrAuth) {
				return err
			}
			if err != nil {
				c.logger.Printf("[WARNING] %s stream check: %v", slug, err)
				continue
			}
			if !live || current.Livestream.ID != info.Livestream.ID {
				c.logger.Printf("[INFO] %s ended or switched streams.", slug)
				return nil
			}
			if preferred, err := check.preferred, check.priorityErr; err != nil {
				if errors.Is(err, ErrAuth) {
					return err
				}
				c.logger.Printf("[WARNING] Priority check: %v", err)
			} else if preferred != "" {
				c.logger.Printf("[PRIORITY] %s went live; switching channels.", preferred)
				return errPriority
			}
			c.logger.Printf("[WATCH] %s is still live.", slug)
		}
	}
}

type streamCheck struct {
	current     channel
	live        bool
	err         error
	preferred   string
	priorityErr error
}

// API scans run separately so slow discovery cannot delay WebSocket heartbeats.
// The caller cancels and joins this monitor before the next selection scan.
func (c *Client) monitor(ctx context.Context, slug string, interval time.Duration) (<-chan streamCheck, <-chan struct{}) {
	checks := make(chan streamCheck)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, live, err := c.checkChannel(ctx, slug)
				check := streamCheck{current: current, live: live, err: err}
				if err == nil && live {
					c.currentInfo = current
					check.preferred, check.priorityErr = c.higherPriority(ctx, slug)
				}
				select {
				case checks <- check:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return checks, done
}

// LiveChannel contains only the public fields used in the terminal list.
type LiveChannel struct {
	Slug         string
	Title        string
	Language     string
	Viewers      int
	Category     string
	CategorySlug string
}

type CategoryInfo struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// LiveChannels reads a bounded, fresh set of directory pages. Pagination URLs
// from the remote response are never followed with the user's credentials.
func (c *Client) LiveChannels(ctx context.Context) ([]LiveChannel, error) {
	return c.liveChannels(ctx, "")
}

func (c *Client) liveChannels(ctx context.Context, category string) ([]LiveChannel, error) {
	cleanup, err := c.initHTTP()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var result []LiveChannel
	seen := make(map[string]bool)
	for page := 1; page <= 3; page++ {
		endpoint, err := url.Parse(c.discoveryURL)
		if err != nil {
			return nil, errors.New("invalid live directory address")
		}
		query := endpoint.Query()
		query.Set("limit", "50")
		query.Set("page", strconv.Itoa(page))
		query.Set("sort", "desc")
		if category != "" {
			query.Set("subcategory", category)
		}
		endpoint.RawQuery = query.Encode()
		var response struct {
			Data []struct {
				ID         int64          `json:"id"`
				IsLive     *bool          `json:"is_live"`
				Title      string         `json:"session_title"`
				Language   string         `json:"language"`
				Viewers    int            `json:"viewer_count"`
				Categories []CategoryInfo `json:"categories"`
				Channel    struct {
					ID     int64  `json:"id"`
					Slug   string `json:"slug"`
					Banned bool   `json:"is_banned"`
				} `json:"channel"`
			} `json:"data"`
			Next *string `json:"next_page_url"`
		}
		if err := c.getJSON(ctx, endpoint.String(), &response, false); err != nil {
			return nil, err
		}
		if response.Data == nil {
			return nil, errors.New("live directory response did not include a data array")
		}
		added := 0
		for _, stream := range response.Data {
			slugs, err := config.ParseChannels(stream.Channel.Slug)
			if err != nil || len(slugs) != 1 || stream.ID <= 0 || stream.Channel.ID <= 0 || stream.Channel.Banned || (stream.IsLive != nil && !*stream.IsLive) {
				continue
			}
			slug := slugs[0]
			if seen[slug] {
				continue
			}
			seen[slug] = true
			added++
			item := LiveChannel{Slug: slug, Title: stream.Title, Language: stream.Language, Viewers: stream.Viewers}
			if len(stream.Categories) > 0 {
				item.Category = stream.Categories[0].Name
				item.CategorySlug = stream.Categories[0].Slug
			}
			if category != "" && !categoryMatches(category, item.Category, item.CategorySlug) {
				continue
			}
			result = append(result, item)
		}
		if response.Next == nil || *response.Next == "" || added == 0 {
			break
		}
	}
	if category == "" {
		c.emit(Event{Kind: "directory", Channels: result})
	}
	return result, nil
}

func (c *Client) candidates(ctx context.Context, priorities []string) ([]string, error) {
	return c.selectCandidates(ctx, priorities)
}

func (c *Client) higherPriority(ctx context.Context, current string) (string, error) {
	var lastErr error
	for _, slug := range c.priorities {
		if slug == current {
			return "", lastErr
		}
		_, live, err := c.checkChannel(ctx, slug)
		if err != nil {
			if errors.Is(err, ErrAuth) || ctx.Err() != nil {
				return "", err
			}
			lastErr = err
			continue
		}
		if live {
			return slug, nil
		}
	}
	candidates, err := c.selectCandidates(ctx, c.priorities)
	if err != nil {
		lastErr = err
	}
	currentRank := c.channelRank(current)
	for _, slug := range candidates {
		if slug == current || c.ranks[slug] < len(c.priorities) || c.ranks[slug] >= currentRank {
			continue
		}
		_, live, err := c.checkChannel(ctx, slug)
		if err != nil {
			if errors.Is(err, ErrAuth) || ctx.Err() != nil {
				return "", err
			}
			lastErr = err
			continue
		}
		if live {
			return slug, nil
		}
	}
	return "", lastErr
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
