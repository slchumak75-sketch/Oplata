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

func TestBuyerUNP(t *testing.T) {
	if got := NormalizeUNP(" 123 456 789 "); got != "123456789" {
		t.Fatalf("NormalizeUNP=%q", got)
	}
	if err := ValidateUNP("123456789"); err != nil {
		t.Fatalf("валидный УНП отклонён: %v", err)
	}
	if err := ValidateUNP(""); err != nil {
		t.Fatalf("пустой УНП должен быть допустим: %v", err)
	}
	for _, bad := range []string{"123", "12345678!", "1234567890123456"} {
		if err := ValidateUNP(bad); err == nil {
			t.Fatalf("УНП %q должен отвергаться", bad)
		}
	}
	db := NewEmptyDB()
	db.Buyers = []Buyer{{ID: "b1", Name: "Покупатель", UNP: "123456789"}}
	if err := db.Validate(); err != nil {
		t.Fatalf("валидный покупатель отклонён: %v", err)
	}
	db.Buyers = append(db.Buyers, Buyer{ID: "b2", Name: "Другой", UNP: "123456789"})
	if err := db.Validate(); err == nil {
		t.Fatal("дублирующийся УНП покупателей должен отвергаться")
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
