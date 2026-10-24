package aistclient_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/aistclient"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/fakeaist"
)

var t0 = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

// Ошибка вызова — вид отказа без адреса: в адресе точечного статуса стоят исходные коды единиц (ADR-0010).
func TestTimeoutErrorHasNoURLOrCodes(t *testing.T) {
	fake := fakeaist.New(t, t0)
	fake.Add(fakeaist.Unit{AistUnitCode: "SECRET-CODE-42"})
	fake.SetSlow(5 * time.Second)
	c := aistclient.New(fake.URL, aistclient.Options{StatusTimeout: 100 * time.Millisecond})
	_, err := c.Statuses(context.Background(), []string{"SECRET-CODE-42"})
	var f *app.AistFailure
	if !errors.As(err, &f) || f.Kind != app.AistTimeout {
		t.Fatalf("ожидали отказ timeout, получили %v", err)
	}
	if text := fmt.Sprintf("%v %+v", err, err); strings.Contains(text, "SECRET-CODE") || strings.Contains(text, fake.URL) {
		t.Fatalf("текст ошибки раскрывает адрес или коды: %s", text)
	}
}

func TestUnavailableAndBadRequestAreClassified(t *testing.T) {
	fake := fakeaist.New(t, t0)
	c := aistclient.New(fake.URL, aistclient.Options{})
	var f *app.AistFailure
	if _, err := c.Changes(context.Background(), 0, 6000); !errors.As(err, &f) || f.Kind != app.AistHTTPStatus || f.HTTPStatus != 400 {
		t.Fatalf("limit 6000: %v", err)
	}
	fake.SetDown(true)
	if _, err := c.Changes(context.Background(), 0, 10); !errors.As(err, &f) || f.Kind == app.AistHTTPStatus {
		t.Fatalf("недоступная АИСТ-М: %v", err)
	}
}

// Больше 1000 кодов делится на пачки: АИСТ-М принимает не больше 1000 за запрос.
func TestStatusesAreBatchedByThousand(t *testing.T) {
	fake := fakeaist.New(t, t0)
	codes := make([]string, 1500)
	for i := range codes {
		codes[i] = fmt.Sprintf("U-%04d", i)
	}
	for _, code := range codes[:3] {
		fake.Add(fakeaist.Unit{AistUnitCode: code})
	}
	c := aistclient.New(fake.URL, aistclient.Options{})
	page, err := c.Statuses(context.Background(), codes)
	if err != nil {
		t.Fatal(err)
	}
	if fake.StatusCalls() != 2 || len(page.Units) != 3 || len(page.NotFound) != 1497 {
		t.Fatalf("пачки: вызовов %d, найдено %d, не найдено %d", fake.StatusCalls(), len(page.Units), len(page.NotFound))
	}
}
