// Package imap is the small part of IMAP4rev1 (RFC 3501) the service needs to read its own
// mailbox: sign in, look for letters, fetch one, flag it, delete it — and wait for new ones
// without asking every minute (IDLE, RFC 2177). Standard library only.
//
// It is not a general client: one mailbox, UIDs only, TLS from the first byte, and a server
// the installer set up itself (Dovecot). What it reads is still treated as hostile: lines and
// literals have limits, and nothing a letter contains is ever interpreted here.
package imap

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Options say where the mailbox is and whose it is.
type Options struct {
	Addr     string // host:port, TLS from the first byte (993)
	User     string
	Password string
	// Timeout of one command; zero means a minute.
	Timeout time.Duration
	// TLS replaces the default settings: the server's name is checked, except on localhost.
	TLS *tls.Config
}

// Client is one connection with a signed-in user. Not safe for concurrent use.
type Client struct {
	conn    net.Conn
	reader  *bufio.Reader
	timeout time.Duration
	tag     int
	caps    map[string]bool
}

// Error is the server's refusal of a command.
type Error struct {
	Status string // NO | BAD | BYE
	Text   string
}

func (e *Error) Error() string { return "imap: the server said " + e.Status + " " + e.Text }

// TooBigError: the letter is larger than the caller is ready to take; it was not downloaded.
type TooBigError struct{ Size int64 }

func (e *TooBigError) Error() string { return fmt.Sprintf("imap: the letter takes %d bytes", e.Size) }

// ErrNotFound: there is no letter with this UID (any more).
var ErrNotFound = errors.New("imap: no such letter")

const (
	maxLine          = 1 << 20 // the answer to SEARCH in a full mailbox is one long line
	maxSmallLiteral  = 64 << 10
	literalAllowance = 4 << 10 // a server may count a few bytes differently than RFC822.SIZE said
)

// Dial connects, checks who answered, and signs in.
func Dial(ctx context.Context, opts Options) (*Client, error) {
	host, _, err := net.SplitHostPort(opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("imap: address %q: %w", opts.Addr, err)
	}
	config := opts.TLS
	if config == nil {
		// Traffic to localhost never leaves the machine, and the certificate there is issued for
		// the public name of the mail server, not for «127.0.0.1»: encryption yes, name check no.
		config = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		if ip := net.ParseIP(host); (ip != nil && ip.IsLoopback()) || host == "localhost" {
			config.InsecureSkipVerify = true
		}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	conn, err := (&tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: config}).DialContext(ctx, "tcp", opts.Addr)
	if err != nil {
		return nil, err
	}
	client := &Client{conn: conn, reader: bufio.NewReaderSize(conn, 64<<10), timeout: timeout, caps: map[string]bool{}}
	if err := client.greet(opts.User, opts.Password); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}

func (c *Client) greet(user, password string) error {
	c.arm()
	greeting, err := c.read(maxSmallLiteral)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(greeting.text, "* OK") {
		return &Error{Status: "BYE", Text: strings.TrimPrefix(greeting.text, "* ")}
	}
	c.note(greeting.text)
	if len(c.caps) == 0 {
		if _, err := c.command(maxSmallLiteral, "CAPABILITY"); err != nil {
			return err
		}
	}
	switch {
	case c.caps["AUTH=PLAIN"]:
		err = c.authenticatePlain(user, password)
	case c.caps["LOGINDISABLED"]:
		return errors.New("imap: the server offers no way to sign in that this client knows")
	default:
		var quotedUser, quotedPassword string
		if quotedUser, err = quote(user); err == nil {
			if quotedPassword, err = quote(password); err == nil {
				_, err = c.command(maxSmallLiteral, "LOGIN "+quotedUser+" "+quotedPassword)
			}
		}
	}
	if err != nil {
		return err
	}
	// What a server can do may differ before and after signing in.
	if !c.caps["IDLE"] || !c.caps["UIDPLUS"] {
		_, _ = c.command(maxSmallLiteral, "CAPABILITY")
	}
	return nil
}

// authenticatePlain is SASL PLAIN (RFC 4616): any character may be in a password.
func (c *Client) authenticatePlain(user, password string) error {
	tag := c.nextTag()
	c.arm()
	if err := c.send(tag + " AUTHENTICATE PLAIN"); err != nil {
		return err
	}
	for {
		resp, err := c.read(maxSmallLiteral)
		if err != nil {
			return err
		}
		if strings.HasPrefix(resp.text, "+") {
			break
		}
		if done, err := c.tagged(tag, resp.text); done {
			if err == nil {
				err = errors.New("imap: signed in without being asked who — not a server to trust")
			}
			return err
		}
	}
	if err := c.send(base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + password))); err != nil {
		return err
	}
	_, err := c.finish(tag, maxSmallLiteral)
	return err
}

