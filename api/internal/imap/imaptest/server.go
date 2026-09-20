// Package imaptest is a mailbox for tests: a small IMAP server in memory that speaks the part of
// the protocol the imap package uses — over TLS, as the real one does.
package imaptest

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Message is a letter as the mailbox keeps it.
type Message struct {
	UID      uint32
	Raw      []byte
	Flags    map[string]bool
	Received time.Time
}

// Server is one mailbox («INBOX») of one user.
type Server struct {
	Addr     string
	User     string
	Password string

	mu          sync.Mutex
	noIdle      bool
	noPlain     bool
	listener    net.Listener
	uidValidity uint32
	nextUID     uint32
	messages    []*Message
	sessions    map[*session]bool
	commands    []string
	closed      bool
}

type session struct {
	conn    net.Conn
	mu      sync.Mutex // writes: commands' answers and notifications of IDLE interleave
	idling  bool
	idleTag string
}

// New starts a server on a free port of localhost. It stops with the test.
func New(t testing.TB, user, password string) *Server {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate(t)}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		Addr: listener.Addr().String(), User: user, Password: password,
		listener: listener, uidValidity: 1700000000, nextUID: 1, sessions: map[*session]bool{},
	}
	go server.accept()
	t.Cleanup(server.Close)
	return server
}

// Without takes capabilities away — «IDLE», «AUTH=PLAIN» — to test what a client does without them.
func (s *Server) Without(capabilities ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, capability := range capabilities {
		switch strings.ToUpper(capability) {
		case "IDLE":
			s.noIdle = true
		case "AUTH=PLAIN":
			s.noPlain = true
		}
	}
}

func (s *Server) offers(capability string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch capability {
	case "IDLE":
		return !s.noIdle
	case "AUTH=PLAIN":
		return !s.noPlain
	}
	return true
}

// Close stops the server and hangs up on everybody.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	_ = s.listener.Close()
	s.Hangup()
}

// Hangup drops every connection, as a restarting mail server does.
func (s *Server) Hangup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sess := range s.sessions {
		_ = sess.conn.Close()
	}
}

// Deliver puts a letter into the mailbox and tells everybody who waits in IDLE.
func (s *Server) Deliver(raw []byte) uint32 {
	s.mu.Lock()
	uid := s.nextUID
	s.nextUID++
	s.messages = append(s.messages, &Message{UID: uid, Raw: append([]byte(nil), raw...), Flags: map[string]bool{}, Received: time.Now()})
	count := len(s.messages)
	var waiting []*session
	for sess := range s.sessions {
		waiting = append(waiting, sess)
	}
	s.mu.Unlock()
	for _, sess := range waiting {
		sess.mu.Lock()
		if sess.idling {
			_, _ = fmt.Fprintf(sess.conn, "* %d EXISTS\r\n", count)
		}
		sess.mu.Unlock()
	}
	return uid
}

// Backdate makes a letter look as if it arrived earlier.
func (s *Server) Backdate(uid uint32, received time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, message := range s.messages {
		if message.UID == uid {
			message.Received = received
		}
	}
}

// Messages returns what is in the mailbox now.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, 0, len(s.messages))
	for _, message := range s.messages {
		flags := map[string]bool{}
		for flag := range message.Flags {
			flags[flag] = true
		}
		out = append(out, Message{UID: message.UID, Raw: message.Raw, Flags: flags, Received: message.Received})
	}
	return out
}

// Commands returns every command the server was given, passwords left out.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// Sessions tells how many connections are open.
func (s *Server) Sessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *Server) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		sess := &session{conn: conn}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.sessions[sess] = true
		s.mu.Unlock()
		go s.serve(sess)
	}
}

func (s *Server) capabilities() string {
	caps := "IMAP4rev1 UIDPLUS"
	for _, capability := range []string{"IDLE", "AUTH=PLAIN"} {
		if s.offers(capability) {
			caps += " " + capability
		}
	}
	return caps
}

func (sess *session) say(format string, args ...any) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	_, _ = fmt.Fprintf(sess.conn, format+"\r\n", args...)
}

