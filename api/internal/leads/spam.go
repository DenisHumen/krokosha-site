package leads

import (
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Spam is decided by points (brief B10.1). A suspicious request is not refused — the sender sees
// the usual «thank you» — it is stored with the status «spam»: no push to anybody, but visible
// in the admin area, where a mistake takes one click to correct.

// SpamThreshold is the number of points from which a request is filed as spam.
const SpamThreshold = 60

// minFillTime: nobody reads the form, types a name, a contact and twenty characters faster.
const minFillTime = 4 * time.Second

// Signals are the facts about a submission that do not come from its text.
type Signals struct {
	ProofOK    bool          // the proof of work was verified
	ProofError string        // why not, for the list of reasons
	FillTime   time.Duration // from asking for the challenge to sending; zero when unknown
}

// Verdict is the result of the checks.
type Verdict struct {
	Score   int
	Reasons []string
}

// IsSpam reports whether the request goes to the spam status.
func (v Verdict) IsSpam() bool { return v.Score >= SpamThreshold }

var (
	reLink = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+|\b[a-z0-9-]+\.(?:com|net|org|ru|xyz|info|biz|top|site|online|shop|click|link)\b/?\S*`)
	// What people selling something to the owner of a site write about. Deliberately short: the
	// words must be unlikely in a request for network or server work. Latin words match whole,
	// Cyrillic ones by their stem.
	reStopWords = regexp.MustCompile(`(?i)\b(?:seo|backlinks?|guest post|link building|rank(?:ing)? (?:your|on google)|first page of google|` +
		`casino|betting|crypto invest\w*|bitcoin|forex|payday loan|viagra|cialis|porn|escort|web traffic|buy followers|` +
		`lead generation|our agency|outsourcing company)\b|` +
		`продвижени[ея] сайт|раскрутк|поисковое продвижение|обратные ссылки|накрутк|казино|ставки на спорт|` +
		`заработок в интернете|кредит без|база клиентов|просування сайт|розкрутк`)
)

// Judge scores a validated submission.
func Judge(sub Submission, signals Signals) Verdict {
	var verdict Verdict
	add := func(points int, reason string) {
		verdict.Score += points
		verdict.Reasons = append(verdict.Reasons, reason)
	}

	if strings.TrimSpace(sub.Honeypot) != "" {
		add(100, "заполнено скрытое поле")
	}
	switch {
	case signals.ProofOK && signals.FillTime > 0 && signals.FillTime < minFillTime:
		add(50, "форма заполнена быстрее, чем это может человек")
	case !signals.ProofOK && signals.ProofError == "":
		// No JavaScript — no proof. Legitimate (the form must work without scripts), but then the
		// text alone has to convince.
		add(35, "без проверки proof-of-work (отправлено без JavaScript)")
	case !signals.ProofOK:
		add(45, "проверка proof-of-work не пройдена: "+signals.ProofError)
	}

	if links := len(reLink.FindAllString(sub.Description, -1)); links > 0 {
		add(min(links*25, 50), "ссылки в описании")
	}
	if reLink.MatchString(sub.Name) {
		add(40, "ссылка в имени")
	}
	if word := reStopWords.FindString(sub.Description); word != "" {
		add(30, "стоп-слово «"+strings.ToLower(word)+"»")
	}
	if shouting(sub.Description) {
		add(15, "текст набран заглавными")
	}
	if verdict.Score > 100 {
		verdict.Score = 100
	}
	return verdict
}

// shouting: most letters are capitals, in a text long enough to judge.
func shouting(text string) bool {
	letters, capitals := 0, 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				capitals++
			}
		}
	}
	return letters >= 40 && capitals*10 >= letters*7
}
