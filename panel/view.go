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

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#5A3FC0", Dark: "#B69CFF"})
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#1F6FB2", Dark: "#7CC4FF"})
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#777777", Dark: "#8A8A8A"})
	buttonStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1A1A1A"}).
			Background(lipgloss.AdaptiveColor{Light: "#3F6FB0", Dark: "#9CC9F5"})
	activeButtonStyle = buttonStyle.Background(lipgloss.AdaptiveColor{Light: "#2E8B57", Dark: "#7FD99B"})
	dangerButtonStyle = buttonStyle.Background(lipgloss.AdaptiveColor{Light: "#B03A3A", Dark: "#F28B82"})
	busyStyle         = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B8860B", Dark: "#FFD166"})
	idleStyle         = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#2E8B57", Dark: "#7FD99B"})
	errorStyle        = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B03A3A", Dark: "#F28B82"})
	addedStyle        = idleStyle
	removedStyle      = errorStyle
)

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

// button adds a clickable label.
func (r *row) button(label string, style lipgloss.Style, action func() tea.Cmd) *row {
	text := " " + label + " "
	r.zones = append(r.zones, zone{x0: r.width, x1: r.width + lipgloss.Width(text), action: action})
	r.add(text, style)
	return r.plain(" ")
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

// header starts a section with its title and any buttons, filled out to the
// panel's width with a rule.
func (s *screen) header(title string, buttons ...func(*row)) *row {
	r := s.row()
	r.add("━ "+title+" ", headerStyle)
	for _, b := range buttons {
		b(r)
	}
	if rest := s.width - r.width; rest > 0 {
		r.add(strings.Repeat("━", rest), dimStyle)
	}
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

// meter draws how much of a window is left.
func meter(remaining float64, cells int) string {
	filled := int(math.Round(remaining * float64(cells)))
	style := idleStyle
	switch {
	case remaining < 0.2:
		style = errorStyle
	case remaining < 0.5:
		style = busyStyle
	}
	return style.Render(strings.Repeat("█", filled)) + dimStyle.Render(strings.Repeat("░", cells-filled))
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
