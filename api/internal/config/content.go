// Package config reads the settings the Go side needs: the editable content files
// (content/*.yaml) and, later, the service environment (/etc/krokosha/env).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Site is the part of content/site.yaml the Go side cares about.
// Texts and translations are resolved by the Astro build, not here.
type Site struct {
	Profile struct {
		Name         Localized `yaml:"name"`
		Nickname     string    `yaml:"nickname"`
		AvatarSource string    `yaml:"avatar_source"`
	} `yaml:"profile"`
	GitHub struct {
		User string `yaml:"user"`
	} `yaml:"github"`
	Timezone string `yaml:"timezone"`
	Contacts struct {
		Form Form `yaml:"form"`
	} `yaml:"contacts"`
	// Loyalty: the discounts of clients and the levels of regular ones.
	Loyalty Loyalty `yaml:"loyalty"`
}

// SenderName is the name letters are signed with when the installer was given none: the owner
// as the site presents them, «Denis (Krokosha)». A letter from a bare address looks like a robot's.
func (s Site) SenderName() string {
	name, nickname := strings.TrimSpace(s.Profile.Name.In("en")), strings.TrimSpace(s.Profile.Nickname)
	switch {
	case name != "" && nickname != "" && name != nickname:
		return name + " (" + nickname + ")"
	case name != "":
		return name
	default:
		return nickname
	}
}

// Projects mirrors content/projects.yaml (without private_projects, which only the site renders).
type Projects struct {
	// Pinned repositories are always featured and come first, in this order.
	Pinned []string `yaml:"pinned"`
	// Exclude hides repositories completely.
	Exclude []string `yaml:"exclude"`
	// Overrides are manual corrections on top of GitHub data.
	Overrides map[string]ProjectOverride `yaml:"overrides"`
	// ShowArchived keeps archived repositories in the list (always as "compact").
	ShowArchived bool `yaml:"show_archived"`
	// HomepageIgnoreHosts lists hosts that are not a project website (t.me, github.com…).
	HomepageIgnoreHosts []string `yaml:"homepage_ignore_hosts"`
}

// ProjectOverride is one entry of projects.yaml → overrides.
type ProjectOverride struct {
	Tier string `yaml:"tier"`
	// Description may be a string or an { en, uk, ru } dictionary; the text itself is applied
	// by the site build. Here it only matters that a description exists.
	Description yaml.Node `yaml:"description"`
}

// HasDescription reports whether a manual description is set.
func (o ProjectOverride) HasDescription() bool {
	return o.Description.Kind != 0 && o.Description.Tag != "!!null"
}

// Content is everything read from the content directory.
type Content struct {
	Dir      string
	Site     Site
	Projects Projects
}

// FindContentDir returns the closest `content` directory (with site.yaml) at or above start.
func FindContentDir(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, "content")
		if _, err := os.Stat(filepath.Join(candidate, "site.yaml")); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("content/site.yaml not found in %q or any parent directory", start)
		}
		dir = parent
	}
}

// LoadContent reads site.yaml and projects.yaml from the content directory.
func LoadContent(dir string) (*Content, error) {
	content := &Content{Dir: dir}
	if err := readYAML(filepath.Join(dir, "site.yaml"), &content.Site); err != nil {
		return nil, err
	}
	if err := readYAML(filepath.Join(dir, "projects.yaml"), &content.Projects); err != nil {
		return nil, err
	}
	if content.Site.GitHub.User == "" {
		return nil, errors.New("content/site.yaml: github.user is required")
	}
	if err := content.Site.Loyalty.Validate(); err != nil {
		return nil, fmt.Errorf("content/site.yaml: %w", err)
	}
	for name, override := range content.Projects.Overrides {
		switch override.Tier {
		case "", "featured", "standard", "compact":
		default:
			return nil, fmt.Errorf("content/projects.yaml: overrides.%s.tier must be featured, standard or compact, got %q", name, override.Tier)
		}
	}
	return content, nil
}

func readYAML(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}
