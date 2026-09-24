// Package mailboxes is the «Почта» screen of the admin area: the mailboxes of the site's own mail
// server — a new one for a colleague, a new password, a mailbox that is no longer needed.
//
// The web service may not touch the mail server's files, and must not be able to: it faces the
// internet, runs without privileges and may write to one directory only. So it writes a request
// there — the action, the address and the hash of the password, never the password itself — and a
// root helper started by systemd (krokosha-mailbox.path → krokosha-mailbox apply) checks the
// request again and applies it, the same way `sudo krokosha-mailbox` does by hand. The helper
// keeps the list of mailboxes and a log of what it did in a directory the service can only read.
package mailboxes

import (
	"bufio"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Actions of a request.
const (
	ActionAdd      = "add"
	ActionPassword = "passwd"
	ActionDelete   = "del"
)

// MinPassword is the length the helper insists on, like `krokosha-mailbox`.
const MinPassword = 12

// Errors.
var (
	ErrNoMail      = errors.New("there is no mail server here")
	ErrBadAddress  = errors.New("not an address of this domain")
	ErrShort       = errors.New("the password is too short")
	ErrExists      = errors.New("the mailbox exists already")
	ErrNoMailbox   = errors.New("no such mailbox")
	ErrService     = errors.New("the site's own mailbox is managed by the installer")
	ErrPending     = errors.New("a request about this mailbox is waiting already")
	reLocal        = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	reRequestName  = regexp.MustCompile(`^[0-9]{19}-[0-9a-f]{8}\.req$`)
	maxLogLines    = 50
	requestPattern = "*.req"
)

// Options say where the helper and the service meet.
type Options struct {
	Requests string // /var/lib/krokosha/requests/mail: the service writes, the helper reads and removes
	Status   string // /var/lib/krokosha/mail: the helper writes «mailboxes» and «log», the service reads
	Domain   string
	// Service is the site's own mailbox (leads@…): its password lives in the service's settings.
	Service string
	// Owner is the address letters are signed with: the screen marks it.
	Owner string
	Now   func() time.Time
}

// Service reads the mailboxes and writes requests.
type Service struct {
	opts Options
}

// New builds the service.
func New(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	opts.Domain = strings.ToLower(opts.Domain)
	opts.Service, opts.Owner = strings.ToLower(opts.Service), strings.ToLower(opts.Owner)
	return &Service{opts: opts}
}

// Domain is the domain every mailbox is at.
func (s *Service) Domain() string { return s.opts.Domain }

// Mailbox is one line of the list.
type Mailbox struct {
	Address string
	Service bool // the site's own
	Owner   bool // letters are written in its name
	Pending string
}

// Installed reports whether there is a mail server: the helper has written the list at least once.
func (s *Service) Installed() bool {
	_, err := os.Stat(filepath.Join(s.opts.Status, "mailboxes"))
	return err == nil
}

// List reads the mailboxes the helper listed, with the requests still waiting for it.
func (s *Service) List() ([]Mailbox, error) {
	file, err := os.Open(filepath.Join(s.opts.Status, "mailboxes"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoMail
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	pending, err := s.Pending()
	if err != nil {
		return nil, err
	}
	waiting := map[string]string{}
	for _, request := range pending {
		waiting[request.Address] = request.Action
	}
	var out []Mailbox
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		address := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if address == "" {
			continue
		}
		out = append(out, Mailbox{Address: address, Service: address == s.opts.Service, Owner: address == s.opts.Owner, Pending: waiting[address]})
		delete(waiting, address)
	}
	// A mailbox asked for and not made yet.
	for address, action := range waiting {
		if action == ActionAdd {
			out = append(out, Mailbox{Address: address, Pending: action})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Address < out[b].Address })
	return out, scanner.Err()
}

func (s *Service) exists(address string) (bool, error) {
	list, err := s.List()
	if err != nil {
		return false, err
	}
	for _, mailbox := range list {
		if mailbox.Address == address && mailbox.Pending != ActionAdd {
			return true, nil
		}
	}
	return false, nil
}

// Request is a request waiting for the helper.
type Request struct {
	ID      string
	Action  string
	Address string
	At      time.Time
}

// Pending lists the requests the helper has not taken yet, oldest first.
func (s *Service) Pending() ([]Request, error) {
	names, err := filepath.Glob(filepath.Join(s.opts.Requests, requestPattern))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []Request
	for _, name := range names {
		content, err := os.ReadFile(name)
		if err != nil {
			continue // the helper took it a moment ago
		}
		lines := strings.SplitN(string(content), "\n", 3)
		if len(lines) < 2 {
			continue
		}
		info, err := os.Stat(name)
		if err != nil {
			continue
		}
		out = append(out, Request{ID: strings.TrimSuffix(filepath.Base(name), ".req"), Action: lines[0], Address: lines[1], At: info.ModTime()})
	}
	return out, nil
}

// Address makes the full address of a mailbox from what was typed: «ivan» or «ivan@domain».
func (s *Service) Address(typed string) (string, error) {
	local := strings.ToLower(strings.TrimSpace(typed))
	if before, domain, found := strings.Cut(local, "@"); found {
		if domain != s.opts.Domain {
			return "", ErrBadAddress
		}
		local = before
	}
	if !reLocal.MatchString(local) || strings.Contains(local, "..") {
		return "", ErrBadAddress
	}
	return local + "@" + s.opts.Domain, nil
}

// Add asks for a new mailbox with the given password.
func (s *Service) Add(typed, password string) (string, error) {
	address, err := s.Address(typed)
	if err != nil {
		return "", err
	}
	exists, err := s.exists(address)
	if err != nil {
		return "", err
	}
	if exists {
		return "", ErrExists
	}
	return address, s.request(ActionAdd, address, password)
}

// SetPassword asks for a new password of a mailbox.
func (s *Service) SetPassword(address, password string) error {
	if address == s.opts.Service {
		return ErrService
	}
	exists, err := s.exists(address)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNoMailbox
	}
	return s.request(ActionPassword, address, password)
}

// Delete asks for a mailbox to be removed. Its letters stay on the disk until removed by hand.
func (s *Service) Delete(address string) error {
	if address == s.opts.Service {
		return ErrService
	}
	exists, err := s.exists(address)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNoMailbox
	}
	return s.request(ActionDelete, address, "")
}

