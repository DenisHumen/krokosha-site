package githubsync

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadmeExcerpt(t *testing.T) {
	cases := []struct {
		name     string
		markdown string
		want     string
	}{
		{
			name: "skips the title, badges and takes the first prose paragraph",
			markdown: "# vlb\n\n" +
				"[![build](https://img.shields.io/badge/build-passing-green)](https://ci.example)\n\n" +
				"Lightweight software for balancing traffic across **multiple** providers with `failover`.\n\n" +
				"## Install\n\nRun the installer.\n",
			want: "Lightweight software for balancing traffic across multiple providers with failover.",
		},
		{
			name: "drops code blocks, HTML, comments and keeps link texts",
			markdown: "<p align=\"center\"><img src=\"logo.png\"></p>\n<!-- a hidden note that is long enough to be a paragraph on its own -->\n\n" +
				"```bash\nthis is a code block that must never become the description of a project\n```\n\n" +
				"A tool built on top of [Docker Compose](https://docs.docker.com/compose/) for local clusters.\n",
			want: "A tool built on top of Docker Compose for local clusters.",
		},
		{
			name:     "ignores lists, tables and quotes",
			markdown: "- first item of a list that is definitely long enough to pass\n- second item\n\n| column | another column with a long enough title |\n|---|---|\n\n> a quote that is long enough to be mistaken for a description\n\nThe real description of this project comes only here, after all the structure.\n",
			want:     "The real description of this project comes only here, after all the structure.",
		},
		{
			name:     "joins wrapped lines and windows line endings",
			markdown: "# T\r\n\r\nThis description is wrapped\r\nover several lines of the\r\nREADME file source.\r\n",
			want:     "This description is wrapped over several lines of the README file source.",
		},
		{
			name:     "too short to be a description",
			markdown: "# Logo\n\nJust a logo.\n",
			want:     "",
		},
		{
			name:     "empty",
			markdown: "",
			want:     "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReadmeExcerpt(tc.markdown); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestReadmeExcerptTruncatesOnAWordBoundary(t *testing.T) {
	long := strings.Repeat("Налаштування мережевого обладнання та серверів. ", 12)
	got := ReadmeExcerpt("# Заголовок\n\n" + long)

	if !strings.HasSuffix(got, "…") {
		t.Fatalf("no ellipsis: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > excerptMaxRunes+1 {
		t.Errorf("%d characters, want at most %d + ellipsis", n, excerptMaxRunes)
	}
	if !utf8.ValidString(got) {
		t.Error("cut in the middle of a character")
	}
	body := strings.TrimSuffix(got, "…")
	if !strings.HasPrefix(long, body) || strings.HasSuffix(body, " ") || strings.HasSuffix(body, ".") {
		t.Errorf("not cut on a word boundary: %q", got)
	}
	// Cyrillic letters count as letters: the paragraph is not mistaken for "too short".
	if countLetters("Привіт") != 6 {
		t.Error("countLetters must count non-ASCII letters")
	}
}
