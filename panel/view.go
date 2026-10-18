package panel

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

// Palette: neon on a dark background, the same as the tmux chrome.
const (
	accent = "#FF5FD7"
	gold   = "#FFD75F"
	mint   = "#5FFF87"
	cyan   = "#00D7FF"
	violet = "#AF87FF"
	hot    = "#FF5F5F"
	ink    = "#1A1033"
	muted  = "#8A86A8"
)

// rainbow is cycled across letters, shifted every animation frame.
var rainbow = []string{"#FF5F87", "#FF8700", "#FFD700", "#5FFF87", "#00D7FF", "#AF87FF"}

// spinner is a busy agent's animation.
var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// confetti decorates celebrations.
var confetti = []rune("✦✧★☆✸✺❋✹")

func fg(color string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color))
}

var (
	dimStyle    = fg(muted)
	errorStyle  = fg(hot).Bold(true)
	noticeStyle = fg(gold).Bold(true)
)

func button(color string) lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ink)).Background(lipgloss.Color(color))
}

// rainbowText colors each rune of text, shifted by frame so it flows.
func rainbowText(text string, frame int, bold bool) string {
	var b strings.Builder
	for i, r := range []rune(text) {
		style := fg(rainbow[(i+frame)%len(rainbow)])
		if bold {
			style = style.Bold(true)
		}
		b.WriteString(style.Render(string(r)))
	}
	return b.String()
}

// confettiText is a strip of colored confetti that moves with frame.
func confettiText(n, frame int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		r := confetti[(i*3+frame)%len(confetti)]
		b.WriteString(fg(rainbow[(i+frame)%len(rainbow)]).Render(string(r)))
	}
	return b.String()
}

// zone is a clickable span of one rendered row.
type zone struct {
	row, x0, x1 int
	action      func() tea.Cmd
}

// row builds one line of the panel and remembers where its buttons are.
type row struct {
	b     strings.Builder
	width int
	zones []zone
}

func (r *row) add(text string, style lipgloss.Style) *row {
	r.b.WriteString(style.Render(text))
	r.width += lipgloss.Width(text)
	return r
}

func (r *row) plain(text string) *row {
	return r.add(text, lipgloss.NewStyle())
}

// raw appends text that is already styled.
func (r *row) raw(styled string) *row {
	r.b.WriteString(styled)
	r.width += lipgloss.Width(styled)
	return r
}

// button adds a clickable label.
func (r *row) button(label string, style lipgloss.Style, action func() tea.Cmd) *row {
	text := " " + label + " "
	r.zones = append(r.zones, zone{x0: r.width, x1: r.width + lipgloss.Width(text), action: action})
	r.add(text, style)
	return r.plain(" ")
}

// clickable makes the whole row run action when clicked.
func (r *row) clickable(width int, action func() tea.Cmd) *row {
	r.zones = append(r.zones, zone{x0: 0, x1: width, action: action})
	return r
}

// screen collects rows and their clickable zones.
type screen struct {
	rows  []*row
	width int
}

func (s *screen) row() *row {
	r := &row{}
	s.rows = append(s.rows, r)
	return r
}

// render lays the rows out, cut to the panel's size, and returns the zones
// that are still visible.
func (s *screen) render(height int) (string, []zone) {
	rows := s.rows
	if len(rows) > height {
		rows = rows[:height]
	}
	lines := make([]string, len(rows))
	var zones []zone
	for i, r := range rows {
		lines[i] = xansi.Truncate(r.b.String(), s.width, "…")
		for _, z := range r.zones {
			if z.x0 < s.width {
				z.row = i
				z.x1 = min(z.x1, s.width)
				zones = append(zones, z)
			}
		}
	}
	return strings.Join(lines, "\n"), zones
}

// hpColor goes green, yellow, red as a window runs out.
func hpColor(remaining float64) string {
	switch {
	case remaining < 0.2:
		return hot
	case remaining < 0.5:
		return gold
	default:
		return mint
	}
}

// hpBar draws how much of a window is left, like a health bar.
func hpBar(remaining float64, cells int) string {
	filled := int(math.Round(remaining * float64(cells)))
	return fg(hpColor(remaining)).Render(strings.Repeat("▰", filled)) + fg("#3A3A5A").Render(strings.Repeat("▱", cells-filled))
}

// countdown is the time until t, such as "2h10m" or "3d4h".
func countdown(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := t.Sub(now)
	if d <= 0 {
		return "リセット済"
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm", int(math.Ceil(d.Minutes())))
	}
}

// age says how old a reading is when that matters, such as "(3時間前)".
func age(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case t.IsZero() || d < 30*time.Minute:
		return ""
	case d < 24*time.Hour:
		return fmt.Sprintf("(%d時間前)", int(math.Max(1, math.Round(d.Hours()))))
	default:
		return fmt.Sprintf("(%d日前)", int(d.Hours())/24)
	}
}

// padRight pads text with spaces to a display width.
func padRight(text string, width int) string {
	if w := lipgloss.Width(text); w < width {
		return text + strings.Repeat(" ", width-w)
	}
	return text
}
