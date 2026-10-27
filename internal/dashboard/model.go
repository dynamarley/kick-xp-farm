package dashboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kick-bot/internal/config"
	"kick-bot/internal/kick"
	"kick-bot/internal/ui"
)

var ErrLoggedOut = errors.New("logged out")

type Options struct {
	Config    config.Config
	Account   kick.Account
	Path      string
	Input     io.Reader
	Output    io.Writer
	Color     string
	Settings  bool
	Worker    func(context.Context, config.Config, *log.Logger, func(kick.Event)) error
	Live      func(context.Context) ([]kick.LiveChannel, error)
	Following func(context.Context) ([]kick.FollowedChannel, error)
}

type workerEvent struct {
	generation int
	event      kick.Event
}
type workerDone struct {
	generation int
	err        error
}
type directoryMsg struct {
	channels []kick.LiveChannel
	err      error
}
type followingMsg struct {
	channels []kick.FollowedChannel
	err      error
}
type tickMsg time.Time
type logEntry struct {
	at             time.Time
	label, message string
}

type model struct {
	opts                                                          Options
	ctx                                                           context.Context
	events                                                        chan tea.Msg
	workers                                                       sync.WaitGroup
	r                                                             *lipgloss.Renderer
	width, height, tab, cursor, liveCursor, logOffset             int
	started, now, connectedAt, lastActivity, directoryAt          time.Time
	state                                                         kick.State
	channel                                                       string
	running, wantRunning, stopping, refreshing, editing, quitting bool
	generation, sent                                              int
	cancel                                                        context.CancelFunc
	input                                                         textinput.Model
	logs                                                          []logEntry
	directory                                                     []kick.LiveChannel
	failed                                                        bool
	authErr                                                       error
	loggedOut                                                     bool
	editingCategory                                               bool
	following                                                     []kick.FollowedChannel
	followCursor                                                  int
	followLoading, followLoaded                                   bool
	followAt                                                      time.Time
	followErr                                                     error
	dailyReward                                                   kick.DailyRewardStatus
}

func Run(ctx context.Context, opts Options) error {
	if opts.Input == nil {
		opts.Input = os.Stdin
	}
	if opts.Output == nil {
		opts.Output = os.Stdout
	}
	root, cancel := context.WithCancel(ctx)
	defer cancel()
	style := ui.New(opts.Output, opts.Color)
	defer style.Close()
	m := newModel(root, opts, style.Renderer())
	_, err := tea.NewProgram(m, tea.WithContext(root), tea.WithInput(opts.Input), tea.WithOutput(opts.Output), tea.WithAltScreen()).Run()
	cancel()
	if m.cancel != nil {
		m.cancel()
	}
	m.workers.Wait()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	if err == nil && m.loggedOut {
		return ErrLoggedOut
	}
	if err == nil && m.authErr != nil {
		return m.authErr
	}
	return err
}

func newModel(ctx context.Context, opts Options, r *lipgloss.Renderer) *model {
	now := time.Now()
	input := textinput.New()
	input.Prompt = "› "
	input.Placeholder = "channel1,channel2"
	input.CharLimit = 500
	input.Width = 60
	m := &model{opts: opts, ctx: ctx, events: make(chan tea.Msg, 256), r: r, width: 100, height: 32, started: now, now: now, input: input, wantRunning: !opts.Settings}
	if opts.Settings {
		m.tab = 2
	}
	return m
}

func (m *model) Init() tea.Cmd {
	if m.wantRunning {
		m.start()
	}
	cmds := []tea.Cmd{m.listen(), tick()}
	if m.opts.Following != nil {
		cmds = append(cmds, m.refreshFollowing())
	}
	if m.opts.Settings {
		cmds = append(cmds, m.refresh())
	}
	return tea.Batch(cmds...)
}

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

func (m *model) listen() tea.Cmd {
	return func() tea.Msg {
		select {
		case event := <-m.events:
			return event
		case <-m.ctx.Done():
			return nil
		}
	}
}

func (m *model) enqueue(ctx context.Context, msg tea.Msg) {
	select {
	case m.events <- msg:
	case <-ctx.Done():
	case <-m.ctx.Done():
	}
}

