package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"time"
)

// Sender submits messages to an SMTP server — on the server, the site's own mail server on
// localhost (docker-mailserver signs with DKIM and delivers).
type Sender struct {
	Addr     string // host:port; 465 means TLS from the first byte, anything else STARTTLS when offered
	User     string
	Password string
	// Hello is the name given in EHLO: the site's host name.
	Hello string
	// Envelope is the address given in MAIL FROM — where bounces go. The mail server lets an
	// account send only under its own address (spoof protection), so this is the account the
	// service signs in with, while the From header stays the owner's. Empty: the From address.
	Envelope string
	// Now is the clock of the Date header.
	Now func() time.Time
}

// PermanentError is an answer of the server that no retry will change (SMTP 5xx).
type PermanentError struct{ Err error }

func (e PermanentError) Error() string { return e.Err.Error() }
func (e PermanentError) Unwrap() error { return e.Err }

// Send delivers one message to the server. A temporary failure — no connection, SMTP 4xx —
// is a plain error: try again later.
func (s *Sender) Send(ctx context.Context, message Message) error {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	raw, err := message.Bytes(now())
	if err != nil {
		return PermanentError{err}
	}
	host, port, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return PermanentError{fmt.Errorf("mail: SMTP address %q: %w", s.Addr, err)}
	}
	// Traffic to localhost never leaves the machine, and the certificate there is issued for the
	// public name of the mail server, not for «127.0.0.1»: encryption yes, name check no.
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if ip := net.ParseIP(host); (ip != nil && ip.IsLoopback()) || host == "localhost" {
		tlsConfig.InsecureSkipVerify = true
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	if port == "465" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", s.Addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", s.Addr)
	}
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(time.Minute))
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return classify(err)
	}
	defer func() { _ = client.Close() }()

	if s.Hello != "" {
		if err := client.Hello(s.Hello); err != nil {
			return classify(err)
		}
	}
	if offered, _ := client.Extension("STARTTLS"); offered && port != "465" {
		if err := client.StartTLS(tlsConfig); err != nil {
			return classify(err)
		}
	}
	if s.User != "" {
		if offered, _ := client.Extension("AUTH"); !offered {
			return errors.New("mail: the server does not offer authentication on this connection")
		}
		if err := client.Auth(smtp.PlainAuth("", s.User, s.Password, host)); err != nil {
			return classify(err)
		}
	}
	envelope := s.Envelope
	if envelope == "" {
		envelope = message.From.Address
	}
	if err := client.Mail(envelope); err != nil {
		return classify(err)
	}
	if err := client.Rcpt(message.To.Address); err != nil {
		return classify(err)
	}
	body, err := client.Data()
	if err != nil {
		return classify(err)
	}
	if _, err := body.Write(raw); err != nil {
		return classify(err)
	}
	if err := body.Close(); err != nil {
		return classify(err)
	}
	return classify(client.Quit())
}

// classify tells permanent refusals (5xx) from everything that may pass next time.
func classify(err error) error {
	var reply *textproto.Error
	if errors.As(err, &reply) && reply.Code >= 500 {
		return PermanentError{err}
	}
	return err
}