var (
	reCommand = regexp.MustCompile(`^(\S+) (?i:(UID )?)(\S+) ?(.*)$`)
	reLogin   = regexp.MustCompile(`^"((?:[^"\\]|\\.)*)" "((?:[^"\\]|\\.)*)"$`)
	reHeader  = regexp.MustCompile(`(?i)HEADER (\S+) "((?:[^"\\]|\\.)*)"`)
	reBefore  = regexp.MustCompile(`(?i)\bBEFORE (\d{1,2}-[A-Za-z]{3}-\d{4})`)
)

func (s *Server) serve(sess *session) {
	defer func() {
		_ = sess.conn.Close()
		s.mu.Lock()
		delete(s.sessions, sess)
		s.mu.Unlock()
	}()
	reader := bufio.NewReader(sess.conn)
	sess.say("* OK [CAPABILITY %s] imaptest ready", s.capabilities())
	signedIn, selected := false, false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if sess.idling {
			if strings.EqualFold(line, "DONE") {
				sess.mu.Lock()
				sess.idling = false
				sess.mu.Unlock()
				sess.say("%s OK Idle completed", sess.idleTag)
			}
			continue
		}
		m := reCommand.FindStringSubmatch(line)
		if m == nil {
			sess.say("* BAD what?")
			continue
		}
		tag, byUID, name, args := m[1], m[2] != "", strings.ToUpper(m[3]), m[4]
		s.mu.Lock()
		if name == "LOGIN" {
			s.commands = append(s.commands, "LOGIN …")
		} else {
			s.commands = append(s.commands, strings.TrimSpace(m[2]+name+" "+args))
		}
		s.mu.Unlock()

		switch {
		case name == "CAPABILITY":
			sess.say("* CAPABILITY %s", s.capabilities())
			sess.say("%s OK done", tag)
		case name == "LOGOUT":
			sess.say("* BYE see you")
			sess.say("%s OK done", tag)
			return
		case name == "NOOP":
			sess.say("%s OK done", tag)
		case name == "AUTHENTICATE" && strings.EqualFold(args, "PLAIN") && s.offers("AUTH=PLAIN"):
			sess.say("+ ")
			answer, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			decoded, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(answer))
			parts := bytes.Split(decoded, []byte{0})
			if len(parts) == 3 && string(parts[1]) == s.User && string(parts[2]) == s.Password {
				signedIn = true
				sess.say("%s OK [CAPABILITY %s] Logged in", tag, s.capabilities())
			} else {
				sess.say("%s NO [AUTHENTICATIONFAILED] Authentication failed.", tag)
			}
		case name == "LOGIN":
			login := reLogin.FindStringSubmatch(args)
			if login != nil && unquote(login[1]) == s.User && unquote(login[2]) == s.Password {
				signedIn = true
				sess.say("%s OK Logged in", tag)
			} else {
				sess.say("%s NO [AUTHENTICATIONFAILED] Authentication failed.", tag)
			}
		case !signedIn:
			sess.say("%s BAD sign in first", tag)
		case name == "SELECT":
			if unquote(strings.Trim(args, `"`)) != "INBOX" {
				sess.say("%s NO no such mailbox", tag)
				continue
			}
			selected = true
			s.mu.Lock()
			count, validity, next := len(s.messages), s.uidValidity, s.nextUID
			s.mu.Unlock()
			sess.say(`* FLAGS (\Answered \Flagged \Deleted \Seen \Draft)`)
			sess.say("* %d EXISTS", count)
			sess.say("* 0 RECENT")
			sess.say("* OK [UIDVALIDITY %d] UIDs valid", validity)
			sess.say("* OK [UIDNEXT %d] Predicted next UID", next)
			sess.say("%s OK [READ-WRITE] Select completed", tag)
		case !selected:
			sess.say("%s BAD select a mailbox first", tag)
		case name == "IDLE" && s.offers("IDLE"):
			sess.mu.Lock()
			sess.idling, sess.idleTag = true, tag
			sess.mu.Unlock()
			sess.say("+ idling")
		case name == "SEARCH" && byUID:
			sess.say("* SEARCH%s", s.search(args))
			sess.say("%s OK Search completed", tag)
		case name == "FETCH" && byUID:
			s.fetch(sess, args)
			sess.say("%s OK Fetch completed", tag)
		case name == "STORE" && byUID:
			s.store(args)
			sess.say("%s OK Store completed", tag)
		case name == "EXPUNGE":
			s.expunge(sess, args, byUID)
			sess.say("%s OK Expunge completed", tag)
		default:
			sess.say("%s BAD unknown command", tag)
		}
	}
}

