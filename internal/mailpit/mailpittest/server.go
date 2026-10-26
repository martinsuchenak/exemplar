// Package mailpittest is a minimal in-process SMTP server for tests: it
// accepts every message and records the envelope and data, like Mailpit.
package mailpittest

import (
	"bufio"
	"encoding/base64"
	"net"
	"strings"
	"sync"
	"testing"
)

// Received is one message the server accepted.
type Received struct {
	From string
	To   []string
	Data string
	// Auth is the "user:password" a client authenticated with, if any.
	Auth string
}

// Server records what clients send. Set options through Start's configure
// functions, before it accepts connections.
type Server struct {
	Addr string
	// RejectRcpt makes RCPT TO fail for this address.
	RejectRcpt string
	// RequireAuth advertises AUTH PLAIN and refuses MAIL until it succeeds.
	RequireAuth bool

	ln       net.Listener
	mu       sync.Mutex
	received []Received
}

// Start listens on a free localhost port until the test ends. configure
// functions set options before the first connection.
func Start(t *testing.T, configure ...func(*Server)) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &Server{Addr: ln.Addr().String(), ln: ln}
	for _, c := range configure {
		c(s)
	}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

// Received returns the messages accepted so far.
func (s *Server) Received() []Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Received{}, s.received...)
}

func (s *Server) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	reply := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	reply("220 mailpittest ESMTP")
	var cur Received
	authed := !s.RequireAuth
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			if s.RequireAuth {
				reply("250-mailpittest")
				reply("250 AUTH PLAIN")
			} else {
				reply("250 mailpittest")
			}
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 {
				cur.Auth = parts[1] + ":" + parts[2]
				authed = true
				reply("235 authenticated")
			} else {
				reply("535 bad credentials")
			}
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			if !authed {
				reply("530 authentication required")
				continue
			}
			cur.From = strings.Trim(line[len("MAIL FROM:"):], "<> ")
			reply("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			rcpt := strings.Trim(line[len("RCPT TO:"):], "<> ")
			if rcpt == s.RejectRcpt {
				reply("550 no such user")
				continue
			}
			cur.To = append(cur.To, rcpt)
			reply("250 ok")
		case cmd == "DATA":
			reply("354 send data")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			cur.Data = b.String()
			s.mu.Lock()
			s.received = append(s.received, cur)
			s.mu.Unlock()
			cur = Received{Auth: cur.Auth}
			reply("250 queued")
		case cmd == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}
