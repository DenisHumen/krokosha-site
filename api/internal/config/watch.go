package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FormSettings hands out contacts.form of content/site.yaml and notices when the file changes:
// an update of the content (git pull and a rebuild of the site) needs no restart of the service.
type FormSettings struct {
	dir string
	log *slog.Logger

	mu       sync.Mutex
	checked  time.Time
	modified time.Time
	form     Form
}

// WatchForm reads the settings once; Current re-reads them when site.yaml is newer.
func WatchForm(contentDir string, log *slog.Logger) *FormSettings {
	settings := &FormSettings{dir: contentDir, log: log}
	settings.Current()
	return settings
}

// Current returns the settings. With an unreadable site.yaml it keeps what it had — a disabled
// form, if there never was anything: requests are not accepted with rules nobody can read.
func (s *FormSettings) Current() Form {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.checked) < 5*time.Second {
		return s.form
	}
	s.checked = time.Now()

	info, err := os.Stat(filepath.Join(s.dir, "site.yaml"))
	if err != nil {
		s.log.Warn("cannot read content/site.yaml: the contact form keeps its previous settings", "error", err)
		return s.form
	}
	if info.ModTime().Equal(s.modified) {
		return s.form
	}
	content, err := LoadContent(s.dir)
	if err != nil {
		s.log.Warn("content/site.yaml does not parse: the contact form keeps its previous settings", "error", err)
		return s.form
	}
	s.modified, s.form = info.ModTime(), content.Site.Contacts.Form
	return s.form
}
