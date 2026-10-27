package ui

import (
	"bytes"
	"strings"
	"testing"

	"kick-bot/internal/kick"
)

func TestColorModesAndRemoteText(t *testing.T) {
	for _, mode := range []string{"always", "never", "auto"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			view := New(&out, mode)
			defer view.Close()
			view.Header()
			view.Channels([]kick.LiveChannel{{Slug: "example", Title: "Hello\x1b[2J\x1b]0;spoofed\x07\nworld", Language: "Turkish"}}, nil)
			text := out.String()
			if strings.Contains(text, "\x1b[2J") || strings.Contains(text, "spoofed") {
				t.Fatal("remote terminal controls leaked")
			}
			if strings.Contains(text, "\x1b[") != (mode == "always") {
				t.Fatal("incorrect color mode")
			}
		})
	}
}
