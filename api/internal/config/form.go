package config

import (
	"fmt"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// Localized is a text of content/*.yaml: one string for every language, or a dictionary
// { en, uk, ru } (docs/contract.md §6).
type Localized map[string]string

// UnmarshalYAML accepts both forms.
func (l *Localized) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*l = Localized{"": node.Value}
		return nil
	}
	texts := map[string]string{}
	if err := node.Decode(&texts); err != nil {
		return err
	}
	*l = texts
	return nil
}

// In returns the text in the given language, falling back to English and then to anything.
func (l Localized) In(lang string) string {
	for _, key := range []string{lang, "", "en"} {
		if text, ok := l[key]; ok {
			return text
		}
	}
	for _, text := range l {
		return text
	}
	return ""
}

// Option is a choice of the contact form that has an id.
type Option struct {
	ID    string    `yaml:"id"`
	Label Localized `yaml:"label"`
}

// Form is content/site.yaml → contacts.form, as far as the API needs it: what may be chosen and
// what to promise. Labels and error texts are the site's business.
type Form struct {
	Enabled     bool        `yaml:"enabled"`
	Attachments bool        `yaml:"attachments"`
	ReplyWithin yaml.Node   `yaml:"reply_within_hours"` // a number, or a TODO note while undecided
	Directions  []Option    `yaml:"directions"`
	Methods     []Option    `yaml:"contact_methods"`
	Budgets     []Localized `yaml:"budgets"`
	Timelines   []Localized `yaml:"timelines"`
}

// ReplyWithinHours returns the promised reaction time, or 0 when nothing is promised yet.
func (f Form) ReplyWithinHours() int {
	hours, err := strconv.Atoi(f.ReplyWithin.Value)
	if err != nil || hours <= 0 {
		return 0
	}
	return hours
}

// Direction returns the option with the given id.
func (f Form) Direction(id string) (Option, bool) {
	for _, option := range f.Directions {
		if option.ID == id {
			return option, true
		}
	}
	return Option{}, false
}

// Choice resolves the value a <select> of the form sends — the position of the option — to
// the option itself. An empty value means «not chosen».
func Choice(options []Localized, value string) (Localized, error) {
	if value == "" {
		return nil, nil
	}
	index, err := strconv.Atoi(value)
	if err != nil || index < 0 || index >= len(options) {
		return nil, fmt.Errorf("no option %q", value)
	}
	return options[index], nil
}
