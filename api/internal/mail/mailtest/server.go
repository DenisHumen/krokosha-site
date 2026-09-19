// Package mailtest is a tiny SMTP server for tests: it accepts messages on localhost, keeps them,
// and can be told to refuse. The «mock SMTP» of brief B10.7.
package mailtest

import (
	"bufio"
	"fmt"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
)

// Received is a message the server accepted.
type Received struct {
	From string
	To   []string
	Raw  string // headers and body as sent
}

// Message parses the raw text.
func (r Received) Message() (*mail.Message, error) {
	return mail.ReadMessage(strings.NewReader(r.Raw))
}

// Server is a running test server.
type Server struct {
	Addr string

	mu       sync.Mutex
	messages []Received
	reject   string // when set: the answer to RCPT, e.g. «550 no such user» or «451 try later»
	listener net.Listener
}

// Start runs a server until the test ends.
func Start(t *testing.T) *Server {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // a test helper
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Addr: listener.Addr().String(), listener: listener}
	t.Cleanup(func() { _ = listener.Close() })
	go server.serve()
	return server
}

// Stop closes the listener: connections are refused from now on.
func (s *Server) Stop() { _ = s.listener.Close() }

// RejectWith makes the server answer every recipient with the given line ("" = accept again).
func (s *Server) RejectWith(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reject = line
}

// Messages returns what was accepted so far.
func (s *Server) Messages() []Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Received(nil), s.messages...)
}

func (s *Server) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.session(conn)
	}
}

func (s *Server) session(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	say := func(line string) { _, _ = fmt.Fprintf(conn, "%s\r\n", line) }

	say("220 mailtest ESMTP")
	var current Received
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(command, "EHLO"), strings.HasPrefix(command, "HELO"):
			say("250-mailtest")
			say("250 8BITMIME")
		case strings.HasPrefix(command, "MAIL FROM:"):
			current = Received{From: address(line)}
			say("250 ok")
		case strings.HasPrefix(command, "RCPT TO:"):
			s.mu.Lock()
			reject := s.reject
			s.mu.Unlock()
			if reject != "" {
				say(reject)
				continue
			}
			current.To = append(current.To, address(line))
			say("250 ok")
		case command == "DATA":
			say("354 go ahead")
			var body strings.Builder
			for {
				data, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if data == ".\r\n" {
					break
				}
				body.WriteString(strings.TrimPrefix(data, ".")) // dot-stuffing
			}
			current.Raw = body.String()
			s.mu.Lock()
			s.messages = append(s.messages, current)
			s.mu.Unlock()
			say("250 queued")
		case command == "QUIT":
			say("221 bye")
			return
		case command == "RSET", command == "NOOP":
			say("250 ok")
		default:
			say("502 not implemented")
		}
	}
}

func address(line string) string {
	start, end := strings.Index(line, "<"), strings.Index(line, ">")
	if start < 0 || end < start {
		return ""
	}
	return line[start+1 : end]
}
