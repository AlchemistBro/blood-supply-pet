// Package phenotype разбирает синтаксис маркеров и проверяет включение требований.
// Он не определяет клиническую совместимость и не выполняет ввод-вывод.
package phenotype

import (
	"errors"
	"strings"
)

type Markers map[string]bool

func Parse(s string) (Markers, error) {
	p := Markers{}
	var name strings.Builder
	for _, ch := range s {
		switch {
		case ch == ' ' || ch == ',' || ch == ';':
			if name.Len() > 0 {
				return nil, errors.New("маркер фенотипа без знака")
			}
		case ch == '+' || ch == '-':
			if name.Len() == 0 {
				return nil, errors.New("знак фенотипа без маркера")
			}
			marker, present := name.String(), ch == '+'
			if previous, ok := p[marker]; ok && previous != present {
				return nil, errors.New("противоречивые требования фенотипа")
			}
			p[marker] = present
			name.Reset()
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			name.WriteRune(ch)
		default:
			return nil, errors.New("недопустимый символ в записи фенотипа")
		}
	}
	if name.Len() > 0 {
		return nil, errors.New("маркер фенотипа без знака")
	}
	return p, nil
}
func (p Markers) Satisfies(requirements Markers) bool {
	for marker, required := range requirements {
		got, ok := p[marker]
		if !ok || got != required {
			return false
		}
	}
	return true
}

// Matches сохраняет точное сравнение старых неструктурированных записей.
func Matches(actual, required string) bool {
	if actual == required && !strings.ContainsAny(required, "+-") {
		return true
	}
	want, err := Parse(required)
	if err != nil {
		return false
	}
	got, err := Parse(actual)
	return err == nil && got.Satisfies(want)
}
