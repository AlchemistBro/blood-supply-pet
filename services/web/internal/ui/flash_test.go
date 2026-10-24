package ui

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestFlashSurvivesRedirectOnce(t *testing.T) {
	// POST: обработчик кладёт уведомление и перенаправляет.
	post := httptest.NewRecorder()
	want := []Flash{
		{Tone: ToneSuccess, Text: "Закреплено 2 единицы. Готовьте к отправке."},
		{Tone: ToneWarning, Text: "Закреплено 2 из 3. На оставшиеся 1 подбор предложит следующий источник."},
	}
	SetFlash(post, true, want...)
	cookies := post.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "__Host-bs_flash" {
		t.Fatalf("cookies %+v", cookies)
	}
	c := cookies[0]
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" || c.MaxAge <= 0 {
		// Префикс __Host- браузер принимает только с Secure, Path=/ и без Domain.
		t.Fatalf("атрибуты cookie: %+v", c)
	}

	// GET после перенаправления: уведомления читаются и cookie удаляется.
	req := httptest.NewRequest(http.MethodGet, "/operator/tasks", nil)
	req.AddCookie(c)
	get := httptest.NewRecorder()
	got := TakeFlashes(get, req, true)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	del := get.Result().Cookies()
	if len(del) != 1 || del[0].Name != FlashCookieNameSecure || del[0].MaxAge >= 0 {
		t.Fatalf("cookie должна удаляться после показа: %+v", del)
	}
}

func TestTakeFlashesWithoutCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	if got := TakeFlashes(rec, httptest.NewRequest(http.MethodGet, "/", nil), true); got != nil {
		t.Fatalf("got %+v", got)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("без уведомлений cookie не трогается")
	}
}

func TestTakeFlashesRejectsGarbageAndUnknownTone(t *testing.T) {
	for _, value := range []string{"%%%", "bm90LWpzb24", encodeFlashes([]Flash{{Tone: "evil", Text: "x"}})} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: FlashCookieNameSecure, Value: value})
		if got := TakeFlashes(httptest.NewRecorder(), req, true); len(got) != 0 {
			t.Errorf("значение %q: got %+v", value, got)
		}
	}
}

func TestSetFlashLimitsSize(t *testing.T) {
	long := strings.Repeat("я", 1000)
	rec := httptest.NewRecorder()
	SetFlash(rec, false, Flash{ToneInfo, long}, Flash{ToneInfo, "2"}, Flash{ToneInfo, "3"}, Flash{ToneInfo, "4"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(rec.Result().Cookies()[0])
	got := TakeFlashes(httptest.NewRecorder(), req, false)
	if len(got) != maxFlashes {
		t.Fatalf("уведомлений %d, ожидалось не больше %d", len(got), maxFlashes)
	}
	if n := len([]rune(got[0].Text)); n != maxFlashRunes {
		t.Fatalf("длина %d, ожидалось %d", n, maxFlashRunes)
	}
	if len(rec.Result().Cookies()[0].Value) > 3000 {
		t.Fatal("cookie не должна приближаться к пределу браузера 4 КБ")
	}
}

func TestFlashCookieNameDependsOnSecure(t *testing.T) {
	rec := httptest.NewRecorder()
	SetFlash(rec, false, Flash{Tone: ToneInfo, Text: "x"})
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].Name != "bs_flash" || c[0].Secure {
		t.Fatalf("без Secure префикс __Host- недопустим: %+v", c)
	}

	// Cookie без префикса не читается, когда включён Secure: её мог поставить поддомен.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: FlashCookieName, Value: encodeFlashes([]Flash{{Tone: ToneInfo, Text: "подложено"}})})
	if got := TakeFlashes(httptest.NewRecorder(), req, true); got != nil {
		t.Fatalf("got %+v", got)
	}
}
