// Package mailtest is an in-process fake SMTP server for tests: it speaks
// enough ESMTP (EHLO, STARTTLS, AUTH PLAIN/LOGIN, MAIL, RCPT, DATA) for
// net/smtp and records every accepted message.
package mailtest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/config"
)

// Message is one accepted message.
type Message struct {
	From string
	To   []string
	// Data is the raw message (headers + body, CRLF).
	Data string
	// User and Pass are the AUTH credentials of the session ("" without AUTH).
	User, Pass string
	// TLS reports whether the session was encrypted.
	TLS bool
}

// Header parses the message headers (nil on error).
func (m Message) Header() mail.Header {
	msg, err := mail.ReadMessage(strings.NewReader(m.Data))
	if err != nil {
		return nil
	}
	return msg.Header
}

var codeRe = regexp.MustCompile(`\b(\d{6})\b`)

// Code returns the first 6-digit code in the message body ("" when none).
func (m Message) Code() string {
	_, body, _ := strings.Cut(m.Data, "\r\n\r\n")
	if s := codeRe.FindStringSubmatch(body); s != nil {
		return s[1]
	}
	return ""
}

var linkRe = regexp.MustCompile(`https?://[^\s"<>]+`)

// Link returns the first http(s) URL of the plain-text part, with
// quoted-printable soft breaks removed ("" when none).
func (m Message) Link() string {
	data := strings.ReplaceAll(m.Data, "=\r\n", "")
	data = strings.ReplaceAll(data, "=3D", "=")
	if s := linkRe.FindString(data); s != "" {
		return s
	}
	return ""
}

// Server is a fake SMTP server.
type Server struct {
	// Addr is host:port of the listener.
	Addr string
	// Host and Port split Addr.
	Host string
	Port int

	ln       net.Listener
	tlsCfg   *tls.Config
	implicit bool
	// Credentials required for AUTH (empty: AUTH accepts anything).
	user, pass string

	mu      sync.Mutex
	msgs    []Message
	notify  chan struct{}
	rejectR bool
	wg      sync.WaitGroup
}

// Option configures a Server.
type Option func(*Server)

// WithAuth requires AUTH with these credentials.
func WithAuth(user, pass string) Option { return func(s *Server) { s.user, s.pass = user, pass } }

// WithSTARTTLS offers STARTTLS with a self-signed certificate.
func WithSTARTTLS() Option { return func(s *Server) { s.tlsCfg = selfSigned() } }

// WithImplicitTLS serves SMTPS (TLS from the first byte).
func WithImplicitTLS() Option {
	return func(s *Server) { s.tlsCfg, s.implicit = selfSigned(), true }
}

// Start runs a server until the test ends.
func Start(t testing.TB, opts ...Option) *Server {
	t.Helper()
	s := &Server{notify: make(chan struct{}, 64)}
	for _, o := range opts {
		o(s)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if s.implicit {
		ln = tls.NewListener(ln, s.tlsCfg)
	}
	s.ln, s.Addr = ln, ln.Addr().String()
	host, port, _ := net.SplitHostPort(s.Addr)
	s.Host = host
	s.Port, _ = strconv.Atoi(port)
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		s.wg.Wait()
	})
	return s
}

// Config returns an SMTP configuration pointing at the server.
func (s *Server) Config() config.SMTPConfig {
	enc := config.SMTPEncryptionNone
	switch {
	case s.implicit:
		enc = config.SMTPEncryptionTLS
	case s.tlsCfg != nil:
		enc = config.SMTPEncryptionSTARTTLS
	}
	return config.SMTPConfig{Host: s.Host, Port: s.Port, Username: s.user, Password: config.Secret(s.pass),
		From: "Panel <panel@example.test>", Encryption: enc, InsecureSkipVerify: s.tlsCfg != nil}
}

// RejectRecipients makes RCPT TO fail with 550.
func (s *Server) RejectRecipients(v bool) {
	s.mu.Lock()
	s.rejectR = v
	s.mu.Unlock()
}

// Messages returns the accepted messages so far.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.msgs...)
}

