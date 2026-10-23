package ui

import (
	"net/url"
	"slices"
	"strconv"
)

// Параметры запроса для сортировки и страниц. Сортировка и пагинация на сервере: ссылки работают без JS,
// htmx (hx-boost на заголовке и пагинации) лишь подменяет таблицу без перезагрузки страницы.
const (
	ParamSort = "sort"
	ParamDir  = "dir"
	ParamPage = "page"
)

// SortDir — направление сортировки колонки.
type SortDir string

// Направления.
const (
	SortNone SortDir = ""
	SortAsc  SortDir = "asc"
	SortDesc SortDir = "desc"
)

// Aria — значение aria-sort для текущей колонки.
func (d SortDir) Aria() string {
	switch d {
	case SortAsc:
		return "ascending"
	case SortDesc:
		return "descending"
	default:
		return ""
	}
}

// Arrow — стрелка в заголовке (только для глаз, состояние для экранного диктора — aria-sort).
func (d SortDir) Arrow() string {
	switch d {
	case SortAsc:
		return "▲"
	case SortDesc:
		return "▼"
	default:
		return "↕"
	}
}

// Sort — выбранная сортировка.
type Sort struct {
	Key string
	Dir SortDir
}

// ParseSort читает sort и dir из запроса. Ключ — только из allowed (белый список колонок), иначе def:
// произвольная строка из адреса не уходит в запрос к сервису.
func ParseSort(q url.Values, allowed []string, def Sort) Sort {
	key := q.Get(ParamSort)
	if key == "" || !slices.Contains(allowed, key) {
		return def
	}
	dir := SortAsc
	if q.Get(ParamDir) == string(SortDesc) {
		dir = SortDesc
	}
	return Sort{Key: key, Dir: dir}
}

// Table — таблица с сортировкой на сервере и пагинацией.
type Table struct {
	ID         string // id обёртки; с ним htmx подменяет только таблицу. Без него — обычные переходы
	Caption    string
	Columns    []Column
	Rows       []Row
	Empty      EmptyState // показывается вместо таблицы, когда строк нет
	Pagination *Pagination
}

// Column — колонка. Единицы измерения — в подписи: «Объём, мл».
type Column struct {
	Key      string
	Label    string
	Numeric  bool // числа выравниваются вправо
	Sortable bool
	SortURL  string  // заполняет SortColumns
	SortDir  SortDir // текущее направление, если таблица отсортирована по этой колонке
}

// Row — строка. ID нужен для точечного обновления строки (SSE, этап 2 WP4).
type Row struct {
	ID    string
	Cells []Cell
}

// Cell — ячейка: текст, при желании ссылка и бейджи (статус O07, «Частично: 2 из 3»).
type Cell struct {
	Text   string
	Href   string
	Badges []Badge
}

// IsNumeric — колонка i числовая (для выравнивания ячеек).
func (t Table) IsNumeric(i int) bool {
	return i >= 0 && i < len(t.Columns) && t.Columns[i].Numeric
}

// Pager — пагинация с целью htmx-подмены, равной ID таблицы; nil — без пагинации.
func (t Table) Pager() *Pagination {
	if t.Pagination == nil {
		return nil
	}
	p := *t.Pagination
	p.Target = t.ID
	return &p
}

// SortColumns возвращает копию колонок со ссылками сортировки: текущая колонка переключает направление,
// другая сортирует по возрастанию. Фильтры из base сохраняются, номер страницы сбрасывается.
func SortColumns(cols []Column, base *url.URL, current Sort) []Column {
	out := make([]Column, len(cols))
	for i, c := range cols {
		c.SortURL, c.SortDir = "", SortNone
		if c.Sortable {
			next := SortAsc
			if c.Key == current.Key {
				c.SortDir = current.Dir
				if current.Dir == SortAsc {
					next = SortDesc
				}
			}
			q := cloneQuery(base)
			q.Set(ParamSort, c.Key)
			q.Set(ParamDir, string(next))
			q.Del(ParamPage)
			c.SortURL = relativeURL(base, q)
		}
		out[i] = c
	}
	return out
}

