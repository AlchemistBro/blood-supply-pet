package ui

import "strings"

// CSRFField — имя скрытого поля с CSRF-токеном сессии в формах (ADR-0018).
const CSRFField = "csrf_token"

// FieldKind — вид поля формы.
type FieldKind string

// Виды полей.
const (
	KindText          FieldKind = "text"
	KindNumber        FieldKind = "number"
	KindDate          FieldKind = "date"
	KindDateTimeLocal FieldKind = "datetime-local"
	KindSearch        FieldKind = "search"
	KindTextarea      FieldKind = "textarea"
	KindSelect        FieldKind = "select"
	KindCheckbox      FieldKind = "checkbox"
)

// Field — поле формы: подпись над полем, обязательные помечены «*», ошибка под полем (style-guide.md).
// Обязательность передаётся aria-required, а не атрибутом required: проверяет сервер и отвечает по-русски
// одинаково с JS и без него.
type Field struct {
	ID           string
	Name         string
	Label        string
	Kind         FieldKind // по умолчанию текст
	Value        string
	Checked      bool
	Required     bool
	Hint         string
	Error        string
	Options      []Option
	Autocomplete string
}

// Option — вариант выпадающего списка.
type Option struct {
	Value    string
	Label    string
	Selected bool
}

// HiddenField — скрытое поле формы.
type HiddenField struct {
	Name  string
	Value string
}

// InputType — тип для <input>.
func (f Field) InputType() string {
	switch f.Kind {
	case KindNumber, KindDate, KindDateTimeLocal, KindSearch:
		return string(f.Kind)
	default:
		return string(KindText)
	}
}

// IsCheckbox — флажок.
func (f Field) IsCheckbox() bool { return f.Kind == KindCheckbox }

// IsSelect — выпадающий список.
func (f Field) IsSelect() bool { return f.Kind == KindSelect }

// IsTextarea — многострочное поле.
func (f Field) IsTextarea() bool { return f.Kind == KindTextarea }

// CheckboxValue — значение флажка при отправке.
func (f Field) CheckboxValue() string {
	if f.Value == "" {
		return "on"
	}
	return f.Value
}

// DescribedBy — id подсказки и ошибки для aria-describedby.
func (f Field) DescribedBy() string {
	var ids []string
	if f.Hint != "" {
		ids = append(ids, f.ID+"-hint")
	}
	if f.Error != "" {
		ids = append(ids, f.ID+"-error")
	}
	return strings.Join(ids, " ")
}

// FieldError — ошибка поля или формы целиком (Field пустой — ошибка без поля, например ответ сервиса).
type FieldError struct {
	Field   string // id поля: ссылка из сводки ведёт к нему
	Label   string
	Message string
}

// FormErrors — ошибки формы: сводка сверху и сообщения под полями.
type FormErrors struct {
	Items []FieldError
}

// Add добавляет ошибку.
func (e *FormErrors) Add(field, label, message string) {
	e.Items = append(e.Items, FieldError{Field: field, Label: label, Message: message})
}

// Any — есть ли ошибки.
func (e FormErrors) Any() bool { return len(e.Items) > 0 }

// For — первая ошибка поля.
func (e FormErrors) For(field string) string {
	for _, it := range e.Items {
		if it.Field != "" && it.Field == field {
			return it.Message
		}
	}
	return ""
}

// Apply проставляет ошибки в поля по их ID.
func (e FormErrors) Apply(fields ...*Field) {
	for _, f := range fields {
		f.Error = e.For(f.ID)
	}
}

// Confirm — подтверждение опасного или значимого действия. Без JS ссылка ведёт на FallbackURL — обычную
// страницу с той же формой (ConfirmPage); с JS app.js открывает <dialog> на месте. Отправка — всегда обычный POST.
type Confirm struct {
	ID           string // id диалога; заголовок — ID + "-title"
	TriggerLabel string // «Отказать…»
	Question     string // заголовок-вопрос
	Consequence  string // краткое последствие
	Action       string // адрес POST
	ConfirmLabel string // по умолчанию «Подтвердить»
	CancelLabel  string // по умолчанию «Отмена»
	CancelURL    string // куда вернуться со страницы-фолбэка
	FallbackURL  string // GET-страница подтверждения без JS
	Danger       bool   // красная кнопка: отказ, отмена
	CSRFToken    string
	Hidden       []HiddenField
	Fields       []Field // например, причина отказа
}

// ConfirmText — подпись кнопки подтверждения.
func (c Confirm) ConfirmText() string {
	if c.ConfirmLabel == "" {
		return "Подтвердить"
	}
	return c.ConfirmLabel
}

// CancelText — подпись кнопки отмены.
func (c Confirm) CancelText() string {
	if c.CancelLabel == "" {
		return "Отмена"
	}
	return c.CancelLabel
}

// confirmView — форма подтверждения в диалоге или на отдельной странице.
type confirmView struct {
	Confirm
	InDialog bool
}
