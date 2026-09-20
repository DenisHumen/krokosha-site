package inbox

import (
	"regexp"
	"strings"
)

// An answer by mail drags the whole conversation behind it. The conversation is in the admin
// area already; what is wanted from the letter is the new part (brief B10.5).

var (
	reQuoted = regexp.MustCompile(`^\s*>`)
	// «On Mon, 1 Sep 2026 at 10:00, Denis <denis@example.com> wrote:» in the languages clients
	// of this site write in, and a few neighbours.
	reWrote = regexp.MustCompile(`(?i)(?:\bwrote|\bwrites|написала?|написал\(а\)|пишет|пише|написав|написала|\bschrieb|\ba écrit|\bescribió|\bha scritto|\bnapisał\(a\)|\bnapisał)\s*:\s*$`)
	// Programs that say nothing like «wrote»: «01.09.2026, 10:00, "Denis" <denis@example.com>:».
	reEndsLikeAttribution = regexp.MustCompile(`(?:<[^<>\s]+@[^<>\s]+>|\b\d{1,2}[:.]\d{2}\b)[^<>]{0,40}:\s*$`)
	reDateOrAddress       = regexp.MustCompile(`@|\b\d{1,2}[:.]\d{2}\b|\b(?:19|20)\d{2}\b`)
	reOriginal            = regexp.MustCompile(`(?i)^\s*-{2,}\s*(?:original message|forwarded message|исходное сообщение|пересылаемое сообщение|пересланное сообщение|оригінальне повідомлення|переслане повідомлення|ursprüngliche nachricht)\s*-{2,}\s*$`)
	reHeaderFrom          = regexp.MustCompile(`(?i)^\s*\*?(?:from|от|від|von|de)\*?\s*:\s*\S`)
	reHeaderSent          = regexp.MustCompile(`(?i)^\s*\*?(?:sent|date|отправлено|дата|надіслано|gesendet|envoyé)\*?\s*:\s*\S`)
	reRule                = regexp.MustCompile(`^\s*[_-]{10,}\s*$`)
)

// StripQuote returns what the sender wrote themselves. When in doubt it keeps more rather than
// less: a quote left in a conversation is a nuisance, a lost sentence of a client is a loss.
func StripQuote(text string) string {
	lines := strings.Split(text, "\n")
	cut := len(lines)
	for i := range lines {
		if startsQuote(lines, i) {
			cut = i
			break
		}
	}
	// The rule Outlook draws above the quoted header belongs to the quote.
	for cut > 0 && (strings.TrimSpace(lines[cut-1]) == "" || reRule.MatchString(lines[cut-1])) {
		cut--
	}
	if own := strings.TrimSpace(strings.Join(lines[:cut], "\n")); own != "" {
		return own
	}

	// Nothing above the quote: the sender wrote below it, or between its lines.
	var kept []string
	for i, line := range lines {
		if reQuoted.MatchString(line) || attribution(lines, i) > 0 || reOriginal.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	if own := tidy(strings.Join(kept, "\n")); own != "" {
		return own
	}
	return strings.TrimSpace(text)
}

// startsQuote: from this line on, the letter repeats what was said before.
func startsQuote(lines []string, i int) bool {
	line := lines[i]
	switch {
	case reOriginal.MatchString(line):
		return true
	case reHeaderFrom.MatchString(line) && within(lines, i+1, 4, reHeaderSent):
		return true // the header block Outlook puts above what it quotes
	case attribution(lines, i) > 0:
		return true
	case reQuoted.MatchString(line):
		// Quoted lines end the letter only when nothing of the sender's own follows them:
		// an answer written between the lines of a quote is kept whole.
		for _, rest := range lines[i:] {
			if strings.TrimSpace(rest) != "" && !reQuoted.MatchString(rest) {
				return false
			}
		}
		return true
	}
	return false
}

// attribution tells whether the line opens «On … wrote:», which mail programs wrap over up to
// three lines, and how many lines it takes. Said in so many words, it is believed; a line that
// merely ends like one — an address or a time, then a colon — needs the quote right after it.
func attribution(lines []string, i int) int {
	if strings.TrimSpace(lines[i]) == "" || reQuoted.MatchString(lines[i]) {
		return 0
	}
	joined := ""
	for n := 1; n <= 3 && i+n <= len(lines); n++ {
		part := strings.TrimSpace(lines[i+n-1])
		if part == "" || reQuoted.MatchString(part) {
			break
		}
		joined = strings.TrimSpace(joined + " " + part)
		if len(joined) > 400 {
			break
		}
		if n == 1 && !reDateOrAddress.MatchString(part) {
			break // «Вчера мой коллега написал:» stays in the letter: an attribution names an address, a date or a time — and begins with them
		}
		if reWrote.MatchString(joined) || (reEndsLikeAttribution.MatchString(joined) && quoteFollows(lines, i+n)) {
			return n
		}
	}
	return 0
}

func quoteFollows(lines []string, from int) bool {
	for _, line := range lines[from:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		return reQuoted.MatchString(line) || reOriginal.MatchString(line)
	}
	return false
}

func within(lines []string, from, count int, pattern *regexp.Regexp) bool {
	for i := from; i < len(lines) && i < from+count; i++ {
		if pattern.MatchString(lines[i]) {
			return true
		}
	}
	return false
}
