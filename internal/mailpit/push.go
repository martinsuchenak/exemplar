// Package mailpit sends stored email fixtures to a test SMTP server such as
// Mailpit. It works on message bytes the caller already fetched through the
// REST API, so it needs no storage access and makes no access decisions.
package mailpit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// DefaultFrom is the envelope sender when a message has no usable From.
const DefaultFrom = "exemplar@exemplar.test"

// Message is one email to push.
type Message struct {
	FixtureID string
	Name      string
	// Data is the complete RFC 5322 message.
	Data []byte
}

// Options configure a push.
type Options struct {
	// Addr is the SMTP server, host:port.
	Addr string
	// Run tags every message of one push (X-Tags: exemplar-run-<Run>), so a
	// test can find its own messages; NewRunID makes one.
	Run string
	// To, when set, replaces every message's envelope recipients, delivering
	// the whole run to one mailbox. Headers are left as they are.
	To []string
	// Raw sends the stored bytes unchanged, without the X- headers.
	Raw bool
	// Username and Password authenticate with AUTH PLAIN when set.
	Username string
	Password string
	// Timeout bounds each message's connection; 0 means 30 seconds.
	Timeout time.Duration
}

// Result is the outcome for one message.
type Result struct {
	FixtureID string   `json:"fixture_id"`
	Name      string   `json:"name"`
	From      string   `json:"from"`
	To        []string `json:"to"`
	Error     string   `json:"error,omitempty"`
}

// NewRunID returns a run ID such as 20261027-091502-3fa1.
func NewRunID(now time.Time) string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// Tag is the Mailpit tag a run's messages carry.
func Tag(run string) string {
	return "exemplar-run-" + run
}

// Push sends every message and reports each one's outcome. A message that
// fails does not stop the others; the error is only for problems with the
// options themselves.
func Push(ctx context.Context, opts Options, msgs []Message) ([]Result, error) {
	if opts.Addr == "" {
		return nil, errors.New("an SMTP address is required")
	}
	if _, _, err := net.SplitHostPort(opts.Addr); err != nil {
		return nil, fmt.Errorf("SMTP address must be host:port: %w", err)
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	results := make([]Result, 0, len(msgs))
	for _, m := range msgs {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		r := Result{FixtureID: m.FixtureID, Name: m.Name}
		from, to, data, err := Prepare(m, opts)
		r.From, r.To = from, to
		if err == nil {
			err = send(ctx, opts, from, to, data)
		}
		if err != nil {
			r.Error = err.Error()
		}
		results = append(results, r)
	}
	return results, nil
}

// Prepare works out a message's envelope and the bytes to send.
func Prepare(m Message, opts Options) (from string, to []string, data []byte, err error) {
	msg, err := mail.ReadMessage(bytes.NewReader(m.Data))
	if err != nil {
		return "", nil, nil, fmt.Errorf("not a valid email: %w", err)
	}
	from = DefaultFrom
	if addrs, err := msg.Header.AddressList("From"); err == nil && len(addrs) > 0 {
		from = addrs[0].Address
	}
	to = opts.To
	if len(to) == 0 {
		for _, h := range []string{"To", "Cc", "Bcc"} {
			addrs, err := msg.Header.AddressList(h)
			if err != nil {
				continue
			}
			for _, a := range addrs {
				to = append(to, a.Address)
			}
		}
	}
	if len(to) == 0 {
		return from, nil, nil, errors.New("no recipients: the message has no To, Cc or Bcc; use --to")
	}
	data = m.Data
	if !opts.Raw {
		var b bytes.Buffer
		if opts.Run != "" {
			fmt.Fprintf(&b, "X-Tags: %s\r\nX-Exemplar-Run: %s\r\n", Tag(opts.Run), opts.Run)
		}
		fmt.Fprintf(&b, "X-Exemplar-Fixture: %s\r\n", headerValue(m.FixtureID))
		b.Write(m.Data)
		data = b.Bytes()
	}
	return from, to, data, nil
}

// send delivers one message over its own connection.
func send(ctx context.Context, opts Options, from string, to []string, data []byte) error {
	dialer := net.Dialer{Timeout: opts.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", opts.Addr)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", opts.Addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(opts.Timeout))
	host, _, _ := net.SplitHostPort(opts.Addr)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("SMTP greeting: %w", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello("exemplar"); err != nil {
		return fmt.Errorf("EHLO: %w", err)
	}
	if opts.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", opts.Username, opts.Password, host)); err != nil {
			return fmt.Errorf("AUTH: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return fmt.Errorf("sending message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("sending message: %w", err)
	}
	return c.Quit()
}

// headerValue keeps a value on one header line.
func headerValue(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}
