package leads

import (
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Choosing templates by context (quick answers): which of the pool fit the conversation best, and
// why. The client's words find templates by their keywords — the last message counts most; the
// moment of the conversation, the direction of the request and what was sent already move them up
// and down. No model and no outside service: the owner edits the keywords in the admin area and
// sees under every suggestion why it came up.

// Reason says why a template came up or went down.
type Reason struct {
	Kind string // word | moment | direction | sent | short
	Text string // a word as the client wrote it; a moment (Moments)
}

// Suggestion is a template with its score.
type Suggestion struct {
	Template Template
	Score    float64
	Reasons  []Reason
	// Top: one of the three shown first — the best of different categories.
	Top bool
}

// Situation is what the card of a request knows about the conversation.
type Situation struct {
	Lang      string
	Direction string
	Moment    string // first | talk | waiting | done
	Last      string // the client's last message
	Earlier   string // the subject and the description, when the last message is something else
	Sent      map[int64]bool
}

// SilentAfter: the answer is ours, and the client has said nothing since — time to remind.
const SilentAfter = 48 * time.Hour

// SituationOf reads the situation off a card; sent are the templates used in it already.
func SituationOf(card *Card, sent map[int64]bool, now time.Time) Situation {
	lead := card.Lead
	situation := Situation{Lang: lead.Lang, Direction: lead.Direction, Sent: sent, Moment: MomentTalk}
	var lastIn, lastMessage *Entry
	for i := range card.Feed {
		entry := &card.Feed[i]
		if entry.Kind != "message" {
			continue
		}
		lastMessage = entry
		if entry.Direction == "in" {
			lastIn = entry
		}
	}
	switch {
	case lead.Status == StatusDone:
		situation.Moment = MomentDone
	case !card.FirstResponseAt.Valid:
		situation.Moment = MomentFirst
	case lead.Status == StatusWaitingClient && lastMessage != nil && lastMessage.Direction == "out" && now.Sub(lastMessage.At) >= SilentAfter:
		situation.Moment = MomentWaiting
	}
	opening := strings.TrimSpace(lead.Subject + "\n" + lead.Description)
	situation.Last = opening
	if lastIn != nil && strings.TrimSpace(lastIn.Body) != strings.TrimSpace(lead.Description) {
		situation.Last, situation.Earlier = lastIn.Body, opening
	}
	return situation
}

// Weights of the signals: a word of the last message is worth two moments of the conversation.
const (
	weightLast      = 3.0
	weightEarlier   = 1.5
	weightMoment    = 2.0
	penaltyMoment   = -3.0
	weightDirection = 1.5
	penaltyStranger = -2.0 // a template for other directions
	penaltySent     = -6.0 // sent already: only when nothing else fits
	weightShort     = 1.0
	maxUsageBonus   = 0.5
)

// Rank orders the reply templates of the client's language, best first; a tie keeps the order of
// the editor. The first three of different categories are marked Top.
func Rank(templates []Template, situation Situation) []Suggestion {
	last, earlier := normalize(situation.Last), normalize(situation.Earlier)
	short := len([]rune(strings.TrimSpace(situation.Last))) < 120
	var out []Suggestion
	for _, template := range templates {
		if template.Kind != "reply" || template.Lang != situation.Lang {
			continue
		}
		suggestion := Suggestion{Template: template}
		inLast, inEarlier := 0.0, 0.0
		for _, keyword := range template.Keywords {
			// A phrase («сколько стоит») says more than a single word («когда»).
			hit := 1.0
			if strings.Contains(strings.TrimSpace(keyword), " ") {
				hit = 1.5
			}
			if word, ok := find(last, keyword); ok {
				suggestion.Reasons = append(suggestion.Reasons, Reason{Kind: "word", Text: word})
				inLast += hit
			} else if word, ok := find(earlier, keyword); ok {
				suggestion.Reasons = append(suggestion.Reasons, Reason{Kind: "word", Text: word})
				inEarlier += hit
			}
		}
		suggestion.Score += weightLast*math.Min(inLast, 3) + weightEarlier*math.Min(inEarlier, 2)
		switch template.Moment {
		case MomentAny:
		case situation.Moment:
			suggestion.Score += weightMoment
			suggestion.Reasons = append(suggestion.Reasons, Reason{Kind: "moment", Text: situation.Moment})
		default:
			suggestion.Score += penaltyMoment
		}
		if len(template.Directions) > 0 {
			if known(template.Directions, situation.Direction) {
				suggestion.Score += weightDirection
				suggestion.Reasons = append(suggestion.Reasons, Reason{Kind: "direction", Text: situation.Direction})
			} else {
				suggestion.Score += penaltyStranger
			}
		}
		// A short first message says little: asking for the details is the likely answer.
		if template.Category == "details" && situation.Moment == MomentFirst && short {
			suggestion.Score += weightShort
			suggestion.Reasons = append(suggestion.Reasons, Reason{Kind: "short"})
		}
		if situation.Sent[template.ID] {
			suggestion.Score += penaltySent
			suggestion.Reasons = append(suggestion.Reasons, Reason{Kind: "sent"})
		}
		// Of two equal ones, the familiar one.
		suggestion.Score += math.Min(maxUsageBonus, 0.1*math.Log2(1+float64(template.UsedCount)))
		out = append(out, suggestion)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	// The first three: the best of each category, and only those that fit at all.
	categories := map[string]bool{}
	top := 0
	for i := range out {
		if top == 3 || out[i].Score <= 0 {
			break
		}
		category := out[i].Template.Category
		if category != "" && categories[category] {
			continue
		}
		categories[category] = true
		out[i].Top = true
		top++
	}
	// The three first, the rest after them in their order.
	sort.SliceStable(out, func(a, b int) bool { return out[a].Top && !out[b].Top })
	return out
}

// normalize makes a text comparable with keywords: lower case, «ё» as «е», one kind of apostrophe,
// words separated by single spaces, a space at both ends.
func normalize(text string) string {
	var out strings.Builder
	out.WriteByte(' ')
	space := true
	for _, r := range strings.ToLower(text) {
		switch r {
		case 'ё':
			r = 'е'
		case '’', 'ʼ', '`':
			r = '\''
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' {
			out.WriteRune(r)
			space = false
		} else if !space {
			out.WriteByte(' ')
			space = true
		}
	}
	if !space {
		out.WriteByte(' ')
	}
	return out.String()
}

// find looks for a keyword in a normalized text and returns the words it found, as they are
// there: a stem finds a word that starts with it («стоим» → «стоимость»), a phrase — words in a
// row. Stems of up to three letters («кп», «nda», «hi») must be whole words.
func find(text, keyword string) (string, bool) {
	keyword = strings.TrimSpace(normalize(keyword))
	if keyword == "" || text == "" {
		return "", false
	}
	needle := " " + keyword
	if len([]rune(keyword)) <= 3 {
		needle += " "
	}
	at := strings.Index(text, needle)
	if at < 0 {
		return "", false
	}
	start := at + 1
	end := start + len(keyword)
	if rest := strings.IndexByte(text[end:], ' '); rest >= 0 {
		end += rest
	}
	return text[start:end], true
}
