package ui

import "testing"

func TestFormErrorsCollectAndApply(t *testing.T) {
	var errs FormErrors
	if errs.Any() {
		t.Fatal("пустой набор")
	}
	errs.Add("reason", "Причина отказа", "Укажите причину")
	errs.Add("", "", "АИСТ-М не ответила. Задание остаётся в ожидании, повторите через минуту.")
	if !errs.Any() || errs.For("reason") != "Укажите причину" || errs.For("other") != "" {
		t.Fatalf("got %+v", errs)
	}

	reason := Field{ID: "reason", Name: "reason", Label: "Причина отказа"}
	qty := Field{ID: "qty", Name: "qty", Label: "Количество"}
	errs.Apply(&reason, &qty)
	if reason.Error != "Укажите причину" || qty.Error != "" {
		t.Fatalf("reason %q qty %q", reason.Error, qty.Error)
	}
}

func TestFieldDescribedBy(t *testing.T) {
	cases := []struct {
		f    Field
		want string
	}{
		{Field{ID: "a"}, ""},
		{Field{ID: "a", Hint: "подсказка"}, "a-hint"},
		{Field{ID: "a", Error: "ошибка"}, "a-error"},
		{Field{ID: "a", Hint: "подсказка", Error: "ошибка"}, "a-hint a-error"},
	}
	for _, c := range cases {
		if got := c.f.DescribedBy(); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.f, got, c.want)
		}
	}
}

func TestFieldKinds(t *testing.T) {
	if (Field{}).InputType() != "text" {
		t.Error("по умолчанию — текстовое поле")
	}
	if (Field{Kind: KindNumber}).InputType() != "number" {
		t.Error("number")
	}
	if !(Field{Kind: KindCheckbox}).IsCheckbox() || !(Field{Kind: KindSelect}).IsSelect() || !(Field{Kind: KindTextarea}).IsTextarea() {
		t.Error("виды полей")
	}
	if (Field{Kind: KindCheckbox}).CheckboxValue() != "on" || (Field{Kind: KindCheckbox, Value: "1"}).CheckboxValue() != "1" {
		t.Error("значение флажка")
	}
}

func TestConfirmDefaults(t *testing.T) {
	c := Confirm{}
	if c.ConfirmText() != "Подтвердить" || c.CancelText() != "Отмена" {
		t.Fatalf("got %q %q", c.ConfirmText(), c.CancelText())
	}
	c = Confirm{ConfirmLabel: "Отказать", CancelLabel: "Вернуться"}
	if c.ConfirmText() != "Отказать" || c.CancelText() != "Вернуться" {
		t.Fatalf("got %q %q", c.ConfirmText(), c.CancelText())
	}
}
