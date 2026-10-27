package dashboard

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"kick-bot/internal/config"
	"kick-bot/internal/kick"
	"kick-bot/internal/ui"
)

const (
	green  = "#53FC18"
	cyan   = "#67E8F9"
	purple = "#B9A3FF"
	muted  = "#8893A7"
	white  = "#EDF2F7"
	amber  = "#FBBF24"
	red    = "#FB7185"
	border = "#354257"
)

func (m *model) text(value, color string) string {
	return m.r.NewStyle().Foreground(lipgloss.Color(color)).Render(ui.Sanitize(value))
}
func (m *model) bold(value, color string) string {
	return m.r.NewStyle().Bold(true).Foreground(lipgloss.Color(color)).Render(ui.Sanitize(value))
}
func (m *model) rule() string { return m.text(strings.Repeat("─", max(1, m.width-2)), border) }

func (m *model) badge() (string, string) {
	if m.stopping {
		return "◌ STOPPING", amber
	}
	if m.failed {
		return "● ERROR · BOT STOPPED", red
	}
	if !m.running {
		return "Ⅱ PAUSED", amber
	}
	dot := "●"
	if m.now.Unix()%2 == 0 {
		dot = "◉"
	}
	switch m.state {
	case kick.Watching:
		return dot + " WATCHING", green
	case kick.Connecting:
		return dot + " CONNECTING", cyan
	case kick.Discovering:
		return dot + " DISCOVERING", cyan
	default:
		return dot + " ACTIVE · WAITING", amber
	}
}

func (m *model) View() string {
	if m.quitting {
		return ""
	}
	if m.width < 50 || m.height < 18 {
		label, color := m.badge()
		lines := []string{m.bold("KICK.TV / FARM", green), m.text(label, color), "", m.text("Resize the terminal to use the dashboard.", muted), m.text("Minimum: 50 columns × 18 rows", muted), m.text("[l] log out · [b] bot · [q] quit", muted)}
		return m.fit(lines)
	}
	var lines []string
	brand := m.r.NewStyle().Bold(true).Foreground(lipgloss.Color(green)).Render(" KICK.TV ")
	var tabs []string
	for i, name := range []string{"Overview", "Live", "Settings", "Following"} {
		if m.width < 65 {
			name = []string{"Home", "Live", "Set", "Following"}[i]
		}
		label := fmt.Sprintf(" %d %s ", i+1, name)
		style := m.r.NewStyle().Foreground(lipgloss.Color(muted))
		if m.tab == i {
			style = style.Bold(true).Foreground(lipgloss.Color("#101418")).Background(lipgloss.Color(green))
		}
		tabs = append(tabs, style.Render(label))
	}
	left := brand + " " + strings.Join(tabs, " ")
	if m.width < 76 {
		lines = append(lines, brand)
		left = strings.Join(tabs, " ")
	}
	status, color := m.badge()
	right := m.bold(status, color)
	if lipgloss.Width(left)+lipgloss.Width(right)+2 <= m.width-2 {
		lines = append(lines, m.align(left, right))
	} else {
		lines = append(lines, left)
		lines = append(lines, m.align("", right))
	}
	accountLabel := " AUTO DISCOVERY  /  SINGLE ACCOUNT"
	if m.opts.Account.Username != "" {
		accountLabel = " ACCOUNT  /  " + m.opts.Account.Username
	}
	if m.opts.Config.ViewerMode {
		accountLabel = " VIEWER CONNECTION  /  ACCOUNT UNVERIFIED"
	}
	lines = append(lines, m.align(m.text(accountLabel, muted), m.text("Session "+duration(m.now.Sub(m.started))+" ", muted)), m.rule())
	// Reserve a fixed bottom area for the live log, independent of active tab.
	logRows := max(3, min(8, m.height/4))
	footerRows := 3
	bodyRows := max(3, m.height-len(lines)-logRows-footerRows-3)
	var body []string
	switch m.tab {
	case 0:
		body = m.overview(bodyRows)
	case 1:
		body = m.liveView(bodyRows)
	case 2:
		body = m.settingsView(bodyRows)
	case 3:
		body = m.followingView(bodyRows)
	}
	for len(body) < bodyRows {
		body = append(body, "")
	}
	if len(body) > bodyRows {
		body = body[:bodyRows]
	}
	lines = append(lines, body...)
	lines = append(lines, m.rule())
	logTitle := " BOT LOG"
	if m.logOffset > 0 {
		logTitle += " · history (End: latest)"
	}
	lines = append(lines, m.bold(logTitle, cyan))
	end := max(0, len(m.logs)-m.logOffset)
	start := max(0, end-logRows)
	for _, entry := range m.logs[start:end] {
		color := muted
		switch entry.label {
		case "WATCH", "BOT":
			color = green
		case "ERROR":
			color = red
		case "WARNING", "WAIT":
			color = amber
		case "SETTINGS", "PRIORITY":
			color = purple
		case "SENT", "CONNECT", "DISCOVER", "REWARD":
			color = cyan
		}
		line := m.text(" "+entry.at.Format("15:04:05")+" ", muted) + m.text(fmt.Sprintf("%-8s", entry.label), color) + " " + m.text(entry.message, white)
		lines = append(lines, line)
	}
	for i := end - start; i < logRows; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, m.rule())
	hint := " [tab] switch  [↑/↓] select  [r] refresh  [b] pause/resume  [q] quit"
	if m.tab == 1 {
		hint = " [enter] toggle favorite  [r] refresh  [tab] switch  [b] bot  [q] quit"
	}
	if m.tab == 3 {
		hint = " [enter] toggle priority  [r] refresh following  [tab] switch  [q] quit"
	}
	if m.tab == 2 {
		hint = " [m] mode  [c] category  [g] daily rewards  [a] add  [d] remove"
	}
	if m.editing {
		hint = " [enter] save and apply  [esc] cancel"
	}
	lines = append(lines, m.text(hint, muted), m.text(" [l] log out · PgUp/PgDn: log history · Settings saved immediately", muted))
	return m.fit(lines)
}

