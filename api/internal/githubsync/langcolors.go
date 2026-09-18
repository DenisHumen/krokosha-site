package githubsync

import (
	_ "embed"
	"strings"
	"sync"
)

//go:generate go run ./internal/genlangcolors

//go:embed langcolors.tsv
var langColorsTSV string

var langColors = sync.OnceValue(func() map[string]string {
	colors := map[string]string{}
	for _, line := range strings.Split(langColorsTSV, "\n") {
		if name, color, ok := strings.Cut(line, "\t"); ok && !strings.HasPrefix(line, "#") {
			colors[name] = strings.TrimSpace(color)
		}
	}
	return colors
})

// languageColor returns the GitHub colour of a language ("#rrggbb") or "" if it has none.
func languageColor(language string) string {
	return langColors()[language]
}
