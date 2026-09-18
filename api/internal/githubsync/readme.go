package githubsync

import (
	"regexp"
	"strings"
	"unicode"
)

// Brief B3: when a repository has no description, the first paragraph of its README is used,
// without markdown, cut to ~160 characters.

const (
	excerptMaxRunes   = 160
	excerptMinLetters = 40 // shorter paragraphs are badges, slogans or headings, not a description
)

var (
	reCodeFence   = regexp.MustCompile("(?s)```.*?```")
	reHTMLComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reHTMLTag     = regexp.MustCompile(`<[^>]+>`)
	reImage       = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	reLink        = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reParagraph   = regexp.MustCompile(`\n\s*\n`)
	// Headings, tables, rules, quotes and list items are structure, not prose.
	reStructural = regexp.MustCompile(`^\s*(#|\||-{3,}|={3,}|>|\*\s|-\s|\d+\.\s)`)
	reEmphasis   = regexp.MustCompile("[*_`]+")
	reSpaces     = regexp.MustCompile(`\s+`)
)

// ReadmeExcerpt returns the first prose paragraph of a markdown document, or "" if there is none.
func ReadmeExcerpt(markdown string) string {
	text := strings.ReplaceAll(markdown, "\r\n", "\n")
	text = reCodeFence.ReplaceAllString(text, "")
	text = reHTMLComment.ReplaceAllString(text, "")
	text = reHTMLTag.ReplaceAllString(text, " ")
	text = reImage.ReplaceAllString(text, "")
	text = reLink.ReplaceAllString(text, "$1")

	for _, paragraph := range reParagraph.Split(text, -1) {
		var lines []string
		for _, line := range strings.Split(paragraph, "\n") {
			if !reStructural.MatchString(line) {
				lines = append(lines, line)
			}
		}
		candidate := reEmphasis.ReplaceAllString(strings.Join(lines, " "), "")
		candidate = strings.TrimSpace(reSpaces.ReplaceAllString(candidate, " "))
		if countLetters(candidate) < excerptMinLetters {
			continue
		}
		return truncate(candidate, excerptMaxRunes)
	}
	return ""
}

func countLetters(text string) int {
	count := 0
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			count++
		}
	}
	return count
}

// truncate cuts on a word boundary and adds an ellipsis.
func truncate(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	cut := string(runes[:maxRunes])
	if i := strings.LastIndex(cut, " "); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, ".,;:—- ") + "…"
}