func (m *model) overview(rows int) []string {
	channel := "Looking for live channels..."
	if !m.running {
		channel = "Bot paused"
	}
	if m.channel != "" {
		channel = m.channel
	}
	if m.failed {
		channel = "Connection stopped · check the log"
	}
	connection := "Closed"
	connectionColor := muted
	if m.running {
		connection = "Not connected yet"
		connectionColor = cyan
	}
	if m.state == kick.Watching && m.running {
		connection = "WebSocket connected"
		connectionColor = green
	}
	if m.stopping {
		connection = "Closing"
		connectionColor = amber
	}
	watchTime := "—"
	if !m.connectedAt.IsZero() {
		watchTime = duration(m.now.Sub(m.connectedAt))
	}
	activity := "—"
	if !m.lastActivity.IsZero() {
		activity = fmt.Sprintf("%d s ago", max(0, int(m.now.Sub(m.lastActivity).Seconds())))
	}
	left := []string{m.bold(" NOW WATCHING", cyan), " " + m.bold(channel, white), "", m.text(" Connection     ", muted) + m.text(connection, connectionColor), m.text(" Channel time   ", muted) + m.text(watchTime, white), m.text(" Last sent      ", muted) + m.text(activity, white), m.text(" Messages sent  ", muted) + m.text(fmt.Sprintf("%d messages", m.sent), white), m.text(" Live directory ", muted) + m.text(fmt.Sprintf("%d channels · [2] list", len(m.channels())), white)}
	right := []string{m.bold(" PRIORITY CHANNELS", purple), m.text(" These channels are checked first when live", muted), ""}
	left = append(left, m.text(" Watching mode  ", muted)+m.text(m.opts.Config.WatchModeLabel(), purple))
	rewardColor := green
	if m.dailyReward.State == "unavailable" {
		rewardColor = amber
	}
	if m.opts.Config.DisableDailyRewards {
		rewardColor = muted
	}
	left = append(left, m.text(" Daily reward   ", muted)+m.text(m.dailyRewardLabel(), rewardColor))
	if m.opts.Config.EffectiveWatchMode() == config.CategoryFirst {
		left = append(left, m.text(" Category       ", muted)+m.text(m.opts.Config.Category, white))
	}
	if len(m.opts.Config.PriorityChannels) == 0 {
		right = append(right, m.text(" No priorities · automatic selection", muted))
	}
	for i, slug := range m.opts.Config.PriorityChannels {
		if len(right) >= rows-1 {
			break
		}
		right = append(right, m.text(fmt.Sprintf(" %d. ", i+1), muted)+m.text(slug, white))
	}
	right = append(right, "", m.text(" [3] Settings · edit priorities", muted))
	if m.width >= 86 {
		var result []string
		half := (m.width - 4) / 2
		for i := 0; i < max(len(left), len(right)); i++ {
			a, b := "", ""
			if i < len(left) {
				a = left[i]
			}
			if i < len(right) {
				b = right[i]
			}
			a = ansi.Truncate(a, half, "…")
			result = append(result, a+strings.Repeat(" ", max(1, half-lipgloss.Width(a)+2))+b)
		}
		return result
	}
	left = append(left, "", m.text(fmt.Sprintf(" Live directory: %d channels · [2] list", len(m.channels())), muted))
	return left
}

