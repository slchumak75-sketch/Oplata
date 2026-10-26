package model

import "testing"

func TestDateOps(t *testing.T) {
	d := Date("2025-01-31")
	if got := d.AddDays(1); got != "2025-02-01" {
		t.Fatalf("AddDays=%s", got)
	}
	if got := Date("2025-01-10").DaysUntil(Date("2025-02-01")); got != 22 {
		t.Fatalf("DaysUntil=%d", got)
	}
	if !Date("2024-02-29").Valid() || Date("2025-02-29").Valid() {
		t.Fatal("високосный год")
	}
	if _, err := ParseDate("31.01.2025"); err == nil {
		t.Fatal("ParseDate должен отвергать не-ISO формат")
	}
}

func TestCentsFormatting(t *testing.T) {
	if got := CentsToDecimal(123456); got != "1234.56" {
		t.Fatalf("CentsToDecimal=%q", got)
	}
	if got := CentsToDecimal(-5); got != "-0.05" {
		t.Fatalf("CentsToDecimal(-5)=%q", got)
	}
	if got := FloatToCents(1013.49); got != 101349 {
		t.Fatalf("FloatToCents=%d", got)
	}
	if got := FloatToCents(-117.375); got != -11738 { // math.Round: половина от нуля
		t.Fatalf("FloatToCents(-117.375)=%d", got)
	}
}

func TestSupplierPaymentModesAndManualHolidays(t *testing.T) {
	if got := (Supplier{DeferralDays: 14}).EffectiveDeferralDays(); got != 14 {
		t.Fatalf("обычная отсрочка=%d", got)
	}
	if got := (Supplier{DeferralDays: 14, Prepayment: true}).EffectiveDeferralDays(); got != 0 {
		t.Fatalf("предоплата должна отключать отсрочку, получено %d", got)
	}
	if got := (Supplier{DeferralDays: 14, OnRealization: true}).EffectiveDeferralDays(); got != 0 {
		t.Fatalf("реализация должна отключать отсрочку, получено %d", got)
	}

	db := NewEmptyDB()
	if db.Settings.Holidays == nil {
		t.Fatal("у новой базы holidays должен быть [], а не null")
	}
	db.Buyers = []Buyer{{ID: "b1", Name: "Покупатель"}}
	if err := db.Validate(); err != nil {
		t.Fatalf("валидный покупатель отклонён: %v", err)
	}
	db.Buyers = append(db.Buyers, Buyer{ID: "b2", Name: " покупатель "})
	if err := db.Validate(); err == nil {
		t.Fatal("дублирующееся наименование покупателя должно отвергаться")
	}

	db = NewEmptyDB()
	db.Suppliers = []Supplier{{ID: "s1", Name: "АВИДО", Prepayment: true, OnRealization: true}}
	if err := db.Validate(); err == nil {
		t.Fatal("одновременная предоплата и реализация должна отвергаться")
	}

	db = NewEmptyDB()
	db.Settings.Holidays = []Holiday{{Date: "2025-01-01", Name: "Новый год"}, {Date: "2025-01-01", Name: "Дубликат"}}
	if err := db.Validate(); err == nil {
		t.Fatal("дублирующаяся дата праздника должна отвергаться")
	}
	db.Settings.Holidays = []Holiday{{Date: "2025-02-29", Name: "Некорректная дата"}}
	if err := db.Validate(); err == nil {
		t.Fatal("некорректная дата праздника должна отвергаться")
	}

	db = NewEmptyDB()
	if db.Settings.WorkingDays == nil {
		t.Fatal("у новой базы working_days должен быть [], а не null")
	}
	db.Settings.WorkingDays = []Date{"2025-01-04", "2025-01-04"}
	if err := db.Validate(); err == nil {
		t.Fatal("дублирующийся рабочий день должен отвергаться")
	}
	db.Settings.WorkingDays = []Date{"2025-01-06"}
	db.Settings.Holidays = []Holiday{{Date: "2025-01-06", Name: "Конфликт"}}
	if err := db.Validate(); err == nil {
		t.Fatal("дата не может быть одновременно рабочим и праздничным днём")
	}
}

func TestValidateObligationVATBreakdown(t *testing.T) {
	db := NewEmptyDB()
	db.Suppliers = []Supplier{{ID: "s1", Name: "АВИДО"}}
	db.Obligations = []Obligation{{
		ID: "o1", SupplierID: "s1", DueDate: "2025-01-15", DeliveryDate: "2025-01-01",
		AmountCents: 12000, AmountExclVATCents: 10000, VATCents: 2000,
	}}
	if err := db.Validate(); err != nil {
		t.Fatalf("корректная разбивка НДС отклонена: %v", err)
	}
	db.Obligations[0].VATCents = 1900
	if err := db.Validate(); err == nil {
		t.Fatal("сумма с НДС, не равная сумме без НДС + НДС, должна отвергаться")
	}
	db.Obligations[0].AmountExclVATCents = 12100
	db.Obligations[0].VATCents = -100
	if err := db.Validate(); err == nil {
		t.Fatal("отрицательный НДС должен отвергаться")
	}
	// Без разбивки (импорт из Excel) обязательство по-прежнему валидно.
	db.Obligations[0].AmountExclVATCents = 0
	db.Obligations[0].VATCents = 0
	if err := db.Validate(); err != nil {
		t.Fatalf("обязательство без разбивки НДС отклонено: %v", err)
	}
}

func TestValidate(t *testing.T) {
	db := NewEmptyDB()
	db.Suppliers = []Supplier{{ID: "s1", Name: "АВИДО"}}
	db.Obligations = []Obligation{{ID: "o1", SupplierID: "s1", DueDate: "2025-01-10", AmountCents: 100}}
	if err := db.Validate(); err != nil {
		t.Fatalf("валидная база отклонена: %v", err)
	}
	db.Payments = []Payment{{ID: "p1", SupplierID: "unknown", Date: "2025-01-10", AmountCents: 1}}
	if err := db.Validate(); err == nil {
		t.Fatal("выплата с неизвестным поставщиком должна отвергаться")
	}
}