// Has reports whether the server announced a capability («IDLE», «UIDPLUS»…).
func (c *Client) Has(capability string) bool { return c.caps[strings.ToUpper(capability)] }

// Mailbox is what SELECT tells about a mailbox.
type Mailbox struct {
	Exists      uint32
	UIDValidity uint32
	UIDNext     uint32
}

var (
	reExists      = regexp.MustCompile(`^\* (\d+) EXISTS$`)
	reUIDValidity = regexp.MustCompile(`(?i)\[UIDVALIDITY (\d+)\]`)
	reUIDNext     = regexp.MustCompile(`(?i)\[UIDNEXT (\d+)\]`)
	reCapability  = regexp.MustCompile(`(?i)(?:\[CAPABILITY ([^\]]*)\]|^\* CAPABILITY (.*)$)`)
	reFetchSize   = regexp.MustCompile(`(?i)RFC822\.SIZE (\d+)`)
	reFetchUID    = regexp.MustCompile(`(?i)\bUID (\d+)`)
)

// Select opens a mailbox for reading and changing.
func (c *Client) Select(mailbox string) (Mailbox, error) {
	name, err := quote(mailbox)
	if err != nil {
		return Mailbox{}, err
	}
	untagged, err := c.command(maxSmallLiteral, "SELECT "+name)
	if err != nil {
		return Mailbox{}, err
	}
	var out Mailbox
	for _, resp := range untagged {
		if m := reExists.FindStringSubmatch(resp.text); m != nil {
			out.Exists = number(m[1])
		}
		if m := reUIDValidity.FindStringSubmatch(resp.text); m != nil {
			out.UIDValidity = number(m[1])
		}
		if m := reUIDNext.FindStringSubmatch(resp.text); m != nil {
			out.UIDNext = number(m[1])
		}
	}
	return out, nil
}