func (m *model) liveView(rows int) []string {
	updated := "Not fetched yet"
	if !m.directoryAt.IsZero() {
		updated = m.directoryAt.Format("15:04:05")
	}
	if m.refreshing {
		updated = "Refreshing..."
	}
	lines := []string{m.bold(fmt.Sprintf(" LIVE CHANNELS  ·  %d", len(m.channels())), cyan) + m.text("  /  "+updated, muted), m.text(" Enter: add to or remove from priorities", muted), ""}
	if len(m.channels()) == 0 {
		return append(lines, m.text(" Waiting for the live list. Press [r] to retry.", muted))
	}
	available := max(1, rows-len(lines))
	start := max(0, m.liveCursor-available+1)
	for i := start; i < min(len(m.channels()), start+available); i++ {
		stream := m.channels()[i]
		badge := " "
		for _, slug := range m.opts.Config.PriorityChannels {
			if slug == stream.Slug {
				badge = "★"
				break
			}
		}
		name := fmt.Sprintf("%-23s", ansi.Truncate(ui.Sanitize(stream.Slug), 23, "…"))
		row := fmt.Sprintf(" %s %2d. %s %7d  %s", badge, i+1, name, stream.Viewers, stream.Title)
		if i == m.liveCursor {
			lines = append(lines, m.selection(row))
		} else {
			lines = append(lines, m.text(row, white))
		}
	}
	return lines
}

func (m *model) settingsView(rows int) []string {
	category := m.opts.Config.Category
	if category == "" {
		category = "Not selected"
	}
	order := "Following > Discovery"
	if m.opts.Config.EffectiveWatchMode() == config.CategoryFirst {
		order = "Category > Following > Discovery"
	}
	lines := []string{m.bold(" WATCHING PREFERENCES", cyan), m.text(" [m] Mode: ", muted) + m.bold(m.opts.Config.WatchModeLabel(), green), m.text(" [c] Category: "+category, purple), m.text(" "+order+" · priorities override all modes", muted)}
	if m.editing {
		var form []string
		if m.editingCategory {
			form = []string{m.text(" Category slug (e.g. just-chatting)", purple), " " + m.input.View(), m.text(" Enter: enable category mode · Esc: cancel", muted)}
		} else {
			form = []string{m.text(" Channel names, separated by commas", purple), " " + m.input.View(), m.text(" Enter: save · Esc: cancel", muted)}
		}
		if rows >= len(lines)+len(form) {
			return append(lines, form...)
		}
		return form
	}
	rewards := "ON · collect when ready"
	if m.opts.Config.DisableDailyRewards {
		rewards = "OFF"
	}
	lines = append(lines, m.text(" [g] Daily rewards: "+rewards, green), m.text(" [l] Log out · [a/d] edit priorities · Ctrl+Up/Down: reorder", amber), m.bold(" PRIORITY CHANNELS", cyan))
	if len(m.opts.Config.PriorityChannels) == 0 {
		return append(lines, m.text(" No overrides. Select from [4] Following or [2] Live.", muted))
	}
	available := max(1, rows-len(lines))
	start := max(0, m.cursor-available+1)
	for i := start; i < min(len(m.opts.Config.PriorityChannels), start+available); i++ {
		slug := m.opts.Config.PriorityChannels[i]
		tag := ""
		if slug == m.channel && m.state == kick.Watching {
			tag = "  ← watching"
		}
		row := fmt.Sprintf(" %2d. %s%s", i+1, slug, tag)
		if i == m.cursor {
			lines = append(lines, m.selection(row))
		} else {
			lines = append(lines, m.text(row, white))
		}
	}
	return lines
}

