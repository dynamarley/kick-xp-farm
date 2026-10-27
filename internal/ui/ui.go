package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"golang.org/x/term"

	"kick-bot/internal/kick"
)

const (
	green  = "#53FC18"
	cyan   = "#67E8F9"
	purple = "#B9A3FF"
	muted  = "#8893A7"
	white  = "#EDF2F7"
	amber  = "#FBBF24"
)

type UI struct {
	out     io.Writer
	r       *lipgloss.Renderer
	width   int
	restore func() error
}

func New(out io.Writer, mode string) *UI {
	u := &UI{out: out, width: 76, r: lipgloss.NewRenderer(out)}
	u.r.SetColorProfile(termenv.Ascii)
	terminal := false
	if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		terminal = true
		if width, _, err := term.GetSize(int(f.Fd())); err == nil {
			u.width = max(28, min(84, width-2))
		}
		if restore, err := termenv.EnableVirtualTerminalProcessing(termenv.NewOutput(out)); err == nil {
			u.restore = restore
		}
	}
	_, noColor := os.LookupEnv("NO_COLOR")
	if mode == "always" || mode == "auto" && terminal && !noColor && os.Getenv("TERM") != "dumb" {
		u.r.SetColorProfile(termenv.TrueColor)
	}
	return u
}

func (u *UI) Close() {
	if u.restore != nil {
		_ = u.restore()
	}
}

func (u *UI) Renderer() *lipgloss.Renderer { return u.r }

func Sanitize(value string) string { return clean(value) }

func (u *UI) text(value, color string) string {
	return u.r.NewStyle().Foreground(lipgloss.Color(color)).Render(value)
}

func (u *UI) Header() {
	title := u.r.NewStyle().Bold(true).Foreground(lipgloss.Color(green)).Render("KICK")
	fmt.Fprintln(u.out)
	fmt.Fprintln(u.out, "  "+title+u.text(" / LEVEL FARM", white)+u.text("   v0.6", muted))
	fmt.Fprintln(u.out, "  "+u.text("Discover live streams. Set your priorities. Stay connected.", muted))
	fmt.Fprintln(u.out, "  "+u.text(strings.Repeat("─", u.width-4), muted))
}

func (u *UI) Panel(title string, rows ...[2]string) {
	content := []string{u.text(title, cyan), ""}
	for _, row := range rows {
		content = append(content, u.text(row[0]+"  ", muted)+u.text(clean(row[1]), white))
	}
	box := u.r.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#354257")).Padding(1, 2).Width(u.width - 4)
	fmt.Fprintln(u.out, box.Render(strings.Join(content, "\n")))
}

func (u *UI) Line(label, message string) {
	color := cyan
	switch label {
	case "READY", "WATCH", "SAVED":
		color = green
	case "PRIORITY", "SETTINGS":
		color = purple
	case "WARNING", "WAIT":
		color = amber
	case "INFO":
		color = muted
	}
	fmt.Fprintln(u.out, "  "+u.text(fmt.Sprintf("%-8s", label), color)+" "+clean(message))
}

func (u *UI) Prompt(message string) {
	fmt.Fprint(u.out, "\n  "+u.text("› ", green)+clean(message)+" ")
}
func (u *UI) Hint(message string) { fmt.Fprintln(u.out, "  "+u.text(clean(message), muted)) }
func (u *UI) Blank()              { fmt.Fprintln(u.out) }

func (u *UI) Channels(channels []kick.LiveChannel, priorities []string) []kick.LiveChannel {
	visible := channels
	if len(visible) > 20 {
		visible = visible[:20]
	}
	ranks := make(map[string]int)
	for i, slug := range priorities {
		ranks[slug] = i + 1
	}
	u.Blank()
	u.Line("LIVE", fmt.Sprintf("%d live channels · showing the first %d", len(channels), len(visible)))
	u.Hint("  #   CHANNEL                    VIEWERS / LANGUAGE")
	for i, stream := range visible {
		badge := " "
		if ranks[stream.Slug] > 0 {
			badge = "★"
		}
		name := fmt.Sprintf("%-24s", ansi.Truncate(stream.Slug, 24, "…"))
		fmt.Fprintln(u.out, "  "+u.text(fmt.Sprintf("%2d", i+1), muted)+" "+u.text(badge, purple)+" "+u.text(name, white)+" "+u.text(fmt.Sprintf("%7d", stream.Viewers), green)+" "+u.text(clean(stream.Language), muted))
		u.Hint("       " + ansi.Truncate(clean(stream.Title), u.width-10, "…"))
	}
	return visible
}

// Write turns structured log entries into compact terminal events.
func (u *UI) Write(data []byte) (int, error) {
	message := strings.TrimSpace(string(data))
	label := "INFO"
	if strings.HasPrefix(message, "[") {
		if end := strings.IndexByte(message, ']'); end > 0 {
			label = message[1:end]
			message = strings.TrimSpace(message[end+1:])
		}
	}
	fmt.Fprint(u.out, "  "+u.text(time.Now().Format("15:04:05"), muted)+" ")
	u.Line(label, message)
	return len(data), nil
}

func clean(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(value))
}
