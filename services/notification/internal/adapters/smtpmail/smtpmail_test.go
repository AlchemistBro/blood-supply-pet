package smtpmail_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/adapters/smtpmail"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/adapters/smtpmail/smtptest"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/app"
)

const from = "no-reply@demo.blood-supply.local"

var now = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

func sender(t *testing.T, addr string) *smtpmail.Sender {
	t.Helper()
	s, err := smtpmail.New(smtpmail.Config{Addr: addr, From: from, Timeout: 3 * time.Second}, clock.NewMutable(now))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mail() app.Mail {
	return app.Mail{
		ID:      uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-400000000001"),
		To:      "transfusion.c001@demo.blood-supply.local",
		Subject: "[демо] Срочное задание на передачу № 0192f3a1",
		Text:    "Срочное задание на передачу (демо)\n\nЗадание: 0192f3a1-7b2c-7d4e-8f90-300000000001\n",
		HTML:    "<!DOCTYPE html>\n<p>Срочное задание на передачу</p>\n<p>0192f3a1-7b2c-7d4e-8f90-300000000001</p>\n",
	}
}

// Письмо уходит на SMTP-сервер: конверт, заголовки и обе части верны.
func TestSendDeliversMultipartLetter(t *testing.T) {
	srv := smtptest.Start(t)
	m := mail()
	if err := sender(t, srv.Addr()).Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	got := srv.Messages()
	if len(got) != 1 {
		t.Fatalf("принято писем %d, ожидалось 1", len(got))
	}
	if got[0].From != from || len(got[0].To) != 1 || got[0].To[0] != m.To {
		t.Fatalf("конверт %q → %v, ожидалось %q → %q", got[0].From, got[0].To, from, m.To)
	}
	l, err := got[0].Parse()
	if err != nil {
		t.Fatal(err)
	}
	h := l.Header
	if h.Get("From") != from || h.Get("To") != m.To {
		t.Fatalf("заголовки From/To: %q / %q", h.Get("From"), h.Get("To"))
	}
	if l.Subject != m.Subject {
		t.Fatalf("тема %q, ожидалась %q", l.Subject, m.Subject)
	}
	if h.Get("Message-ID") != "<"+m.ID.String()+"@demo.blood-supply.local>" {
		t.Fatalf("Message-ID %q", h.Get("Message-ID"))
	}
	if h.Get("MIME-Version") != "1.0" || h.Get("Auto-Submitted") != "auto-generated" {
		t.Fatalf("MIME-Version %q, Auto-Submitted %q", h.Get("MIME-Version"), h.Get("Auto-Submitted"))
	}
	if d, err := h.Date(); err != nil || !d.Equal(now) {
		t.Fatalf("Date %v (%v), ожидалось %v по часам сервиса", d, err, now)
	}
	if !strings.Contains(l.TextType, "charset=utf-8") || !strings.Contains(l.HTMLType, "charset=utf-8") {
		t.Fatalf("части без utf-8: %q, %q", l.TextType, l.HTMLType)
	}
	if strings.TrimSpace(l.Text) != strings.TrimSpace(m.Text) {
		t.Fatalf("text/plain:\n%s\nожидалось:\n%s", l.Text, m.Text)
	}
	if strings.TrimSpace(l.HTML) != strings.TrimSpace(m.HTML) {
		t.Fatalf("text/html:\n%s\nожидалось:\n%s", l.HTML, m.HTML)
	}
}

// Отказ адресату (550) — постоянная ошибка: повторять бессмысленно.
func TestRejectedRecipientIsPermanent(t *testing.T) {
	srv := smtptest.Start(t)
	srv.RejectRecipients(true)
	err := sender(t, srv.Addr()).Send(context.Background(), mail())
	if !errors.Is(err, app.ErrRejected) {
		t.Fatalf("ошибка %v, ожидалась app.ErrRejected", err)
	}
	if len(srv.Messages()) != 0 {
		t.Fatal("отклонённое письмо не должно быть принято")
	}
}

// Сервис «лежит» (421) или порт закрыт — временная ошибка: письмо подождёт следующей попытки.
func TestUnavailableServerIsTemporary(t *testing.T) {
	srv := smtptest.Start(t)
	srv.SetDown(true)
	err := sender(t, srv.Addr()).Send(context.Background(), mail())
	if err == nil || errors.Is(err, app.ErrRejected) {
		t.Fatalf("421: ошибка %v, ожидалась временная", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	_ = ln.Close()
	err = sender(t, closed).Send(context.Background(), mail())
	if err == nil || errors.Is(err, app.ErrRejected) {
		t.Fatalf("закрытый порт: ошибка %v, ожидалась временная", err)
	}
}

// Отправитель и адресат — только голые адреса: имя в заголовок не попадает, перевод строки не внедрить.
func TestAddressesMustBeBare(t *testing.T) {
	if _, err := smtpmail.New(smtpmail.Config{Addr: "127.0.0.1:1025", From: "Уведомления <no-reply@demo.blood-supply.local>"}, clock.System{}); err == nil {
		t.Fatal("отправитель с именем принят")
	}
	if _, err := smtpmail.New(smtpmail.Config{Addr: "", From: from}, clock.System{}); err == nil {
		t.Fatal("пустой адрес SMTP принят")
	}
	srv := smtptest.Start(t)
	m := mail()
	m.To = "x@demo.blood-supply.local\r\nBcc: leak@example.org"
	if err := sender(t, srv.Addr()).Send(context.Background(), m); !errors.Is(err, app.ErrRejected) {
		t.Fatalf("адресат с переводом строки: %v, ожидалась постоянная ошибка", err)
	}
	if len(srv.Messages()) != 0 {
		t.Fatal("письмо с испорченным адресатом не должно уйти")
	}
}
