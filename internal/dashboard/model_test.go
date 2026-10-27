package dashboard

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"kick-bot/internal/config"
	"kick-bot/internal/kick"
	"kick-bot/internal/ui"
)

func testModel(t *testing.T) *model {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	style := ui.New(&bytes.Buffer{}, "always")
	t.Cleanup(style.Close)
	m := newModel(ctx, Options{Path: filepath.Join(t.TempDir(), "config.json"), Config: config.Config{Token: "dummy-secret", PriorityChannels: []string{"first", "second"}}, Settings: true, Live: func(context.Context) ([]kick.LiveChannel, error) { return nil, nil }}, style.Renderer())
	return m
}

func TestStateReflectsActualConnection(t *testing.T) {
	m := testModel(t)
	m.running = true
	m.wantRunning = true
	for _, state := range []kick.State{kick.Discovering, kick.Connecting, kick.Waiting} {
		m.apply(kick.Event{Kind: "state", State: state, Channel: "example"})
		label, _ := m.badge()
		if strings.Contains(label, "WATCHING") || !m.connectedAt.IsZero() {
			t.Fatal("connection claimed before watching event")
		}
	}
	m.apply(kick.Event{Kind: "state", State: kick.Watching, Channel: "example"})
	label, _ := m.badge()
	if !strings.Contains(label, "WATCHING") || m.connectedAt.IsZero() {
		t.Fatal("watch event did not update dashboard")
	}
	m.apply(kick.Event{Kind: "sent", Message: "ping"})
	if m.sent != 1 || m.lastActivity.IsZero() {
		t.Fatal("send counters not updated")
	}
	m.apply(kick.Event{Kind: "state", State: kick.Waiting})
	if !m.connectedAt.IsZero() {
		t.Fatal("disconnected channel timer still running")
	}
	// Delayed events from an old worker must not claim a new connection.
	m.generation = 2
	m.Update(workerEvent{1, kick.Event{Kind: "state", State: kick.Watching, Channel: "old"}})
	if m.state == kick.Watching {
		t.Fatal("stale generation accepted")
	}
}

