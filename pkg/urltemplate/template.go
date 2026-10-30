// Package urltemplate expands the URL templates used by the interactive
// batch downloader.
package urltemplate

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var tokenPattern = regexp.MustCompile(`\{\{\s*(page|index|vol|volume)(?::([0-9]+)d?)?\s*\}\}`)

// HasPage reports whether raw contains a page/index placeholder.
func HasPage(raw string) bool {
	return strings.Contains(raw, "[PAGE]") || tokenPattern.MatchString(raw) && hasToken(raw, "page", "index")
}

// HasVolume reports whether raw contains a volume placeholder.
func HasVolume(raw string) bool {
	return strings.Contains(raw, "[VOL]") || tokenPattern.MatchString(raw) && hasToken(raw, "vol", "volume")
}

// HasAB reports whether raw contains an A/B side placeholder.
func HasAB(raw string) bool {
	return strings.Contains(raw, "[AB]") || strings.Contains(raw, "[ab]") || strings.Contains(raw, "{{ab}}")
}

// ABPlaceholder returns the spelling of the A/B placeholder in raw.
func ABPlaceholder(raw string) (string, bool, bool) {
	switch {
	case strings.Contains(raw, "[AB]"):
		return "[AB]", false, true
	case strings.Contains(raw, "[ab]"):
		return "[ab]", true, true
	case strings.Contains(raw, "{{ab}}"):
		return "{{ab}}", true, true
	default:
		return "", false, false
	}
}

// Render expands page, index, volume and legacy [PAGE]/[VOL] placeholders.
// The A/B placeholder is intentionally preserved for the caller's A/B probe.
func Render(raw string, page, volume int, defaultPageWidth string) (string, error) {
	if !HasPage(raw) {
		return "", fmt.Errorf("模板必须包含 [PAGE]、{{page}} 或 {{index}} 占位符")
	}
	if _, err := parseWidth(defaultPageWidth); err != nil {
		return "", fmt.Errorf("页码格式无效: %w", err)
	}

	out := tokenPattern.ReplaceAllStringFunc(raw, func(token string) string {
		matches := tokenPattern.FindStringSubmatch(token)
		name := matches[1]
		width := defaultPageWidth
		if matches[2] != "" {
			width = matches[2]
		}
		value := page
		if name == "vol" || name == "volume" {
			value = volume
			if matches[2] == "" {
				width = "4"
			}
		}
		return formatNumber(value, width)
	})
	out = strings.ReplaceAll(out, "[PAGE]", formatNumber(page, defaultPageWidth))
	out = strings.ReplaceAll(out, "[VOL]", formatNumber(volume, "4"))
	return out, nil
}

// Extension returns the extension of a template URL without query or fragment.
func Extension(raw string) string {
	if pos := strings.IndexAny(raw, "?#"); pos >= 0 {
		raw = raw[:pos]
	}
	return filepath.Ext(raw)
}

func hasToken(raw string, names ...string) bool {
	for _, matches := range tokenPattern.FindAllStringSubmatch(raw, -1) {
		for _, name := range names {
			if matches[1] == name {
				return true
			}
		}
	}
	return false
}

func parseWidth(raw string) (int, error) {
	if raw == "" {
		return 0, fmt.Errorf("不能为空")
	}
	width, err := strconv.Atoi(raw)
	if err != nil || width < 1 || width > 12 {
		return 0, fmt.Errorf("必须是 1 到 12 之间的数字")
	}
	return width, nil
}

func formatNumber(value int, width string) string {
	n, err := parseWidth(width)
	if err != nil {
		n = 1
	}
	return fmt.Sprintf("%0*d", n, value)
}
