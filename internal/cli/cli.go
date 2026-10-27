package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"kick-bot/internal/config"
	"kick-bot/internal/dashboard"
	"kick-bot/internal/kick"
	"kick-bot/internal/ui"
)

const help = `
  USAGE
    kick-level-farm                         Discover live channels and start
    kick-level-farm dashboard               Open the full-screen dashboard
    kick-level-farm run --plain             Use line-based log output
    kick-level-farm run --viewer            Use the original WebSocket workflow
    kick-level-farm run --viewer --with-session
    kick-level-farm settings                Choose watching mode and priorities
    kick-level-farm settings --mode following|category|priority
    kick-level-farm settings --mode category --category just-chatting
    kick-level-farm settings --priority channel1,channel2
    kick-level-farm settings --clear-priority
    kick-level-farm settings --daily-rewards=true
    kick-level-farm channels                List live channels
    kick-level-farm login                   Save or replace your token
    kick-level-farm login --with-session    Try optional browser session cookies
    kick-level-farm status                  Show account and settings status
    kick-level-farm logout                  Remove the saved token

  APPEARANCE
    --color auto|always|never               Default: auto
    NO_COLOR                               Disable automatic color output

  On first launch, enter your Bearer token. Your input is visible.
  Live channels are discovered automatically. Priorities are optional.
  Priorities are checked from left to right. Exit: Ctrl+C.
`

type application struct {
	watch      func(context.Context, config.Config, *log.Logger) error
	verify     func(context.Context, config.Config) (kick.Account, error)
	live       func(context.Context) ([]kick.LiveChannel, error)
	viewer     func(context.Context, config.Config) error
	viewerMode bool
}

func Run(args []string, in *os.File, out io.Writer) error {
	app := application{
		watch: func(ctx context.Context, cfg config.Config, logger *log.Logger) error {
			return kick.New(cfg.Token, logger).WithSession(cfg).WithWatching(cfg).Run(ctx, cfg.PriorityChannels)
		},
		verify: func(ctx context.Context, cfg config.Config) (kick.Account, error) {
			return kick.New(cfg.Token, log.New(io.Discard, "", 0)).WithSession(cfg).VerifyAccount(ctx)
		},
		viewer: func(ctx context.Context, cfg config.Config) error {
			return kick.New(cfg.Token, log.New(io.Discard, "", 0)).WithSession(cfg).VerifyViewerConnection(ctx)
		},
		live: func(ctx context.Context) ([]kick.LiveChannel, error) {
			return kick.New("", log.New(io.Discard, "", 0)).LiveChannels(ctx)
		},
	}
	return app.run(args, in, out)
}

