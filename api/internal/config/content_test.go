package config

import "testing"

// Letters are signed with the owner as the site presents them, whatever the content file gives.
func TestSenderName(t *testing.T) {
	site := func(name Localized, nickname string) Site {
		var s Site
		s.Profile.Name, s.Profile.Nickname = name, nickname
		return s
	}
	for want, s := range map[string]Site{
		"Denis (Krokosha)": site(Localized{"en": "Denis", "uk": "Денис", "ru": "Денис"}, "Krokosha"),
		"Denis":            site(Localized{"": "Denis"}, ""),
		"Krokosha":         site(nil, " Krokosha "),
		"Olena":            site(Localized{"en": "Olena"}, "Olena"),
		"":                 site(nil, ""),
	} {
		if got := s.SenderName(); got != want {
			t.Errorf("SenderName() = %q, want %q", got, want)
		}
	}
}

// The real content file has what SenderName needs.
func TestSenderNameOfTheSite(t *testing.T) {
	dir, err := FindContentDir(".")
	if err != nil {
		t.Skip(err)
	}
	content, err := LoadContent(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := content.Site.SenderName(); got != "Denis (Krokosha)" {
		t.Errorf("content/site.yaml signs letters as %q", got)
	}
}

// The thresholds of the admin area's priority: the design's when the content names none; no level
// of nothing, and no key client who asks less than a regular one.
func TestPriorityRules(t *testing.T) {
	if got := (Loyalty{}).PriorityRules(); got != DefaultPriority {
		t.Errorf("no thresholds in the content: %+v", got)
	}
	own := Loyalty{Priority: LoyaltyPriority{Middle: PriorityLevel{Orders: 1}, High: PriorityLevel{Orders: 3, Spent: 5000}}}
	if err := own.Validate(); err != nil || own.PriorityRules() != own.Priority {
		t.Errorf("thresholds of the content: %+v %v", own.PriorityRules(), err)
	}
	for name, bad := range map[string]LoyaltyPriority{
		"high asks less than middle": {Middle: PriorityLevel{Orders: 3}, High: PriorityLevel{Orders: 2}},
		"a level of nothing":         {Middle: PriorityLevel{Orders: 1}},
		"a negative sum":             {Middle: PriorityLevel{Orders: 1, Spent: -5}, High: PriorityLevel{Orders: 4}},
	} {
		if err := (Loyalty{Priority: bad}).Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	dir, err := FindContentDir(".")
	if err != nil {
		t.Skip(err)
	}
	content, err := LoadContent(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := content.Site.Loyalty.PriorityRules(); got.Middle.Orders != 2 || got.High.Spent != 8000 {
		t.Errorf("content/site.yaml → loyalty.priority: %+v", got)
	}
}
