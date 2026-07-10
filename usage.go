package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// --- usage limits header ---

// usageCache mirrors ~/.claude/.usage-cache.json, maintained by the user's
// statusline refresh script (usage-fetch.sh) — kraken only reads it.
type usageCache struct {
	FiveHour usageWindow  `json:"five_hour"`
	SevenDay usageWindow  `json:"seven_day"`
	Limits   []usageLimit `json:"limits"`
}

type usageWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

type usageLimit struct {
	Kind     string  `json:"kind"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resets_at"`
	Scope    struct {
		Model struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

type codexRateWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

type codexRateLimits struct {
	Primary   codexRateWindow `json:"primary"`
	Secondary codexRateWindow `json:"secondary"`
}

func usageColor(pct int) string {
	switch {
	case pct >= 80:
		return red
	case pct >= 50:
		return yellow
	default:
		return green
	}
}

// resetIn renders time until an ISO8601 reset as "3h12m" / "4d" / "8m".
func resetIn(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, iso); err != nil {
			return ""
		}
	}
	d := time.Until(t)
	switch {
	case d <= 0:
		return "now"
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm", int(d.Minutes())+1)
	}
}

func resetInUnix(epoch int64) string {
	if epoch <= 0 {
		return ""
	}
	return resetDuration(time.Until(time.Unix(epoch, 0)))
}

func resetDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "now"
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm", int(d.Minutes())+1)
	}
}

func readCodexRateLimits(path string) (codexRateLimits, bool) {
	var limits codexRateLimits
	f, err := os.Open(path)
	if err != nil {
		return limits, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return limits, false
	}
	const window = 256 * 1024
	start := info.Size() - window
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, 0); err != nil {
		return limits, false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return limits, false
	}
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, `"rate_limits"`) || !strings.Contains(line, `"token_count"`) {
			continue
		}
		var rec struct {
			Payload struct {
				Type       string          `json:"type"`
				RateLimits codexRateLimits `json:"rate_limits"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Payload.Type == "token_count" {
			limits, found = rec.Payload.RateLimits, true
		}
	}
	return limits, found
}

func codexUsageLine(color bool, seg func(string, float64, string) string) string {
	codexSessionCache.Lock()
	var newest codexSession
	for _, entry := range codexSessionCache.m {
		if entry.session.UpdatedAt > newest.UpdatedAt {
			newest = entry.session
		}
	}
	codexSessionCache.Unlock()
	if newest.Path == "" {
		return ""
	}
	limits, ok := readCodexRateLimits(newest.Path)
	if !ok {
		return ""
	}
	parts := []string{
		seg("5h", limits.Primary.UsedPercent, resetInUnix(limits.Primary.ResetsAt)),
		seg("7d", limits.Secondary.UsedPercent, resetInUnix(limits.Secondary.ResetsAt)),
	}
	lines := make([]string, len(parts))
	for i, part := range parts {
		prefix := "        "
		if i == 0 {
			prefix = paint(color, cyan, "codex") + "   "
		}
		lines[i] = prefix + part
	}
	return strings.Join(lines, "\n")
}

func usageHeader(color bool) string {
	path := filepath.Join(home(), ".claude", ".usage-cache.json")
	data, err := os.ReadFile(path)
	var uc usageCache
	claudeOK := err == nil && json.Unmarshal(data, &uc) == nil
	seg := func(label string, pct float64, reset string) string {
		p := int(pct)
		s := paint(color, dim, label+" ") + paint(color, usageColor(p), fmt.Sprintf("%d%%", p))
		if reset != "" {
			s += paint(color, dim, " · "+reset)
		}
		return s
	}
	var lines []string
	if claudeOK {
		segs := []string{
			seg("5h", uc.FiveHour.Utilization, resetIn(uc.FiveHour.ResetsAt)),
			seg("7d", uc.SevenDay.Utilization, resetIn(uc.SevenDay.ResetsAt)),
		}
		for _, l := range uc.Limits {
			if name := l.Scope.Model.DisplayName; name != "" {
				segs = append(segs, seg(strings.ToLower(name), l.Percent, resetIn(l.ResetsAt)))
			}
		}
		claudeLines := make([]string, len(segs))
		for i, part := range segs {
			prefix := "        "
			if i == 0 {
				prefix = paint(color, cyan, "claude") + "  "
			}
			claudeLines[i] = prefix + part
		}
		if info, err := os.Stat(path); err == nil {
			if stale := time.Since(info.ModTime()); stale > 10*time.Minute {
				claudeLines[0] += paint(color, dim, fmt.Sprintf("   (stale %s)", age(stale)))
			}
		}
		lines = append(lines, claudeLines...)
	}
	if line := codexUsageLine(color, seg); line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// usageForAgent extracts one agent's limit rows from the combined usage
// snapshot and removes the repeated agent label; the surrounding table title
// already supplies that context.
func usageForAgent(usage, agent string, color bool) []string {
	if usage == "" || (agent != "claude" && agent != "codex") {
		return nil
	}
	marker := paint(color, cyan, agent)
	var rows []string
	inBlock := false
	for _, line := range strings.Split(usage, "\n") {
		plain := ansiRe.ReplaceAllString(line, "")
		if strings.HasPrefix(plain, agent+strings.Repeat(" ", 8-len(agent))) {
			inBlock = true
			line = strings.TrimPrefix(line, marker)
			line = strings.TrimPrefix(line, agent)
			line = strings.TrimPrefix(line, strings.Repeat(" ", 8-len(agent)))
			rows = append(rows, "  "+line)
			continue
		}
		if len(plain) >= 8 && strings.TrimSpace(plain[:8]) == "" && inBlock {
			rows = append(rows, "  "+strings.TrimPrefix(line, "        "))
			continue
		}
		if inBlock {
			break
		}
	}
	return rows
}
