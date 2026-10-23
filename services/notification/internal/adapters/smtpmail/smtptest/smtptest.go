// Package smtptest — фальшивый SMTP-сервер для тестов: слушает 127.0.0.1, принимает письма, умеет «лежать»
// (421 на подключение) и отклонять адресата (550). Без Docker и сети.
package smtptest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

// Message — принятое письмо: конверт и сырые данные (после DATA, концы строк LF).
type Message struct {
	From string
	To   []string
	Raw  []byte
}

// Server — фальшивый SMTP-сервер.
type Server struct {
	ln         net.Listener
	mu         sync.Mutex
	msgs       []Message
	down       bool
	rejectRcpt bool
	wg         sync.WaitGroup
}

// Start поднимает сервер; он закрывается в t.Cleanup.
func Start(t testing.TB) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("фальшивый SMTP: %v", err)
	}
	s := &Server{ln: ln}
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(func() {
		_ = ln.Close()
		s.wg.Wait()
	})
	return s
}

// Addr — адрес host:port.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// SetDown включает режим «сервис недоступен»: 421 на подключение и обрыв.
func (s *Server) SetDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

// RejectRecipients включает постоянный отказ на RCPT TO (550).
func (s *Server) RejectRecipients(reject bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejectRcpt = reject
}

// Messages возвращает копию принятых писем.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.msgs...)
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(conn)
		}()
	}
}

func (s *Server) state() (down, reject bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.down, s.rejectRcpt
}

func (s *Server) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	// Зависший клиент не должен держать остановку сервера в t.Cleanup.
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	tp := textproto.NewConn(conn)
	down, _ := s.state()
	if down {
		_ = tp.PrintfLine("421 4.3.2 Service not available")
		return
	}
	_ = tp.PrintfLine("220 smtptest ESMTP")
	var cur Message
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			_ = tp.PrintfLine("250 smtptest")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			cur = Message{From: between(line, "<", ">")}
			_ = tp.PrintfLine("250 2.1.0 Ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			if _, reject := s.state(); reject {
				_ = tp.PrintfLine("550 5.1.1 Mailbox unavailable")
				continue
			}
			cur.To = append(cur.To, between(line, "<", ">"))
			_ = tp.PrintfLine("250 2.1.5 Ok")
		case cmd == "DATA":
			_ = tp.PrintfLine("354 End data with <CR><LF>.<CR><LF>")
			data, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			cur.Raw = data
			s.mu.Lock()
			s.msgs = append(s.msgs, cur)
			s.mu.Unlock()
			_ = tp.PrintfLine("250 2.0.0 Ok: queued")
		case cmd == "RSET", cmd == "NOOP":
			_ = tp.PrintfLine("250 2.0.0 Ok")
		case cmd == "QUIT":
			_ = tp.PrintfLine("221 2.0.0 Bye")
			return
		default:
			_ = tp.PrintfLine("502 5.5.2 Command not recognized")
		}
	}
}

func between(s, open, closing string) string {
	i := strings.Index(s, open)
	j := strings.LastIndex(s, closing)
	if i < 0 || j <= i {
		return ""
	}
	return s[i+1 : j]
}

// Letter — разобранное письмо: заголовки, декодированная тема и части text/plain и text/html.
type Letter struct {
	Header   mail.Header
	Subject  string
	TextType string
	Text     string
	HTMLType string
	HTML     string
}

// Parse разбирает письмо multipart/alternative и декодирует части (quoted-printable снимает multipart.Reader).
func (m Message) Parse() (Letter, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(m.Raw))
	if err != nil {
		return Letter{}, fmt.Errorf("письмо: %w", err)
	}
	var dec mime.WordDecoder
	subject, err := dec.DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		return Letter{}, fmt.Errorf("тема: %w", err)
	}
	out := Letter{Header: msg.Header, Subject: subject}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		return Letter{}, fmt.Errorf("Content-Type: %w", err)
	}
	if mediaType != "multipart/alternative" {
		return Letter{}, fmt.Errorf("ожидалось multipart/alternative, получено %s", mediaType)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Letter{}, fmt.Errorf("часть письма: %w", err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			return Letter{}, fmt.Errorf("часть письма: %w", err)
		}
		text := strings.ReplaceAll(string(body), "\r\n", "\n")
		ctype := part.Header.Get("Content-Type")
		switch {
		case strings.HasPrefix(ctype, "text/plain"):
			out.TextType, out.Text = ctype, text
		case strings.HasPrefix(ctype, "text/html"):
			out.HTMLType, out.HTML = ctype, text
		}
	}
	return out, nil
}