// request writes a request for the helper: three lines, the last one the hash. The file appears
// whole or not at all (a temporary name, then a rename), and it is readable by its owner only.
func (s *Service) request(action, address, password string) error {
	if !s.Installed() {
		return ErrNoMail
	}
	pending, err := s.Pending()
	if err != nil {
		return err
	}
	for _, request := range pending {
		if request.Address == address {
			return ErrPending
		}
	}
	hash := ""
	if action != ActionDelete {
		if utf8.RuneCountInString(password) < MinPassword {
			return ErrShort
		}
		if strings.ContainsAny(password, "\r\n") {
			return ErrShort
		}
		if hash, err = Crypt(password); err != nil {
			return err
		}
		hash = "{SHA512-CRYPT}" + hash
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	name := fmt.Sprintf("%019d-%x", s.opts.Now().UnixNano(), suffix)
	temporary := filepath.Join(s.opts.Requests, "."+name+".tmp")
	if err := os.WriteFile(temporary, []byte(action+"\n"+address+"\n"+hash+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, filepath.Join(s.opts.Requests, name+".req")); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

// Outcome is a line of the helper's log.
type Outcome struct {
	At      time.Time
	ID      string
	Action  string
	Address string
	OK      bool
	Message string
}

// Log reads what the helper did lately, newest first.
func (s *Service) Log() ([]Outcome, error) {
	content, err := os.ReadFile(filepath.Join(s.opts.Status, "log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Outcome
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		fields := strings.SplitN(line, "\t", 6)
		if len(fields) < 5 {
			continue
		}
		at, _ := time.Parse(time.RFC3339, fields[0])
		outcome := Outcome{At: at, ID: fields[1], Action: fields[2], Address: fields[3], OK: fields[4] == "ok"}
		if len(fields) == 6 {
			outcome.Message = fields[5]
		}
		out = append([]Outcome{outcome}, out...)
	}
	if len(out) > maxLogLines {
		out = out[:maxLogLines]
	}
	return out, nil
}

// Generate makes a password: four groups of five letters and digits, none of them easy to misread
// (no 0/O, 1/l/I). About 110 bits — nothing to guess.
func Generate() (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var out strings.Builder
	for group := range 4 {
		if group > 0 {
			out.WriteByte('-')
		}
		for range 5 {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return "", err
			}
			out.WriteByte(alphabet[n.Int64()])
		}
	}
	return out.String(), nil
}

// ValidRequestName tells the helper's files from anything else in the directory (tests use it).
func ValidRequestName(name string) bool { return reRequestName.MatchString(name) }
