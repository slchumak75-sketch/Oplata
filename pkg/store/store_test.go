package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/slchumak75-sketch/oplata/pkg/model"
)

// TestOpenCreatesEmptyDB фиксирует требование владельца: в программе нет
// встроенных сумм из Excel, поэтому первый запуск всегда создаёт пустую базу.
func TestOpenCreatesEmptyDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "data.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db := st.Snapshot()
	if db.Version != model.SchemaVersion {
		t.Fatalf("версия=%d, ожидалось %d", db.Version, model.SchemaVersion)
	}
	if len(db.Suppliers) != 0 || len(db.Obligations) != 0 || len(db.Payments) != 0 || len(db.Buyers) != 0 || len(db.Warnings) != 0 {
		t.Fatalf("первая база должна быть пустой: %+v", db)
	}
	if db.Settings.SeededFrom != "" {
		t.Fatalf("seeded_from=%q, ожидалось пусто", db.Settings.SeededFrom)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("файл пустой базы не создан: %v", err)
	}
}

// TestDecodeMigratesV1: файл формата v1 (без buyers и unp) открывается как v3.
func TestDecodeMigratesV1(t *testing.T) {
	v1 := []byte(`{"version":1,"settings":{"company_name":"Тест","currency_label":"руб.","warn_days":3},
		"suppliers":[{"id":"s1","name":"АВИДО","deferral_days":0,"note":"","archived":false}],
		"obligations":[],"payments":[],"warnings":[]}`)
	db, err := Decode(v1)
	if err != nil {
		t.Fatalf("миграция v1: %v", err)
	}
	if db.Version != 3 || db.Buyers == nil || len(db.Buyers) != 0 || db.Suppliers[0].UNP != "" {
		t.Fatalf("после миграции: %+v", db)
	}
}

func TestDecodeMigratesV2(t *testing.T) {
	v2 := []byte(`{"version":2,"settings":{"company_name":"Тест","currency_label":"руб.","warn_days":3},
		"suppliers":[{"id":"s1","name":"АВИДО","unp":"123456789","deferral_days":14,"note":"","archived":false}],
		"obligations":[{"id":"o1","supplier_id":"s1","due_date":"2025-01-15","amount_cents":12000,"delivery_date":"2025-01-01","doc_number":"","comment":""}],
		"payments":[],"buyers":[],"warnings":[]}`)
	db, err := Decode(v2)
	if err != nil {
		t.Fatalf("миграция v2: %v", err)
	}
	if db.Version != 3 || db.Obligations[0].AmountExclVATCents != 0 || db.Obligations[0].VATCents != 0 {
		t.Fatalf("после миграции v2: %+v", db)
	}
}

func TestOpenUpdateReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "data.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("файл данных не создан: %v", err)
	}
	err = st.Update(func(db *model.DB) error {
		db.Suppliers = append(db.Suppliers, model.Supplier{ID: "s1", Name: "Тест"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Невалидная правка откатывается.
	err = st.Update(func(db *model.DB) error {
		db.Obligations = append(db.Obligations, model.Obligation{ID: "o1", SupplierID: "нет", DueDate: "2025-01-01", AmountCents: 1})
		return nil
	})
	if err == nil {
		t.Fatal("ожидалась ошибка валидации")
	}
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if db := st2.Snapshot(); len(db.Suppliers) != 1 || len(db.Obligations) != 0 {
		t.Fatalf("перезагрузка: %+v", db)
	}
}