func (app application) run(args []string, in *os.File, out io.Writer) error {
	mode := "auto"
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--color" {
			if i+1 == len(args) {
				return errors.New("--color requires auto, always, or never")
			}
			i++
			mode = args[i]
		} else if strings.HasPrefix(args[i], "--color=") {
			mode = strings.TrimPrefix(args[i], "--color=")
		} else {
			filtered = append(filtered, args[i])
		}
	}
	if mode != "auto" && mode != "always" && mode != "never" {
		return errors.New("color mode must be auto, always, or never")
	}
	args = filtered
	view := ui.New(out, mode)
	defer view.Close()
	command := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	if command == "help" || len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		view.Header()
		fmt.Fprint(out, help)
		return nil
	}
	if command != "run" && command != "dashboard" && command != "login" && command != "status" && command != "logout" && command != "settings" && command != "channels" {
		return errors.New("unknown command; use --help for usage")
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(out)
	flags.Usage = func() { view.Header(); fmt.Fprint(out, help) }
	var priority string
	var clear bool
	var plain bool
	var withSession bool
	var viewerMode bool
	var watchMode, category string
	var dailyRewards bool
	if command == "login" || command == "run" || command == "dashboard" {
		flags.BoolVar(&withSession, "with-session", false, "Compare Bearer-only sign-in with optional session cookies")
		flags.BoolVar(&viewerMode, "viewer", false, "Use viewer WebSocket verification without requiring account sign-in (saved after success)")
	}
	if command == "run" || command == "dashboard" {
		flags.BoolVar(&plain, "plain", false, "Use line-based output instead of the dashboard")
	}
	if command == "settings" {
		flags.BoolVar(&dailyRewards, "daily-rewards", true, "Automatically collect daily watch rewards when Kick marks them ready")
		flags.StringVar(&watchMode, "mode", "", "Watching mode: following, category, or priority")
		flags.StringVar(&category, "category", "", "Kick category slug, for example just-chatting")
		flags.StringVar(&priority, "priority", "", "Comma-separated channel names in priority order")
		flags.BoolVar(&clear, "clear-priority", false, "Clear priorities and use automatic selection")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected argument; use --help for usage")
	}
	prioritySet := false
	watchModeSet, categorySet := false, false
	dailyRewardsSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "daily-rewards" {
			dailyRewardsSet = true
		}
		if f.Name == "mode" {
			watchModeSet = true
		}
		if f.Name == "category" {
			categorySet = true
		}
		if f.Name == "priority" {
			prioritySet = true
		}
	})
	if prioritySet && clear {
		return errors.New("--priority and --clear-priority cannot be used together")
	}
	var selected []string
	if prioritySet {
		var err error
		selected, err = config.ParseChannels(priority)
		if err != nil {
			return err
		}
	}
	terminalOutput, ok := out.(*os.File)
	interactive := ok && term.IsTerminal(int(terminalOutput.Fd())) && term.IsTerminal(int(in.Fd())) && !plain && (os.Getenv("TERM") != "dumb" || mode == "always" || command == "dashboard")
	if command == "dashboard" && !interactive {
		return errors.New("dashboard requires an interactive terminal; use run --plain for log output")
	}
	if !interactive || command != "run" && command != "dashboard" {
		view.Header()
	}
	ctx := context.Background()
	if command == "channels" {
		channels, err := app.fetchLive(ctx, view)
		if err != nil {
			return err
		}
		view.Channels(channels, nil)
		view.Hint("Choose priority channels: kick-level-farm settings")
		return nil
	}
	path, err := config.Path()
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	app.viewerMode = cfg.ViewerMode
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "viewer" {
			app.viewerMode = viewerMode
		}
	})
	reader := bufio.NewReader(in)
	switch command {
	case "settings":
		if prioritySet || clear || watchModeSet || categorySet || dailyRewardsSet {
			if dailyRewardsSet {
				cfg.DisableDailyRewards = !dailyRewards
			}
			if prioritySet || clear {
				cfg.PriorityChannels = selected
			}
			if watchModeSet {
				cfg.WatchMode = strings.ToLower(strings.TrimSpace(watchMode))
			}
			if categorySet {
				cfg.Category = strings.TrimSpace(category)
			}
			if err := config.Save(path, cfg); err != nil {
				return err
			}
			view.Line("SETTINGS", "Saved: "+cfg.WatchModeLabel()+"; priorities: "+priorityLabel(cfg.PriorityChannels))
			return nil
		}
		if !interactive {
			return app.settings(ctx, reader, view, path, cfg)
		}
	case "status":
		state := "not saved"
		if cfg.Token != "" {
			state = "saved (not verified with the API)"
		}
		sessionState := "not saved"
		if cfg.XSRFToken != "" || cfg.KickSession != "" || cfg.KickSessionID != "" {
			sessionState = "saved (hidden)"
		}
		connectionMode := "Account sign-in"
		if cfg.ViewerMode {
			connectionMode = "Viewer connection (account unverified)"
		}
		view.Panel("ACCOUNT AND SETTINGS", [2]string{"Token", state}, [2]string{"Session cookies", sessionState}, [2]string{"Mode", connectionMode}, [2]string{"Watching mode", cfg.WatchModeLabel()}, [2]string{"Category", cfg.Category}, [2]string{"Daily rewards", dailyRewardsLabel(cfg)}, [2]string{"Priority", priorityLabel(cfg.PriorityChannels)}, [2]string{"Config file", path})
		return nil
	case "logout":
		if cfg.Token == "" && cfg.XSRFToken == "" && cfg.KickSession == "" && cfg.KickSessionID == "" {
			view.Line("INFO", "No saved token.")
			return nil
		}
		cfg.Token = ""
		cfg.XSRFToken, cfg.KickSession, cfg.KickSessionID = "", "", ""
		if err := config.Save(path, cfg); err != nil {
			return err
		}
		view.Line("READY", "Token and session cookies removed. Channel priorities were kept.")
		return nil
	}
	if interactive && (command == "run" || command == "dashboard") {
		view.Header()
	}
	forceLogin := command == "login" || withSession
	for {
		var account kick.Account
		cfg, account, err = app.authenticate(ctx, reader, view, path, cfg, forceLogin, withSession)
		if err != nil {
			return err
		}
		if command == "login" {
			return nil
		}
		if interactive {
			err = app.runDashboard(ctx, in, out, mode, path, cfg, account, command == "settings")
		} else {
			err = app.runPlain(ctx, view, cfg, account)
		}
		if errors.Is(err, dashboard.ErrLoggedOut) {
			view.Line("READY", "Logged out. Saved credentials removed; channel priorities were kept.")
			return nil
		}
		if !errors.Is(err, kick.ErrAuth) {
			return err
		}
		view.Line("WARNING", "Your session was rejected. Sign in again to continue.")
		// Dashboard edits may have changed priorities while the worker was running.
		cfg, err = config.Load(path)
		if err != nil {
			return err
		}
		forceLogin = true
	}
}

