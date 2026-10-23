package ui

import (
	"net/url"
	"reflect"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestParseSortAllowsOnlyKnownKeys(t *testing.T) {
	def := Sort{Key: "deadline", Dir: SortAsc}
	allowed := []string{"deadline", "status"}
	cases := []struct {
		query string
		want  Sort
	}{
		{"", def},
		{"sort=status", Sort{Key: "status", Dir: SortAsc}},
		{"sort=status&dir=desc", Sort{Key: "status", Dir: SortDesc}},
		{"sort=status&dir=sideways", Sort{Key: "status", Dir: SortAsc}},
		{"sort=patient_case_no&dir=desc", def}, // неизвестный ключ не уходит в запрос к сервису
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.query)
		if got := ParseSort(q, allowed, def); got != c.want {
			t.Errorf("ParseSort(%q) = %+v, want %+v", c.query, got, c.want)
		}
	}
}

func TestSortColumnsBuildsToggleLinksAndKeepsFilters(t *testing.T) {
	base := mustURL(t, "/operator/tasks?status=PENDING&page=3&sort=deadline&dir=asc")
	cols := []Column{
		{Key: "deadline", Label: "Срок", Sortable: true},
		{Key: "qty", Label: "Количество, ед.", Numeric: true, Sortable: true},
		{Key: "recipient", Label: "Получатель"},
	}
	got := SortColumns(cols, base, Sort{Key: "deadline", Dir: SortAsc})

	if got[0].SortDir != SortAsc || got[0].SortURL != "/operator/tasks?dir=desc&sort=deadline&status=PENDING" {
		t.Errorf("текущая колонка переключается на убывание и сбрасывает страницу: %+v", got[0])
	}
	if got[1].SortDir != SortNone || got[1].SortURL != "/operator/tasks?dir=asc&sort=qty&status=PENDING" {
		t.Errorf("другая колонка сортируется по возрастанию: %+v", got[1])
	}
	if got[2].SortURL != "" {
		t.Errorf("несортируемая колонка без ссылки: %+v", got[2])
	}
	if cols[0].SortURL != "" {
		t.Error("исходный срез колонок не меняется")
	}
}

func TestSortDirAriaAndArrow(t *testing.T) {
	cases := []struct {
		dir         SortDir
		aria, arrow string
	}{
		{SortAsc, "ascending", "▲"},
		{SortDesc, "descending", "▼"},
		{SortNone, "", "↕"},
	}
	for _, c := range cases {
		if c.dir.Aria() != c.aria || c.dir.Arrow() != c.arrow {
			t.Errorf("%q: aria %q arrow %q", c.dir, c.dir.Aria(), c.dir.Arrow())
		}
	}
}

func TestParsePage(t *testing.T) {
	cases := map[string]int{"": 1, "page=0": 1, "page=-4": 1, "page=abc": 1, "page=7": 7}
	for raw, want := range cases {
		q, _ := url.ParseQuery(raw)
		if got := ParsePage(q); got != want {
			t.Errorf("ParsePage(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestNewPaginationWindow(t *testing.T) {
	base := mustURL(t, "/operator/tasks?status=PENDING&sort=deadline&dir=asc&page=10")
	p := NewPagination(base, 10, 20, 400)

	if p.Page != 10 || p.Pages != 20 || p.From != 181 || p.To != 200 || p.Total != 400 {
		t.Fatalf("got %+v", p)
	}
	type link struct {
		Label   string
		Query   string
		Current bool
		Gap     bool
		Rel     string
	}
	var got []link
	for _, l := range p.Links {
		q := ""
		if l.URL != "" {
			q = mustURL(t, l.URL).RawQuery
		}
		got = append(got, link{l.Label, q, l.Current, l.Gap, l.Rel})
	}
	want := []link{
		{"Назад", "dir=asc&page=9&sort=deadline&status=PENDING", false, false, "prev"},
		{"1", "dir=asc&sort=deadline&status=PENDING", false, false, ""},
		{"…", "", false, true, ""},
		{"8", "dir=asc&page=8&sort=deadline&status=PENDING", false, false, ""},
		{"9", "dir=asc&page=9&sort=deadline&status=PENDING", false, false, ""},
		{"10", "dir=asc&page=10&sort=deadline&status=PENDING", true, false, ""},
		{"11", "dir=asc&page=11&sort=deadline&status=PENDING", false, false, ""},
		{"12", "dir=asc&page=12&sort=deadline&status=PENDING", false, false, ""},
		{"…", "", false, true, ""},
		{"20", "dir=asc&page=20&sort=deadline&status=PENDING", false, false, ""},
		{"Далее", "dir=asc&page=11&sort=deadline&status=PENDING", false, false, "next"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ссылки:\n got %+v\nwant %+v", got, want)
	}
}

func TestNewPaginationFillsSmallGapWithNumber(t *testing.T) {
	// Между 1 и 3 одна страница: показывается номер «2», а не многоточие.
	p := NewPagination(mustURL(t, "/x"), 5, 10, 70)
	var labels []string
	for _, l := range p.Links {
		labels = append(labels, l.Label)
	}
	want := []string{"Назад", "1", "2", "3", "4", "5", "6", "7", "Далее"}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("got %v, want %v", labels, want)
	}
}

func TestNewPaginationClampsAndSinglePage(t *testing.T) {
	p := NewPagination(mustURL(t, "/x"), 99, 20, 35)
	if p.Page != 2 || p.From != 21 || p.To != 35 {
		t.Fatalf("страница за пределами — последняя: %+v", p)
	}
	one := NewPagination(mustURL(t, "/x"), 1, 20, 5)
	if len(one.Links) != 0 || one.From != 1 || one.To != 5 {
		t.Fatalf("одна страница — без ссылок: %+v", one)
	}
	empty := NewPagination(mustURL(t, "/x"), 1, 20, 0)
	if empty.Total != 0 || len(empty.Links) != 0 || empty.From != 0 || empty.To != 0 {
		t.Fatalf("пусто: %+v", empty)
	}
}

func TestTablePagerCarriesTarget(t *testing.T) {
	p := NewPagination(mustURL(t, "/x"), 1, 10, 30)
	tbl := Table{ID: "tasks", Pagination: p}
	if got := tbl.Pager(); got == nil || got.Target != "tasks" {
		t.Fatalf("got %+v", got)
	}
	if p.Target != "" {
		t.Fatal("исходная пагинация не меняется")
	}
	if (Table{}).Pager() != nil {
		t.Fatal("без пагинации — nil")
	}
}

func TestClampPage(t *testing.T) {
	cases := []struct{ page, perPage, total, want int }{
		{1, 20, 0, 1},
		{0, 20, 100, 1},
		{3, 20, 100, 3},
		{9, 20, 100, 5},
		{2, 0, 3, 2}, // perPage < 1 считается как 1
	}
	for _, c := range cases {
		if got := ClampPage(c.page, c.perPage, c.total); got != c.want {
			t.Errorf("ClampPage(%d, %d, %d) = %d, want %d", c.page, c.perPage, c.total, got, c.want)
		}
	}
}