// Pagination — страницы таблицы: «Показаны 21–40 из 135» и ссылки.
type Pagination struct {
	Page, Pages, PerPage, Total int
	From, To                    int // номера первой и последней записи на странице; 0, если записей нет
	Links                       []PageLink
	Target                      string // ID таблицы для htmx; ставит Table.Pager
}

// PageLink — ссылка пагинации или многоточие.
type PageLink struct {
	Label   string
	URL     string
	Current bool
	Gap     bool
	Rel     string // prev / next
}

// pageWindow — сколько соседних страниц показывать по обе стороны от текущей.
const pageWindow = 2

// ParsePage читает номер страницы; всё, что не положительное целое, — первая страница.
func ParsePage(q url.Values) int {
	n, err := strconv.Atoi(q.Get(ParamPage))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// NewPagination строит пагинацию: первая и последняя страницы, текущая ± pageWindow, многоточия на разрывах.
// Номер за пределами приводится к последней странице.
func NewPagination(base *url.URL, page, perPage, total int) *Pagination {
	if perPage < 1 {
		perPage = 1
	}
	if total < 0 {
		total = 0
	}
	pages := (total + perPage - 1) / perPage
	page = max(1, min(page, pages))
	p := &Pagination{Page: page, Pages: pages, PerPage: perPage, Total: total}
	if total == 0 {
		return p
	}
	p.From = (page-1)*perPage + 1
	p.To = min(page*perPage, total)
	if pages < 2 {
		return p
	}

	link := func(n int, label, rel string) PageLink {
		q := cloneQuery(base)
		if n == 1 {
			q.Del(ParamPage)
		} else {
			q.Set(ParamPage, strconv.Itoa(n))
		}
		return PageLink{Label: label, URL: relativeURL(base, q), Current: n == page && rel == "", Rel: rel}
	}

	if page > 1 {
		p.Links = append(p.Links, link(page-1, "Назад", "prev"))
	}
	var shown []int
	for n := 1; n <= pages; n++ {
		if n == 1 || n == pages || (n >= page-pageWindow && n <= page+pageWindow) {
			shown = append(shown, n)
		}
	}
	prev := 0
	for _, n := range shown {
		switch {
		case n-prev == 2:
			// Пропущена одна страница: её номер короче многоточия.
			p.Links = append(p.Links, link(prev+1, strconv.Itoa(prev+1), ""))
		case n-prev > 2:
			p.Links = append(p.Links, PageLink{Label: "…", Gap: true})
		}
		p.Links = append(p.Links, link(n, strconv.Itoa(n), ""))
		prev = n
	}
	if page < pages {
		p.Links = append(p.Links, link(page+1, "Далее", "next"))
	}
	return p
}

func cloneQuery(base *url.URL) url.Values {
	q := url.Values{}
	if base == nil {
		return q
	}
	for k, v := range base.Query() {
		q[k] = slices.Clone(v)
	}
	return q
}

// relativeURL — путь и запрос без схемы и хоста: ссылки остаются на своём источнике.
func relativeURL(base *url.URL, q url.Values) string {
	path := "/"
	if base != nil && base.Path != "" {
		path = base.Path
	}
	return (&url.URL{Path: path, RawQuery: q.Encode()}).String()
}

// EmptyState — пустое состояние: серый текст и одна кнопка действия (style-guide.md).
type EmptyState struct {
	Text        string // по умолчанию «Записей нет»
	ActionLabel string
	ActionURL   string
}

// Message — текст пустого состояния.
func (e EmptyState) Message() string {
	if e.Text == "" {
		return "Записей нет"
	}
	return e.Text
}

// Card — карточка «ключ — значение» (параметры задания и т. п.).
type Card struct {
	Title string
	Items []KV
}

// KV — строка карточки. Значение выводится как ячейка таблицы: текст, ссылка, бейджи.
type KV struct {
	Key     string
	Value   string
	Href    string
	Badges  []Badge
	Numeric bool
}

// Cell — значение строки карточки в виде ячейки.
func (kv KV) Cell() Cell {
	return Cell{Text: kv.Value, Href: kv.Href, Badges: kv.Badges}
}