func (app application) runPlain(ctx context.Context, view *ui.UI, cfg config.Config, account kick.Account) error {
	accountLabel := account.Username
	if cfg.ViewerMode {
		accountLabel = "Viewer connection · account unverified"
	}
	view.Panel("AUTOMATIC MODE", [2]string{"Account", accountLabel}, [2]string{"Watching mode", cfg.WatchModeLabel()}, [2]string{"Category", cfg.Category}, [2]string{"Daily rewards", dailyRewardsLabel(cfg)}, [2]string{"Priority", priorityLabel(cfg.PriorityChannels)})
	view.Hint("Edit priorities: kick-level-farm settings  ·  Exit: Ctrl+C")
	view.Blank()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	err := app.watch(ctx, cfg, log.New(view, "", 0))
	if ctx.Err() != nil {
		view.Line("INFO", "Stopped.")
		return nil
	}
	return err
}

func (app application) runDashboard(ctx context.Context, in *os.File, out io.Writer, mode, path string, cfg config.Config, account kick.Account, settings bool) error {
	return dashboard.Run(ctx, dashboard.Options{Config: cfg, Account: account, Path: path, Input: in, Output: out, Color: mode, Settings: settings, Live: app.live,
		Following: func(ctx context.Context) ([]kick.FollowedChannel, error) {
			return kick.New(cfg.Token, log.New(io.Discard, "", 0)).WithSession(cfg).FollowedChannels(ctx)
		},
		Worker: func(ctx context.Context, preferences config.Config, logger *log.Logger, observer func(kick.Event)) error {
			return kick.New(preferences.Token, logger).WithSession(preferences).WithWatching(preferences).WithEvents(observer).Run(ctx, preferences.PriorityChannels)
		},
	})
}

func (app application) fetchLive(ctx context.Context, view *ui.UI) ([]kick.LiveChannel, error) {
	view.Line("DISCOVER", "Fetching live channels from the Kick API...")
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	return app.live(ctx)
}

