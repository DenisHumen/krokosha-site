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