func (m *model) start() {
	if m.running || m.quitting {
		return
	}
	if m.opts.Config.Token == "" {
		m.wantRunning = false
		m.addLog("WARNING", "No saved token. Use the login command first.")
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	m.generation++
	generation := m.generation
	cfg := m.opts.Config
	cfg.PriorityChannels = append([]string(nil), cfg.PriorityChannels...)
	m.running = true
	m.stopping = false
	m.wantRunning = true
	m.failed = false
	m.state = kick.Discovering
	m.addLog("BOT", "Automatic discovery started.")
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		observer := func(event kick.Event) { m.enqueue(ctx, workerEvent{generation, event}) }
		logger := log.New(eventWriter{emit: observer}, "", 0)
		err := m.opts.Worker(ctx, cfg, logger, observer)
		m.enqueue(m.ctx, workerDone{generation, err})
	}()
}

type eventWriter struct{ emit func(kick.Event) }

func (w eventWriter) Write(data []byte) (int, error) {
	w.emit(kick.Event{Kind: "log", Message: string(data), At: time.Now()})
	return len(data), nil
}

func (m *model) refresh() tea.Cmd {
	if m.refreshing {
		return nil
	}
	m.refreshing = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 45*time.Second)
		defer cancel()
		channels, err := m.opts.Live(ctx)
		return directoryMsg{channels, err}
	}
}