// Wait blocks until at least n messages arrived (fails the test after 5 s).
func (s *Server) Wait(t testing.TB, n int) []Message {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if m := s.Messages(); len(m) >= n {
			return m
		}
		select {
		case <-s.notify:
		case <-deadline:
			t.Fatalf("mailtest: want %d messages, got %d", n, len(s.Messages()))
		}
	}
}

// Last waits for at least one message and returns the newest.
func (s *Server) Last(t testing.TB) Message {
	t.Helper()
	m := s.Wait(t, 1)
	return m[len(m)-1]
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.session(c)
		}()
	}
}

func (s *Server) session(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	r, w := bufio.NewReader(c), bufio.NewWriter(c)
	reply := func(lines ...string) {
		for _, l := range lines {
			_, _ = w.WriteString(l + "\r\n")
		}
		_ = w.Flush()
	}
	read := func() (string, bool) {
		l, err := r.ReadString('\n')
		if err != nil {
			return "", false
		}
		return strings.TrimRight(l, "\r\n"), true
	}
	_, isTLS := c.(*tls.Conn)
	var cur Message
	authed := s.user == ""
	reply("220 mailtest ESMTP")
	for {
		line, ok := read()
		if !ok {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			ext := []string{"250-mailtest"}
			if s.tlsCfg != nil && !isTLS {
				ext = append(ext, "250-STARTTLS")
			}
			ext = append(ext, "250-AUTH PLAIN LOGIN", "250 8BITMIME")
			reply(ext...)
		case "STARTTLS":
			if s.tlsCfg == nil || isTLS {
				reply("502 not supported")
				continue
			}
			reply("220 go ahead")
			tc := tls.Server(c, s.tlsCfg)
			if err := tc.Handshake(); err != nil {
				return
			}
			c, isTLS = tc, true
			r, w = bufio.NewReader(tc), bufio.NewWriter(tc)
			cur = Message{}
		case "AUTH":
			mech, rest, _ := strings.Cut(arg, " ")
			var user, pass string
			switch strings.ToUpper(mech) {
			case "PLAIN":
				if rest == "" {
					reply("334 ")
					rest, _ = read()
				}
				raw, _ := base64.StdEncoding.DecodeString(rest)
				parts := strings.Split(string(raw), "\x00")
				if len(parts) == 3 {
					user, pass = parts[1], parts[2]
				}
			case "LOGIN":
				reply("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
				l, _ := read()
				u, _ := base64.StdEncoding.DecodeString(l)
				reply("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
				l, _ = read()
				p, _ := base64.StdEncoding.DecodeString(l)
				user, pass = string(u), string(p)
			default:
				reply("504 unsupported")
				continue
			}
			if s.user != "" && (user != s.user || pass != s.pass) {
				reply("535 authentication failed")
				continue
			}
			authed, cur.User, cur.Pass = true, user, pass
			reply("235 ok")
		case "MAIL":
			if !authed {
				reply("530 authentication required")
				continue
			}
			cur.From = addrArg(arg)
			cur.To = nil
			reply("250 ok")
		case "RCPT":
			s.mu.Lock()
			rej := s.rejectR
			s.mu.Unlock()
			if rej {
				reply("550 no such user")
				continue
			}
			cur.To = append(cur.To, addrArg(arg))
			reply("250 ok")
		case "DATA":
			reply("354 end with .")
			var b strings.Builder
			for {
				l, ok := read()
				if !ok {
					return
				}
				if l == "." {
					break
				}
				b.WriteString(strings.TrimPrefix(l, ".") + "\r\n")
			}
			m := cur
			m.Data, m.TLS = b.String(), isTLS
			s.mu.Lock()
			s.msgs = append(s.msgs, m)
			s.mu.Unlock()
			select {
			case s.notify <- struct{}{}:
			default:
			}
			reply("250 queued")
		case "RSET":
			cur = Message{User: cur.User, Pass: cur.Pass}
			reply("250 ok")
		case "NOOP":
			reply("250 ok")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 unknown command")
		}
	}
}

func addrArg(arg string) string {
	if i := strings.IndexByte(arg, '<'); i >= 0 {
		if j := strings.IndexByte(arg[i:], '>'); j > 0 {
			return arg[i+1 : i+j]
		}
	}
	return arg
}

func selfSigned() *tls.Config {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mailtest"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
}