// Search returns the UIDs of the letters that match, oldest first. The criteria are written as
// in RFC 3501 («UNSEEN», «SEEN BEFORE 1-Jan-2026»); strings inside them go through Quote.
func (c *Client) Search(criteria string) ([]uint32, error) {
	untagged, err := c.command(maxSmallLiteral, "UID SEARCH "+criteria)
	if err != nil {
		return nil, err
	}
	var uids []uint32
	for _, resp := range untagged {
		rest, found := strings.CutPrefix(strings.ToUpper(resp.text), "* SEARCH")
		if !found {
			continue
		}
		for _, field := range strings.Fields(rest) {
			if uid, err := strconv.ParseUint(field, 10, 32); err == nil && uid > 0 {
				uids = append(uids, uint32(uid))
			}
		}
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	return uids, nil
}

// Quote writes a string for use inside search criteria.
func Quote(text string) (string, error) { return quote(text) }

// Size asks how many bytes a letter takes.
func (c *Client) Size(uid uint32) (int64, error) {
	untagged, err := c.command(maxSmallLiteral, fmt.Sprintf("UID FETCH %d (RFC822.SIZE)", uid))
	if err != nil {
		return 0, err
	}
	for _, resp := range untagged {
		if !isFetchOf(resp.text, uid) {
			continue
		}
		if m := reFetchSize.FindStringSubmatch(resp.text); m != nil {
			size, _ := strconv.ParseInt(m[1], 10, 64)
			return size, nil
		}
	}
	return 0, ErrNotFound
}

// Fetch downloads a letter as it is stored, without marking it as read. One that takes more than
// limit bytes is left where it is: TooBigError.
func (c *Client) Fetch(uid uint32, limit int64) ([]byte, error) {
	size, err := c.Size(uid)
	if err != nil {
		return nil, err
	}
	if size > limit {
		return nil, &TooBigError{Size: size}
	}
	untagged, err := c.command(size+literalAllowance, fmt.Sprintf("UID FETCH %d (BODY.PEEK[])", uid))
	if err != nil {
		return nil, err
	}
	for _, resp := range untagged {
		if isFetchOf(resp.text, uid) && len(resp.literals) > 0 {
			return resp.literals[0], nil
		}
	}
	return nil, ErrNotFound
}

func isFetchOf(text string, uid uint32) bool {
	if !strings.Contains(strings.ToUpper(text), " FETCH (") {
		return false
	}
	m := reFetchUID.FindStringSubmatch(text)
	return m != nil && number(m[1]) == uid
}

// AddFlags marks a letter: `\Seen`, `\Deleted`.
func (c *Client) AddFlags(uid uint32, flags ...string) error {
	for _, flag := range flags {
		if !reFlag.MatchString(flag) {
			return fmt.Errorf("imap: %q is not a flag", flag)
		}
	}
	_, err := c.command(maxSmallLiteral, fmt.Sprintf("UID STORE %d +FLAGS.SILENT (%s)", uid, strings.Join(flags, " ")))
	return err
}

var reFlag = regexp.MustCompile(`^\\?[A-Za-z0-9$_-]+$`)

// Delete removes a letter from the mailbox for good.
func (c *Client) Delete(uid uint32) error {
	if err := c.AddFlags(uid, `\Seen`, `\Deleted`); err != nil {
		return err
	}
	// Without UIDPLUS the only EXPUNGE there is removes every letter marked as deleted — in a
	// mailbox nobody else works in, those are this client's own.
	command := "EXPUNGE"
	if c.caps["UIDPLUS"] {
		command = fmt.Sprintf("UID EXPUNGE %d", uid)
	}
	_, err := c.command(maxSmallLiteral, command)
	return err
}

// Idle waits until the server says that something arrived, for at most the given time (servers
// hang up on a connection that idles for half an hour), or until the context is done. It reports
// whether it is worth looking into the mailbox. A server without IDLE is asked once a minute.
func (c *Client) Idle(ctx context.Context, wait time.Duration) (bool, error) {
	if !c.caps["IDLE"] {
		if wait > time.Minute {
			wait = time.Minute
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
		}
		_, err := c.command(maxSmallLiteral, "NOOP")
		return true, err
	}

	tag := c.nextTag()
	c.arm()
	if err := c.send(tag + " IDLE"); err != nil {
		return false, err
	}
	arrived := false
	for {
		resp, err := c.read(maxSmallLiteral)
		if err != nil {
			return false, err
		}
		if strings.HasPrefix(resp.text, "+") {
			break
		}
		if done, err := c.tagged(tag, resp.text); done {
			if err == nil {
				err = errors.New("imap: IDLE ended before it began")
			}
			return false, err
		}
		arrived = c.note(resp.text) || arrived
	}

	// A deadline in the past is how a blocked read is woken up: by the clock, or by the context.
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetReadDeadline(time.Unix(1, 0)) })
	defer stop()
	deadline := time.Now().Add(wait)
	for !arrived && ctx.Err() == nil {
		_ = c.conn.SetReadDeadline(deadline)
		// Peek, not read: when the wait runs out no half of a line is lost.
		if _, err := c.reader.Peek(1); err != nil {
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				break
			}
			return false, err
		}
		c.arm()
		resp, err := c.read(maxSmallLiteral)
		if err != nil {
			return false, err
		}
		if strings.HasPrefix(strings.ToUpper(resp.text), "* BYE") {
			return false, &Error{Status: "BYE", Text: strings.TrimSpace(resp.text[5:])}
		}
		arrived = c.note(resp.text) || arrived
	}
	stop()

	c.arm()
	if err := c.send("DONE"); err != nil {
		return false, err
	}
	untagged, err := c.finish(tag, maxSmallLiteral)
	if err != nil {
		return false, err
	}
	for _, resp := range untagged {
		arrived = reExists.MatchString(resp.text) || arrived
	}
	return arrived, ctx.Err()
}

// Logout says goodbye and closes the connection.
func (c *Client) Logout() error {
	_, err := c.command(maxSmallLiteral, "LOGOUT")
	if closeErr := c.conn.Close(); err == nil {
		err = closeErr
	}
	return err
}

// Close drops the connection without ceremony.
func (c *Client) Close() error { return c.conn.Close() }

// --- the wire ---

type response struct {
	text     string // the line; every literal in it is replaced by «{}»
	literals [][]byte
}

func (c *Client) nextTag() string {
	c.tag++
	return "K" + strconv.Itoa(c.tag)
}

// arm gives the next exchange its time limit.
func (c *Client) arm() { _ = c.conn.SetDeadline(time.Now().Add(c.timeout)) }

