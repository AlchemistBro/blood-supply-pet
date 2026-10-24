// Package smtpmail — адаптер почтового сервиса C04 (ИНТ-04) на net/smtp: письмо multipart/alternative
// (text/plain и text/html, UTF-8, quoted-printable). В демо Mailpit без TLS; STARTTLS включается конфигурацией.
//
// Ошибки делятся на постоянные (код 5xx, испорченный адрес — app.ErrRejected) и временные (нет соединения,
// таймаут, код 4xx): по временным письмо остаётся PENDING и ждёт следующей попытки.
package smtpmail

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/app"
)

// Config — параметры SMTP.
type Config struct {
	// Addr — host:port сервера (BS_SMTP_ADDR), в демо mailpit:1025.
	Addr string
	// From — адрес отправителя без имени (BS_SMTP_FROM).
	From string
	// Timeout — предел на всю отправку одного письма.
	Timeout time.Duration
	// StartTLS — требовать STARTTLS (BS_SMTP_STARTTLS); в демо Mailpit без TLS.
	StartTLS bool
	// HelloName — имя в EHLO.
	HelloName string
}

// Sender реализует app.Mailer.
type Sender struct {
	cfg    Config
	host   string
	domain string
	clock  clock.Clock
}

// New проверяет конфигурацию.
func New(cfg Config, clk clock.Clock) (*Sender, error) {
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host == "" {
		return nil, fmt.Errorf("адрес SMTP %q: нужен host:port", cfg.Addr)
	}
	if err := bare(cfg.From); err != nil {
		return nil, fmt.Errorf("отправитель: %w", err)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.HelloName == "" {
		cfg.HelloName = "notification"
	}
	domain := cfg.From[strings.LastIndex(cfg.From, "@")+1:]
	return &Sender{cfg: cfg, host: host, domain: domain, clock: clk}, nil
}

// Send отправляет письмо. Сетевой вызов идёт вне транзакций БД (вызывает отправщик app).
func (s *Sender) Send(ctx context.Context, m app.Mail) error {
	if err := bare(m.To); err != nil {
		return fmt.Errorf("%w: адресат: %w", app.ErrRejected, err)
	}
	msg, err := s.build(m)
	if err != nil {
		return fmt.Errorf("%w: сборка письма: %w", app.ErrRejected, err)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("соединение с SMTP: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	// Отмена контекста обрывает соединение: зависший сервер не держит отправщика.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		_ = conn.Close()
		return classify("приветствие", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello(s.cfg.HelloName); err != nil {
		return classify("EHLO", err)
	}
	if s.cfg.StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("SMTP: сервер не предлагает STARTTLS, а он обязателен по конфигурации")
		}
		if err := c.StartTLS(&tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}); err != nil {
			return classify("STARTTLS", err)
		}
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return classify("MAIL FROM", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return classify("RCPT TO", err)
	}
	w, err := c.Data()
	if err != nil {
		return classify("DATA", err)
	}
	if _, err := w.Write(msg); err != nil {
		return classify("DATA", err)
	}
	if err := w.Close(); err != nil {
		return classify("DATA", err)
	}
	// Письмо принято сервером; сбой на QUIT не повод отправлять его повторно.
	_ = c.Quit()
	return nil
}

// build собирает письмо: заголовки и две части. Граница и Message-ID производны от идентификатора записи:
// повтор после сбоя даёт то же письмо, почтовый клиент распознает дубль по Message-ID.
func (s *Sender) build(m app.Mail) ([]byte, error) {
	var b bytes.Buffer
	boundary := "bs-" + strings.ReplaceAll(m.ID.String(), "-", "")
	header := [][2]string{
		{"From", s.cfg.From},
		{"To", m.To},
		{"Subject", mime.BEncoding.Encode("utf-8", m.Subject)},
		{"Date", s.clock.Now().Format(time.RFC1123Z)},
		{"Message-ID", "<" + m.ID.String() + "@" + s.domain + ">"},
		{"MIME-Version", "1.0"},
		{"Content-Type", `multipart/alternative; boundary="` + boundary + `"`},
		// RFC 3834: письмо автоматическое, автоответчики на него не отвечают.
		{"Auto-Submitted", "auto-generated"},
	}
	for _, h := range header {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	b.WriteString("\r\n")
	mw := multipart.NewWriter(&b)
	if err := mw.SetBoundary(boundary); err != nil {
		return nil, err
	}
	parts := []struct{ ctype, body string }{
		{"text/plain; charset=utf-8", m.Text},
		{"text/html; charset=utf-8", m.HTML},
	}
	for _, p := range parts {
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {p.ctype},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(pw)
		if _, err := qp.Write([]byte(p.body)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// classify отделяет постоянный отказ (5xx) от временного сбоя.
func classify(step string, err error) error {
	var tp *textproto.Error
	if errors.As(err, &tp) && tp.Code >= 500 {
		return fmt.Errorf("%w: %s: %d %s", app.ErrRejected, step, tp.Code, tp.Msg)
	}
	return fmt.Errorf("SMTP %s: %w", step, err)
}

// bare пропускает только голый адрес (addr-spec): без имени, угловых скобок и переводов строки.
func bare(addr string) error {
	a, err := mail.ParseAddress(addr)
	if err != nil {
		return fmt.Errorf("%q не адрес почты: %w", addr, err)
	}
	if a.Name != "" || a.Address != addr {
		return fmt.Errorf("%q: нужен адрес без имени", addr)
	}
	return nil
}