func TestSettingsPersistAndRestartWithoutOverlap(t *testing.T) {
	m := testModel(t)
	received := make(chan config.Config, 4)
	var active, peak, starts atomic.Int32
	m.opts.Worker = func(ctx context.Context, cfg config.Config, logger *log.Logger, observer func(kick.Event)) error {
		received <- cfg
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		starts.Add(1)
		observer(kick.Event{Kind: "state", State: kick.Discovering})
		<-ctx.Done()
		return ctx.Err()
	}
	m.start()
	cfg := m.opts.Config
	cfg.PriorityChannels = []string{"second", "first"}
	cfg.WatchMode, cfg.Category = config.CategoryFirst, "just-chatting"
	if !m.saveConfig(cfg) {
		t.Fatal("settings save failed")
	}
	deadline := time.After(3 * time.Second)
	for starts.Load() < 2 {
		select {
		case msg := <-m.events:
			m.Update(msg)
		case <-deadline:
			t.Fatal("worker did not restart")
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	for m.running {
		select {
		case msg := <-m.events:
			m.Update(msg)
		case <-deadline:
			t.Fatal("worker did not stop")
		}
	}
	m.workers.Wait()
	<-received
	latest := <-received
	if latest.WatchMode != config.CategoryFirst || latest.Category != "just-chatting" || strings.Join(latest.PriorityChannels, ",") != "second,first" {
		t.Fatal("worker restarted with stale watching preferences")
	}
	if peak.Load() != 1 || active.Load() != 0 {
		t.Fatal("workers overlapped or failed to stop")
	}
	cfg, err := config.Load(m.opts.Path)
	if err != nil || cfg.Token != "dummy-secret" || strings.Join(cfg.PriorityChannels, ",") != "second,first" {
		t.Fatal("settings persistence failed")
	}
}

func TestKeyboardEdits(t *testing.T) {
	m := testModel(t)
	m.setChannels([]kick.LiveChannel{{Slug: "new"}}, time.Now())
	m.tab = 1
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.opts.Config.PriorityChannels) != 3 {
		t.Fatal("live channel favorite toggle failed")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.opts.Config.PriorityChannels) != 2 {
		t.Fatal("favorite removal failed")
	}
	m.tab = 2
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlDown})
	if m.opts.Config.PriorityChannels[0] != "second" || m.cursor != 1 {
		t.Fatal("priority reorder failed")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m.input.SetValue("third,fourth")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.editing || len(m.opts.Config.PriorityChannels) != 4 {
		t.Fatal("add input did not save")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if len(m.opts.Config.PriorityChannels) != 3 {
		t.Fatal("delete failed")
	}
}

func TestFailureStaysStopped(t *testing.T) {
	m := testModel(t)
	m.running = true
	m.wantRunning = true
	m.generation = 1
	m.Update(workerDone{1, errors.New("HTTP 401: login required")})
	label, _ := m.badge()
	if m.wantRunning || !strings.Contains(label, "ERROR") {
		t.Fatal("failed worker automatically restarted or displayed as active")
	}
}

func TestAuthFailureExitsForLogin(t *testing.T) {
	m := testModel(t)
	m.generation = 1
	m.running, m.wantRunning = true, true
	_, cmd := m.Update(workerDone{1, kick.ErrAuth})
	if !errors.Is(m.authErr, kick.ErrAuth) || !m.quitting || m.wantRunning || cmd == nil {
		t.Fatal("authentication failure did not exit for login")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("dashboard did not release the terminal for login")
	}
}

func TestResponsiveDashboard(t *testing.T) {
	m := testModel(t)
	m.running = true
	m.wantRunning = true
	m.apply(kick.Event{Kind: "state", State: kick.Watching, Channel: "example"})
	m.addLog("WATCH", "Local test connection established.")
	for _, size := range [][2]int{{120, 36}, {90, 28}, {60, 24}, {50, 18}, {35, 12}} {
		m.width, m.height = size[0], size[1]
		for tab := 0; tab < 4; tab++ {
			m.tab = tab
			view := ansi.Strip(m.View())
			if m.width >= 50 && !strings.Contains(view, "Following") {
				t.Fatal("Following tab is hidden at a supported terminal width")
			}
			lines := strings.Split(view, "\n")
			if len(lines) != m.height {
				t.Fatalf("wrong height for %v: %d", size, len(lines))
			}
			for _, line := range lines {
				if ansi.StringWidth(line) >= m.width {
					t.Fatalf("overflow for %v: %q", size, line)
				}
			}
			if strings.Contains(view, "dummy-secret") {
				t.Fatal("dashboard leaked token")
			}
		}
	}
}

func TestFollowingTabRefreshAndPrioritySelection(t *testing.T) {
	m := testModel(t)
	m.opts.Following = func(context.Context) ([]kick.FollowedChannel, error) {
		return []kick.FollowedChannel{{Slug: "followed-live", IsLive: true, Viewers: 42}, {Slug: "followed-offline"}}, nil
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	if m.tab != 3 {
		t.Fatal("Following tab unavailable")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil || !m.followLoading {
		t.Fatal("following refresh did not start")
	}
	m.Update(cmd())
	if !m.followLoaded || m.followLoading || len(m.following) != 2 {
		t.Fatal("followed channels not loaded")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Following") || !strings.Contains(view, "followed-live") || !strings.Contains(view, "OFFLINE") {
		t.Fatal("following tab does not show online and offline channels")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cfg, err := config.Load(m.opts.Path)
	if err != nil || strings.Join(cfg.PriorityChannels, ",") != "first,second,followed-offline" {
		t.Fatal("following priority choice was not persisted")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.opts.Config.PriorityChannels) != 2 {
		t.Fatal("following priority toggle did not remove the channel")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.tab != 0 {
		t.Fatal("tab navigation did not wrap after Following")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.tab != 3 {
		t.Fatal("reverse tab navigation skipped Following")
	}
}

func TestFollowingFailurePreservesListAndKeepsBotRunning(t *testing.T) {
	m := testModel(t)
	m.tab = 3
	m.running, m.wantRunning = true, true
	m.Update(followingMsg{channels: []kick.FollowedChannel{{Slug: "cached-channel"}}})
	m.Update(followingMsg{err: kick.ErrAuth})
	if len(m.following) != 1 || m.quitting || !m.wantRunning || !m.running {
		t.Fatal("following failure stopped bot or cleared cached list")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Kick rejected this token") || !strings.Contains(view, "cached-channel") {
		t.Fatal("following failure was not shown alongside cached channels")
	}
}

func TestWatchingSettingsKeysPersistAndValidate(t *testing.T) {
	m := testModel(t)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if !m.editing || !m.editingCategory {
		t.Fatal("category mode must ask for a category")
	}
	m.input.SetValue("")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.editing || m.opts.Config.EffectiveWatchMode() != config.FollowingFirst {
		t.Fatal("blank category enabled category mode")
	}
	m.input.SetValue("just-chatting")
	for _, size := range [][2]int{{50, 18}, {100, 32}} {
		m.width, m.height = size[0], size[1]
		if !strings.Contains(ansi.Strip(m.View()), "just-chatting") {
			t.Fatal("category input hidden at a supported terminal size")
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cfg, err := config.Load(m.opts.Path)
	if err != nil || m.editing || cfg.WatchMode != config.CategoryFirst || cfg.Category != "just-chatting" || cfg.Token != "dummy-secret" {
		t.Fatal("category settings did not persist")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Category > Following > Discovery") {
		t.Fatal("category fallback missing from settings")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if m.opts.Config.WatchMode != config.PriorityFirst {
		t.Fatal("priority mode unavailable")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if m.opts.Config.WatchMode != config.FollowingFirst {
		t.Fatal("following mode unavailable")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m.input.SetValue("discarded")
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.opts.Config.Category != "just-chatting" || m.editing {
		t.Fatal("cancel changed category")
	}
}

func TestWatchingFollowingEventCopiesSnapshot(t *testing.T) {
	m := testModel(t)
	list := []kick.FollowedChannel{{Slug: "followed", IsLive: true}}
	m.apply(kick.Event{Kind: "following", Followed: list})
	list[0].Slug = "mutated"
	if !m.followLoaded || len(m.following) != 1 || m.following[0].Slug != "followed" {
		t.Fatal("following event not applied to owned memory")
	}
}

func TestDailyRewardsProgressAndSetting(t *testing.T) {
	m := testModel(t)
	m.apply(kick.Event{Kind: "reward", Reward: &kick.DailyRewardStatus{State: "in_progress", Progress: 42, Required: 60}})
	if !strings.Contains(m.dailyRewardLabel(), "42 / 60 min") {
		t.Fatal("server watch progress missing")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	cfg, err := config.Load(m.opts.Path)
	if err != nil || !cfg.DisableDailyRewards || m.dailyRewardLabel() != "Auto-claim off" {
		t.Fatal("daily rewards toggle was not saved")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m.apply(kick.Event{Kind: "reward", Reward: &kick.DailyRewardStatus{State: "claimed"}})
	if m.opts.Config.DisableDailyRewards || !strings.Contains(m.dailyRewardLabel(), "Collected") {
		t.Fatal("daily collection state missing")
	}
}

func TestProgramQuitStopsWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var out bytes.Buffer
	var active atomic.Int32
	err := Run(ctx, Options{Config: config.Config{Token: "dummy-secret"}, Input: strings.NewReader("q"), Output: &out, Color: "never",
		Worker: func(ctx context.Context, cfg config.Config, logger *log.Logger, observer func(kick.Event)) error {
			active.Add(1)
			defer active.Add(-1)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil || active.Load() != 0 {
		t.Fatalf("quit cleanup failed: %v", err)
	}
	if strings.Contains(out.String(), "dummy-secret") {
		t.Fatal("program leaked token")
	}
}

func TestLogoutRemovesCredentialsAndStopsWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Config{Token: "dummy-secret", XSRFToken: "dummy-xsrf", KickSession: "dummy-session", KickSessionID: "dummy-id", PriorityChannels: []string{"first", "second"}, ViewerMode: true}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	var active atomic.Int32
	var out bytes.Buffer
	err := Run(ctx, Options{Config: cfg, Path: path, Input: strings.NewReader("l"), Output: &out, Color: "never",
		Worker: func(ctx context.Context, cfg config.Config, logger *log.Logger, observer func(kick.Event)) error {
			active.Add(1)
			defer active.Add(-1)
			<-ctx.Done()
			return kick.ErrAuth // A late rejection must not prompt sign-in after logout.
		},
	})
	if !errors.Is(err, ErrLoggedOut) || active.Load() != 0 {
		t.Fatalf("logout did not finish cleanly: %v", err)
	}
	stored, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Token != "" || stored.XSRFToken != "" || stored.KickSession != "" || stored.KickSessionID != "" {
		t.Fatal("logout left account credentials")
	}
	if strings.Join(stored.PriorityChannels, ",") != "first,second" {
		t.Fatal("logout lost priorities")
	}
	for _, secret := range []string{"dummy-secret", "dummy-xsrf", "dummy-session", "dummy-id"} {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), secret) || strings.Contains(out.String(), secret) {
			t.Fatal("logout left or displayed credentials")
		}
	}
}

func TestLogoutSaveFailureKeepsSession(t *testing.T) {
	m := testModel(t)
	m.opts.Path = t.TempDir() // Atomic replacement cannot replace a directory.
	cancelled := false
	m.cancel = func() { cancelled = true }
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if cmd != nil || m.loggedOut || m.quitting || cancelled || m.opts.Config.Token == "" {
		t.Fatal("failed logout claimed success")
	}
	if len(m.logs) == 0 || !strings.Contains(m.logs[len(m.logs)-1].message, "Could not log out") {
		t.Fatal("logout failure was not shown")
	}
}

func TestDashboardSnapshot(t *testing.T) {
	path := os.Getenv("KICK_DASHBOARD_SNAPSHOT")
	if path == "" {
		t.Skip("optional visual snapshot")
	}
	m := testModel(t)
	m.width = 100
	m.height = 32
	m.tab = 0
	m.running = true
	m.wantRunning = true
	m.started = m.now.Add(-8 * time.Minute)
	m.opts.Config.PriorityChannels = []string{"example_channel", "second_channel", "third_channel"}
	m.setChannels(make([]kick.LiveChannel, 96), m.now)
	m.apply(kick.Event{Kind: "state", State: kick.Watching, Channel: "example_channel", At: m.now.Add(-3 * time.Minute)})
	m.sent = 27
	m.lastActivity = m.now.Add(-5 * time.Second)
	m.addLog("BOT", "Visual test · local connection events")
	m.addLog("DISCOVER", "Found 96 live channels.")
	m.addLog("CONNECT", "Connecting to example_channel...")
	m.addLog("WATCH", "WebSocket connection established.")
	m.addLog("SENT", "channel_handshake → example_channel")
	m.addLog("WATCH", "Stream connection is open; watching continues.")
	if err := os.WriteFile(path, []byte(m.View()), 0600); err != nil {
		t.Fatal(err)
	}
}