func (s *Server) search(criteria string) string {
	upper := " " + strings.ToUpper(criteria) + " "
	var before time.Time
	if m := reBefore.FindStringSubmatch(criteria); m != nil {
		before, _ = time.Parse("2-Jan-2006", m[1])
	}
	header := reHeader.FindStringSubmatch(criteria)

	s.mu.Lock()
	defer s.mu.Unlock()
	var out strings.Builder
	for _, message := range s.messages {
		switch {
		case strings.Contains(upper, " UNSEEN ") && message.Flags[`\Seen`],
			strings.Contains(upper, " SEEN ") && !message.Flags[`\Seen`],
			!before.IsZero() && !message.Received.Before(before):
			continue
		}
		if header != nil {
			parsed, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(message.Raw))).ReadMIMEHeader()
			if err != nil && len(parsed) == 0 {
				continue
			}
			if !strings.Contains(strings.ToLower(parsed.Get(header[1])), strings.ToLower(unquote(header[2]))) {
				continue
			}
		}
		fmt.Fprintf(&out, " %d", message.UID)
	}
	return out.String()
}

func (s *Server) fetch(sess *session, args string) {
	set, items, _ := strings.Cut(args, " ")
	uid, _ := strconv.ParseUint(set, 10, 32)
	s.mu.Lock()
	var found *Message
	seq := 0
	for i, message := range s.messages {
		if uint64(message.UID) == uid {
			found, seq = message, i+1
		}
	}
	s.mu.Unlock()
	if found == nil {
		return
	}
	upper := strings.ToUpper(items)
	sess.mu.Lock()
	defer sess.mu.Unlock()
	_, _ = fmt.Fprintf(sess.conn, "* %d FETCH (UID %d", seq, found.UID)
	if strings.Contains(upper, "RFC822.SIZE") {
		_, _ = fmt.Fprintf(sess.conn, " RFC822.SIZE %d", len(found.Raw))
	}
	if strings.Contains(upper, "BODY.PEEK[]") {
		_, _ = fmt.Fprintf(sess.conn, " BODY[] {%d}\r\n", len(found.Raw))
		_, _ = sess.conn.Write(found.Raw)
	}
	_, _ = fmt.Fprint(sess.conn, ")\r\n")
}

func (s *Server) store(args string) {
	set, rest, _ := strings.Cut(args, " ")
	uid, _ := strconv.ParseUint(set, 10, 32)
	open, end := strings.IndexByte(rest, '('), strings.LastIndexByte(rest, ')')
	if open < 0 || end < open || !strings.HasPrefix(strings.ToUpper(rest), "+FLAGS") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, message := range s.messages {
		if uint64(message.UID) == uid {
			for _, flag := range strings.Fields(rest[open+1 : end]) {
				message.Flags[flag] = true
			}
		}
	}
}

func (s *Server) expunge(sess *session, args string, byUID bool) {
	only, _ := strconv.ParseUint(strings.TrimSpace(args), 10, 32)
	s.mu.Lock()
	var kept []*Message
	var gone []int
	for i, message := range s.messages {
		if message.Flags[`\Deleted`] && (!byUID || uint64(message.UID) == only) {
			gone = append(gone, i+1-len(gone)) // sequence numbers shift as letters go
			continue
		}
		kept = append(kept, message)
	}
	s.messages = kept
	s.mu.Unlock()
	for _, seq := range gone {
		sess.say("* %d EXPUNGE", seq)
	}
}

func unquote(text string) string {
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(text)
}

// certificate makes a self-signed certificate for «mail.test»: the client reaches the server by
// the address of localhost, exactly as the service reaches the mail server of an installation.
func certificate(t testing.TB) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mail.test"}, DNSNames: []string{"mail.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