func (c *Client) send(line string) error {
	_, err := c.conn.Write([]byte(line + "\r\n"))
	return err
}

// command sends one command and collects what the server says before the tagged answer.
func (c *Client) command(maxLiteral int64, line string) ([]response, error) {
	tag := c.nextTag()
	c.arm()
	if err := c.send(tag + " " + line); err != nil {
		return nil, err
	}
	return c.finish(tag, maxLiteral)
}

func (c *Client) finish(tag string, maxLiteral int64) ([]response, error) {
	var untagged []response
	for {
		resp, err := c.read(maxLiteral)
		if err != nil {
			return nil, err
		}
		if done, err := c.tagged(tag, resp.text); done {
			return untagged, err
		}
		if strings.HasPrefix(resp.text, "+") {
			return nil, errors.New("imap: the server waits for a continuation nobody promised")
		}
		c.note(resp.text)
		untagged = append(untagged, resp)
	}
}

// tagged recognises the answer that ends a command.
func (c *Client) tagged(tag, text string) (done bool, err error) {
	rest, found := strings.CutPrefix(text, tag+" ")
	if !found {
		return false, nil
	}
	status, explanation, _ := strings.Cut(rest, " ")
	if strings.EqualFold(status, "OK") {
		c.note(text)
		return true, nil
	}
	return true, &Error{Status: strings.ToUpper(status), Text: explanation}
}

// note keeps what the server mentions in passing; it reports whether letters arrived.
func (c *Client) note(text string) (arrived bool) {
	if m := reCapability.FindStringSubmatch(text); m != nil {
		c.caps = map[string]bool{}
		for _, capability := range strings.Fields(m[1] + " " + m[2]) {
			c.caps[strings.ToUpper(capability)] = true
		}
	}
	return reExists.MatchString(text)
}

// read takes one response: a line, with the literals it announces («{123}» at the end of a line
// means 123 raw bytes follow, and then the line goes on).
func (c *Client) read(maxLiteral int64) (response, error) {
	var out response
	var text strings.Builder
	for {
		line, err := c.readLine()
		if err != nil {
			return response{}, err
		}
		open := strings.LastIndexByte(line, '{')
		if open < 0 || !strings.HasSuffix(line, "}") {
			text.WriteString(line)
			out.text = text.String()
			return out, nil
		}
		size, err := strconv.ParseInt(line[open+1:len(line)-1], 10, 64)
		if err != nil || size < 0 {
			text.WriteString(line) // braces that are just text
			out.text = text.String()
			return out, nil
		}
		if size > maxLiteral {
			return response{}, fmt.Errorf("imap: the server sends %d bytes where at most %d were expected", size, maxLiteral)
		}
		literal := make([]byte, size)
		if _, err := readFull(c.reader, literal); err != nil {
			return response{}, err
		}
		text.WriteString(line[:open] + "{}")
		out.literals = append(out.literals, literal)
		if text.Len() > maxLine {
			return response{}, errors.New("imap: a response is too long")
		}
	}
}

func (c *Client) readLine() (string, error) {
	var line []byte
	for {
		chunk, err := c.reader.ReadSlice('\n')
		line = append(line, chunk...)
		if err == nil {
			return strings.TrimRight(string(line), "\r\n"), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return "", err
		}
		if len(line) > maxLine {
			return "", errors.New("imap: a response line is too long")
		}
	}
}

func readFull(reader *bufio.Reader, into []byte) (int, error) {
	read := 0
	for read < len(into) {
		n, err := reader.Read(into[read:])
		read += n
		if err != nil {
			return read, err
		}
	}
	return read, nil
}

// quote writes a quoted string. Line breaks and bytes outside ASCII would need a literal, and
// nothing this client says needs those.
func quote(text string) (string, error) {
	var out strings.Builder
	out.WriteByte('"')
	for i := 0; i < len(text); i++ {
		switch b := text[i]; {
		case b == '\r', b == '\n', b == 0, b >= 0x80:
			return "", fmt.Errorf("imap: %q cannot be written as a quoted string", text)
		case b == '"', b == '\\':
			out.WriteByte('\\')
			out.WriteByte(b)
		default:
			out.WriteByte(b)
		}
	}
	out.WriteByte('"')
	return out.String(), nil
}

func number(digits string) uint32 {
	n, _ := strconv.ParseUint(digits, 10, 32)
	return uint32(n)
}