func (m *model) followingView(rows int) []string {
	live := 0
	for _, channel := range m.following {
		if channel.IsLive {
			live++
		}
	}
	status := "Not loaded yet"
	if m.followLoaded {
		status = "Updated " + m.followAt.Format("15:04:05")
	}
	if m.followLoading {
		status = "Loading..."
	}
	lines := []string{m.bold(fmt.Sprintf(" FOLLOWING · %d channels · %d live", len(m.following), live), cyan), m.text(" Enter: toggle priority · [r] refresh · "+status, muted)}
	if m.followErr != nil {
		message := " Could not refresh followed channels. Press [r] to retry."
		if errors.Is(m.followErr, kick.ErrAuth) {
			message = " Kick rejected this token. Log out [l] and enter a current account Bearer token."
		}
		lines = append(lines, m.text(message, amber))
	}
	lines = append(lines, "")
	if len(m.following) == 0 {
		message := " Loading followed channels..."
		if m.followLoaded {
			message = " You are not following any channels."
		} else if m.followErr != nil {
			message = " An authenticated Kick account token is required."
		}
		return append(lines, m.text(message, muted))
	}
	available := max(1, rows-len(lines))
	start := max(0, m.followCursor-available+1)
	for i := start; i < min(len(m.following), start+available); i++ {
		channel := m.following[i]
		star := " "
		for _, slug := range m.opts.Config.PriorityChannels {
			if slug == channel.Slug {
				star = "★"
				break
			}
		}
		state := "OFFLINE"
		if channel.IsLive {
			state = "LIVE"
		}
		row := fmt.Sprintf(" %s %2d. %-23s %-7s", star, i+1, ansi.Truncate(ui.Sanitize(channel.Slug), 23, "…"), state)
		if channel.IsLive {
			row += fmt.Sprintf(" %7d", channel.Viewers)
		}
		if channel.Category != "" {
			row += "  " + channel.Category
		}
		if i == m.followCursor {
			lines = append(lines, m.selection(row))
		} else {
			color := muted
			if channel.IsLive {
				color = green
			}
			lines = append(lines, m.text(row, color))
		}
	}
	return lines
}

func (m *model) selection(value string) string {
	value = "›" + strings.TrimPrefix(value, " ")
	return m.r.NewStyle().Bold(true).Foreground(lipgloss.Color("#101418")).Background(lipgloss.Color(green)).Width(m.width - 2).Render(ansi.Truncate(ui.Sanitize(value), m.width-2, "…"))
}

func (m *model) align(left, right string) string {
	gap := m.width - 2 - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return ansi.Truncate(left, max(1, m.width-3-lipgloss.Width(right)), "…") + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *model) fit(lines []string) string {
	for len(lines) < m.height {
		lines = append(lines, "")
	}
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, max(1, m.width-1), "…")
	}
	return strings.Join(lines, "\n")
}

func duration(value time.Duration) string {
	seconds := max(0, int(value.Seconds()))
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
}