func (m *model) refreshFollowing() tea.Cmd {
	if m.followLoading || m.opts.Following == nil {
		return nil
	}
	m.followLoading = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 45*time.Second)
		defer cancel()
		channels, err := m.opts.Following(ctx)
		return followingMsg{channels, err}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.quitting {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(20, msg.Width)
		m.height = max(8, msg.Height)
		m.input.Width = max(8, m.width-10)
	case tickMsg:
		m.now = time.Time(msg)
		return m, tick()
	case workerEvent:
		if msg.generation == m.generation {
			m.apply(msg.event)
		}
		return m, m.listen()
	case workerDone:
		if msg.generation == m.generation {
			restart := m.stopping && m.wantRunning
			m.running = false
			m.stopping = false
			m.channel = ""
			m.connectedAt = time.Time{}
			m.state = ""
			if errors.Is(msg.err, kick.ErrAuth) {
				m.authErr = kick.ErrAuth
				return m, m.quit()
			}
			if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
				restart = false
				m.failed = true
				m.addLog("ERROR", msg.err.Error())
			}
			m.wantRunning = restart
			if restart && !m.quitting {
				m.start()
			} else {
				m.addLog("BOT", "Bot stopped; connection closed.")
			}
		}
		return m, m.listen()
	case directoryMsg:
		m.refreshing = false
		if msg.err != nil {
			m.addLog("WARNING", "Could not refresh the live list: "+msg.err.Error())
		} else {
			m.setChannels(msg.channels, time.Now())
			m.addLog("DISCOVER", fmt.Sprintf("Found %d live channels.", len(msg.channels)))
		}
	case followingMsg:
		m.followLoading = false
		m.followErr = msg.err
		if msg.err != nil {
			m.addLog("WARNING", "Could not load followed channels: "+msg.err.Error())
		} else {
			m.following = append([]kick.FollowedChannel(nil), msg.channels...)
			m.followLoaded = true
			m.followAt = time.Now()
			m.followCursor = min(m.followCursor, max(0, len(m.following)-1))
			m.addLog("FOLLOWING", fmt.Sprintf("Loaded %d followed channels.", len(m.following)))
		}
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, m.quit()
		}
		if m.editing {
			switch msg.String() {
			case "esc":
				m.editing = false
				m.input.Blur()
				return m, nil
			case "enter":
				if m.editingCategory {
					cfg := m.opts.Config
					cfg.Category = strings.TrimSpace(m.input.Value())
					cfg.WatchMode = config.CategoryFirst
					if m.saveConfig(cfg) {
						m.editing = false
						m.input.Blur()
					}
					return m, nil
				}
				channels, err := config.ParseChannels(m.input.Value())
				if err != nil {
					m.addLog("WARNING", err.Error())
					return m, nil
				}
				merged, err := config.ParseChannels(strings.Join(append(append([]string(nil), m.opts.Config.PriorityChannels...), channels...), ","))
				if err == nil && m.save(merged) {
					m.editing = false
					m.input.Blur()
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		switch msg.String() {
		case "q":
			return m, m.quit()
		case "l":
			return m, m.logout()
		case "tab":
			m.tab = (m.tab + 1) % 4
		case "shift+tab":
			m.tab = (m.tab + 3) % 4
		case "1", "2", "3", "4":
			m.tab = int(msg.String()[0] - '1')
		case "b":
			m.wantRunning = !m.wantRunning
			if m.running {
				m.stopping = true
				m.cancel()
			} else if m.wantRunning {
				m.start()
			}
		case "r":
			if m.tab == 3 {
				return m, m.refreshFollowing()
			}
			return m, m.refresh()
		case "up", "k":
			if m.tab == 1 {
				m.liveCursor = max(0, m.liveCursor-1)
			} else if m.tab == 2 {
				m.cursor = max(0, m.cursor-1)
			} else if m.tab == 3 {
				m.followCursor = max(0, m.followCursor-1)
			}
		case "down", "j":
			if m.tab == 1 {
				m.liveCursor = min(max(0, len(m.channels())-1), m.liveCursor+1)
			} else if m.tab == 2 {
				m.cursor = min(max(0, len(m.opts.Config.PriorityChannels)-1), m.cursor+1)
			} else if m.tab == 3 {
				m.followCursor = min(max(0, len(m.following)-1), m.followCursor+1)
			}
		case "a":
			m.tab = 2
			m.editing = true
			m.editingCategory = false
			m.input.Placeholder = "channel1,channel2"
			m.input.SetValue("")
			return m, m.input.Focus()
		case "m":
			if m.tab == 2 {
				cfg := m.opts.Config
				switch cfg.EffectiveWatchMode() {
				case config.FollowingFirst:
					if strings.TrimSpace(cfg.Category) == "" {
						return m, m.editCategory()
					}
					cfg.WatchMode = config.CategoryFirst
				case config.CategoryFirst:
					cfg.WatchMode = config.PriorityFirst
				default:
					cfg.WatchMode = config.FollowingFirst
				}
				m.saveConfig(cfg)
			}
		case "c":
			if m.tab == 2 {
				return m, m.editCategory()
			}
		case "g":
			if m.tab == 2 {
				cfg := m.opts.Config
				cfg.DisableDailyRewards = !cfg.DisableDailyRewards
				if m.saveConfig(cfg) {
					m.dailyReward = kick.DailyRewardStatus{}
					if cfg.DisableDailyRewards {
						m.dailyReward.State = "disabled"
					}
				}
			}
		case "d", "delete":
			if m.tab == 2 && len(m.opts.Config.PriorityChannels) > 0 {
				list := append([]string(nil), m.opts.Config.PriorityChannels...)
				list = append(list[:m.cursor], list[m.cursor+1:]...)
				m.save(list)
			}
		case "ctrl+up", "ctrl+down":
			if m.tab == 2 && len(m.opts.Config.PriorityChannels) > 1 {
				target := m.cursor - 1
				if msg.String() == "ctrl+down" {
					target = m.cursor + 1
				}
				if target >= 0 && target < len(m.opts.Config.PriorityChannels) {
					list := append([]string(nil), m.opts.Config.PriorityChannels...)
					list[m.cursor], list[target] = list[target], list[m.cursor]
					if m.save(list) {
						m.cursor = target
					}
				}
			}
		case "enter":
			if m.tab == 1 && len(m.channels()) > 0 {
				m.toggle(m.channels()[m.liveCursor].Slug)
			}
			if m.tab == 3 && len(m.following) > 0 {
				m.toggle(m.following[m.followCursor].Slug)
			}
		case "pgup":
			m.logOffset = min(max(0, len(m.logs)-1), m.logOffset+5)
		case "pgdown":
			m.logOffset = max(0, m.logOffset-5)
		case "end":
			m.logOffset = 0
		}
	default:
		if m.editing {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *model) quit() tea.Cmd {
	m.quitting = true
	m.wantRunning = false
	if m.cancel != nil {
		m.cancel()
	}
	return tea.Quit
}

func (m *model) logout() tea.Cmd {
	cfg := m.opts.Config
	cfg.Token, cfg.XSRFToken, cfg.KickSession, cfg.KickSessionID = "", "", "", ""
	if err := config.Save(m.opts.Path, cfg); err != nil {
		m.addLog("ERROR", "Could not log out: "+err.Error())
		return nil
	}
	m.opts.Config = cfg
	m.opts.Account = kick.Account{}
	m.authErr = nil
	m.loggedOut = true
	return m.quit()
}

func (m *model) save(priorities []string) bool {
	cfg := m.opts.Config
	cfg.PriorityChannels = priorities
	return m.saveConfig(cfg)
}

func (m *model) editCategory() tea.Cmd {
	m.editing, m.editingCategory = true, true
	m.input.Placeholder = "just-chatting"
	m.input.SetValue(m.opts.Config.Category)
	return m.input.Focus()
}

func (m *model) saveConfig(cfg config.Config) bool {
	if err := config.Save(m.opts.Path, cfg); err != nil {
		m.addLog("ERROR", "Could not save settings: "+err.Error())
		return false
	}
	m.opts.Config = cfg
	m.cursor = min(m.cursor, max(0, len(cfg.PriorityChannels)-1))
	m.addLog("SETTINGS", "Saved watching preferences: "+cfg.WatchModeLabel()+".")
	if m.running && m.wantRunning {
		m.stopping = true
		m.cancel()
		m.addLog("BOT", "Applying updated watching preferences...")
	}
	return true
}

func (m *model) toggle(slug string) {
	list := append([]string(nil), m.opts.Config.PriorityChannels...)
	for i, value := range list {
		if value == slug {
			m.save(append(list[:i], list[i+1:]...))
			return
		}
	}
	m.save(append(list, slug))
}

func (m *model) addLog(label, message string) {
	if m.logOffset > 0 {
		m.logOffset++
	}
	m.logs = append(m.logs, logEntry{time.Now(), label, ui.Sanitize(strings.TrimSpace(message))})
	if len(m.logs) > 200 {
		m.logs = m.logs[len(m.logs)-200:]
	}
	m.logOffset = min(m.logOffset, max(0, len(m.logs)-1))
}

func (m *model) apply(event kick.Event) {
	if event.At.IsZero() {
		event.At = time.Now()
	}
	switch event.Kind {
	case "state":
		m.state = event.State
		if event.State == kick.Watching {
			m.channel = event.Channel
			m.connectedAt = event.At
			m.lastActivity = event.At
		} else {
			m.connectedAt = time.Time{}
			m.channel = event.Channel
		}
	case "directory":
		m.setChannels(event.Channels, event.At)
	case "following":
		m.following = append([]kick.FollowedChannel(nil), event.Followed...)
		m.followLoaded, m.followErr, m.followAt = true, nil, event.At
		m.followCursor = min(m.followCursor, max(0, len(m.following)-1))
	case "reward":
		if event.Reward != nil {
			m.dailyReward = *event.Reward
		}
	case "sent":
		m.sent++
		m.lastActivity = event.At
		if event.Message != "ping" {
			m.addLog("SENT", event.Message+" → "+event.Channel)
		}
	case "log":
		label, message := "INFO", strings.TrimSpace(event.Message)
		if strings.HasPrefix(message, "[") {
			if end := strings.IndexByte(message, ']'); end > 0 {
				label = message[1:end]
				message = strings.TrimSpace(message[end+1:])
			}
		}
		m.addLog(label, message)
	}
}

// Keep discovery snapshots in model-owned memory.
func (m *model) setChannels(channels []kick.LiveChannel, at time.Time) {
	m.directory = append([]kick.LiveChannel(nil), channels...)
	m.directoryAt = at
	m.liveCursor = min(m.liveCursor, max(0, len(channels)-1))
}
func (m *model) channels() []kick.LiveChannel { return m.directory }
