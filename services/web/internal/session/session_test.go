package session

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
)

var t0 = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)

func newKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func newCodec(t *testing.T, clk clock.Clock, keys ...[]byte) *Codec {
	t.Helper()
	c, err := NewCodec(clk, keys...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type payload struct {
	User  string `json:"user"`
	Roles []string
}

func TestCodecRoundTrip(t *testing.T) {
	clk := clock.NewMutable(t0)
	c := newCodec(t, clk, newKey(t))
	val, err := c.Seal("bs_session", payload{User: "operator-c001", Roles: []string{"CLINIC_OPERATOR"}}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(val, "operator-c001") {
		t.Fatal("значение cookie должно быть зашифровано")
	}
	var got payload
	exp, err := c.Open("bs_session", val, &got)
	if err != nil {
		t.Fatal(err)
	}
	if got.User != "operator-c001" || len(got.Roles) != 1 || !exp.Equal(t0.Add(time.Hour)) {
		t.Fatalf("расшифровано неверно: %+v, срок %v", got, exp)
	}
}

// Подделанная cookie отклоняется: изменён любой байт шифртекста.
func TestCodecRejectsTamperedValue(t *testing.T) {
	c := newCodec(t, clock.NewMutable(t0), newKey(t))
	val, err := c.Seal("bs_session", payload{User: "u1"}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(val)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, len(raw) / 2, len(raw) - 1} {
		forged := bytes.Clone(raw)
		forged[i] ^= 0x01
		var got payload
		if _, err := c.Open("bs_session", base64.RawURLEncoding.EncodeToString(forged), &got); !errors.Is(err, ErrInvalid) {
			t.Fatalf("байт %d: ожидалась ErrInvalid, получено %v", i, err)
		}
	}
	for _, bad := range []string{"", "не base64!", "AAAA", strings.Repeat("A", 10)} {
		if _, err := c.Open("bs_session", bad, &payload{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: ожидалась ErrInvalid, получено %v", bad, err)
		}
	}
}

// Cookie, зашифрованная чужим ключом, отклоняется.
func TestCodecRejectsForeignKey(t *testing.T) {
	a := newCodec(t, clock.NewMutable(t0), newKey(t))
	b := newCodec(t, clock.NewMutable(t0), newKey(t))
	val, err := a.Seal("bs_session", payload{User: "u1"}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Open("bs_session", val, &payload{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ожидалась ErrInvalid, получено %v", err)
	}
}

// Значение одной cookie нельзя подставить в другую: имя входит в дополнительные данные AEAD.
func TestCodecBindsCookieName(t *testing.T) {
	c := newCodec(t, clock.NewMutable(t0), newKey(t))
	val, err := c.Seal("bs_auth", payload{User: "u1"}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Open("bs_session", val, &payload{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ожидалась ErrInvalid, получено %v", err)
	}
}

// Просроченная cookie отклоняется, даже если браузер её прислал.
func TestCodecRejectsExpired(t *testing.T) {
	clk := clock.NewMutable(t0)
	c := newCodec(t, clk, newKey(t))
	val, err := c.Seal("bs_session", payload{User: "u1"}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour + time.Second)
	if _, err := c.Open("bs_session", val, &payload{}); !errors.Is(err, ErrExpired) {
		t.Fatalf("ожидалась ErrExpired, получено %v", err)
	}
	if _, err := c.Seal("bs_session", payload{}, t0); err == nil {
		t.Fatal("нельзя выпустить уже просроченную cookie")
	}
}

// Ротация ключа: прежний ключ только расшифровывает, новые cookie шифруются текущим.
func TestCodecKeyRotation(t *testing.T) {
	clk := clock.NewMutable(t0)
	oldKey, newK := newKey(t), newKey(t)
	before := newCodec(t, clk, oldKey)
	val, err := before.Seal("bs_session", payload{User: "u1"}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	after := newCodec(t, clk, newK, oldKey)
	var got payload
	if _, err := after.Open("bs_session", val, &got); err != nil || got.User != "u1" {
		t.Fatalf("старая cookie должна читаться после ротации: %v", err)
	}
	fresh, err := after.Seal("bs_session", got, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := before.Open("bs_session", fresh, &payload{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("новые cookie должны шифроваться только текущим ключом")
	}
}

func TestNewCodecRejectsBadKeys(t *testing.T) {
	if _, err := NewCodec(clock.System{}); err == nil {
		t.Fatal("без ключа кодек не создаётся")
	}
	if _, err := NewCodec(clock.System{}, make([]byte, 16)); err == nil {
		t.Fatal("ключ короче 32 байт не принимается")
	}
}

func TestParseKey(t *testing.T) {
	k := newKey(t)
	for _, enc := range []string{
		base64.StdEncoding.EncodeToString(k), base64.RawStdEncoding.EncodeToString(k),
		base64.URLEncoding.EncodeToString(k), base64.RawURLEncoding.EncodeToString(k),
	} {
		got, err := ParseKey(enc)
		if err != nil || !bytes.Equal(got, k) {
			t.Fatalf("%q: %v", enc, err)
		}
	}
	if _, err := ParseKey(""); err == nil {
		t.Fatal("пустой ключ не принимается")
	}
	if _, err := ParseKey(base64.StdEncoding.EncodeToString(make([]byte, 31))); err == nil {
		t.Fatal("ключ не той длины не принимается")
	}
}

// ---------------------------------------------------------------- хранилище в cookie

func newStore(t *testing.T, clk clock.Clock) *Store {
	t.Helper()
	return NewStore(newCodec(t, clk, newKey(t)), "bs_session", true)
}

func requestWith(cookies []*http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range cookies {
		if c.MaxAge >= 0 {
			r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
	return r
}

func TestStoreSplitsLargeValueIntoChunks(t *testing.T) {
	clk := clock.NewMutable(t0)
	s := newStore(t, clk)
	big := payload{User: strings.Repeat("x", 9000)}
	rec := httptest.NewRecorder()
	if err := s.Save(rec, httptest.NewRequest(http.MethodGet, "/", nil), big, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) < 3 {
		t.Fatalf("значение больше 4 КБ должно делиться на части, частей %d", len(cookies))
	}
	for _, c := range cookies {
		if len(c.Value) > chunkSize {
			t.Fatalf("часть %s длиннее %d", c.Name, chunkSize)
		}
		if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
			t.Fatalf("атрибуты cookie %s: %+v", c.Name, c)
		}
		if c.MaxAge != 3600 {
			t.Fatalf("Max-Age должен совпадать со сроком сессии: %d", c.MaxAge)
		}
	}
	var got payload
	exp, err := s.Load(requestWith(cookies), &got)
	if err != nil {
		t.Fatal(err)
	}
	if got.User != big.User || !exp.Equal(t0.Add(time.Hour)) {
		t.Fatal("значение из частей собрано неверно")
	}
}

// Меньшая новая версия удаляет лишние части прежней.
func TestStoreSaveRemovesStaleChunks(t *testing.T) {
	clk := clock.NewMutable(t0)
	s := newStore(t, clk)
	rec := httptest.NewRecorder()
	if err := s.Save(rec, httptest.NewRequest(http.MethodGet, "/", nil), payload{User: strings.Repeat("x", 9000)}, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	old := rec.Result().Cookies()

	rec = httptest.NewRecorder()
	if err := s.Save(rec, requestWith(old), payload{User: "u1"}, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	removed := 0
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			removed++
		}
	}
	if removed != len(old)-1 {
		t.Fatalf("должны удаляться %d лишних частей, удалено %d", len(old)-1, removed)
	}
}

func TestStoreLoadErrors(t *testing.T) {
	clk := clock.NewMutable(t0)
	s := newStore(t, clk)
	if _, err := s.Load(httptest.NewRequest(http.MethodGet, "/", nil), &payload{}); !errors.Is(err, ErrNoSession) {
		t.Fatalf("без cookie ожидалась ErrNoSession, получено %v", err)
	}

	rec := httptest.NewRecorder()
	if err := s.Save(rec, httptest.NewRequest(http.MethodGet, "/", nil), payload{User: strings.Repeat("x", 9000)}, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	cookies := rec.Result().Cookies()
	// Пропала средняя часть — склейка даёт мусор, cookie отклоняется.
	partial := append([]*http.Cookie{cookies[0]}, cookies[2:]...)
	if _, err := s.Load(requestWith(partial), &payload{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("без средней части ожидалась ErrInvalid, получено %v", err)
	}
	// Подменена одна часть.
	forged := []*http.Cookie{{Name: cookies[0].Name, Value: strings.Repeat("A", len(cookies[0].Value))}}
	forged = append(forged, cookies[1:]...)
	if _, err := s.Load(requestWith(forged), &payload{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("с подменённой частью ожидалась ErrInvalid, получено %v", err)
	}
	clk.Advance(2 * time.Hour)
	if _, err := s.Load(requestWith(cookies), &payload{}); !errors.Is(err, ErrExpired) {
		t.Fatalf("после срока ожидалась ErrExpired, получено %v", err)
	}
}

func TestStoreRejectsTooLargeValue(t *testing.T) {
	s := newStore(t, clock.NewMutable(t0))
	err := s.Save(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil),
		payload{User: strings.Repeat("x", maxChunks*chunkSize)}, t0.Add(time.Hour))
	if err == nil {
		t.Fatal("значение больше предела частей не сохраняется")
	}
}

func TestStoreClearRemovesAllChunks(t *testing.T) {
	clk := clock.NewMutable(t0)
	s := newStore(t, clk)
	rec := httptest.NewRecorder()
	if err := s.Save(rec, httptest.NewRequest(http.MethodGet, "/", nil), payload{User: strings.Repeat("x", 9000)}, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	cookies := rec.Result().Cookies()
	rec = httptest.NewRecorder()
	s.Clear(rec, requestWith(cookies))
	cleared := rec.Result().Cookies()
	if len(cleared) != len(cookies) {
		t.Fatalf("удаляются все %d частей, удалено %d", len(cookies), len(cleared))
	}
	for _, c := range cleared {
		if c.MaxAge >= 0 || c.Value != "" {
			t.Fatalf("часть %s не удалена: %+v", c.Name, c)
		}
	}
}

// Данные сессии превращаются в участника для матрицы прав.
func TestDataPrincipal(t *testing.T) {
	clinic := int64(1)
	d := Data{Subject: "s1", Username: "operator-c001", Roles: []string{"CLINIC_OPERATOR"}, ClinicID: &clinic}
	p := d.Principal()
	if p.Username != "operator-c001" || p.PrimaryRole() != "CLINIC_OPERATOR" || p.ClinicID == nil || *p.ClinicID != 1 {
		t.Fatalf("участник собран неверно: %+v", p)
	}
}