func (app application) settings(ctx context.Context, reader *bufio.Reader, view *ui.UI, path string, cfg config.Config) error {
	view.Panel("SETTINGS", [2]string{"Watching mode", cfg.WatchModeLabel()}, [2]string{"Category", cfg.Category}, [2]string{"Daily rewards", dailyRewardsLabel(cfg)}, [2]string{"Priority", priorityLabel(cfg.PriorityChannels)})
	view.Line("SETTINGS", "1  Choose priority channels from the live list")
	view.Line("SETTINGS", "2  Set priorities by channel name")
	view.Line("SETTINGS", "3  Clear priorities")
	view.Line("SETTINGS", "4  Following first")
	view.Line("SETTINGS", "5  Category first (fallback: following, then discovery)")
	view.Line("SETTINGS", "6  Priority channels first")
	view.Line("SETTINGS", "7  Toggle automatic daily rewards")
	view.Hint("Explicit priorities override all modes. Higher tiers are checked every 30 seconds.")
	view.Line("INFO", "0  Exit")
	view.Prompt("Your choice [0]:")
	choice, err := readLine(reader)
	if err != nil {
		return err
	}
	switch choice {
	case "", "0":
		return nil
	case "1":
		channels, err := app.fetchLive(ctx, view)
		if err != nil {
			return err
		}
		visible := view.Channels(channels, cfg.PriorityChannels)
		if len(visible) == 0 {
			view.Line("INFO", "No live channels are available right now.")
			return nil
		}
		view.Hint("Enter numbers in priority order (e.g. 3,1). Leave blank to keep your list.")
		view.Prompt("Channel numbers:")
		raw, err := readLine(reader)
		if err != nil {
			return err
		}
		if raw == "" {
			return nil
		}
		selected, err := selectChannels(raw, visible)
		if err != nil {
			return err
		}
		cfg.PriorityChannels = selected
	case "2":
		view.Hint("Offline channels can be added too. Example: channel1,channel2")
		view.Prompt("Priority channels (blank: keep current):")
		raw, err := readLine(reader)
		if err != nil {
			return err
		}
		if raw == "" {
			return nil
		}
		cfg.PriorityChannels, err = config.ParseChannels(raw)
		if err != nil {
			return err
		}
	case "3":
		cfg.PriorityChannels = nil
	case "4":
		cfg.WatchMode = config.FollowingFirst
	case "5":
		view.Prompt("Kick category slug (for example just-chatting):")
		cfg.Category, err = readLine(reader)
		if err != nil {
			return err
		}
		cfg.WatchMode = config.CategoryFirst
	case "6":
		cfg.WatchMode = config.PriorityFirst
	case "7":
		cfg.DisableDailyRewards = !cfg.DisableDailyRewards
	default:
		return errors.New("invalid choice; enter a number from 0 to 7")
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	view.Line("SETTINGS", "Saved: "+priorityLabel(cfg.PriorityChannels))
	view.Hint("These settings will be used on the next run.")
	return nil
}

func selectChannels(raw string, channels []kick.LiveChannel) ([]string, error) {
	var selected []string
	seen := make(map[int]bool)
	for _, part := range strings.Split(raw, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 1 || n > len(channels) {
			return nil, errors.New("enter channel numbers from the list, separated by commas")
		}
		if !seen[n] {
			selected = append(selected, channels[n-1].Slug)
			seen[n] = true
		}
	}
	return selected, nil
}

func priorityLabel(channels []string) string {
	if len(channels) == 0 {
		return "None · automatic selection"
	}
	return strings.Join(channels, " → ")
}

func dailyRewardsLabel(cfg config.Config) string {
	if cfg.DisableDailyRewards {
		return "Off"
	}
	return "Auto-claim on · checks every minute while running"
}

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return "", errors.New("could not read input; open an interactive terminal or use login first")
	}
	return strings.TrimSpace(line), nil
}
