package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/slchumak75-sketch/oplata/pkg/calc"
	"github.com/slchumak75-sketch/oplata/pkg/model"
)

// TestSeedReconciliation сверяет встроенный seed с итогами исходного Excel
// (docs/EXCEL_FORMAT.md): 43 поставщика, Собств 837 334,34, выплаты 811 579,42,
// задолженность 25 754,92. Если seed перегенерирован другим файлом — обновить тест и доки.
func TestSeedReconciliation(t *testing.T) {
	db, err := Decode(SeedJSON)
	if err != nil {
		t.Fatalf("seed нечитаем: %v", err)
	}
	if len(db.Suppliers) != 43 || len(db.Obligations) != 438 || len(db.Payments) != 415 {
		t.Fatalf("seed: поставщиков=%d начислений=%d выплат=%d", len(db.Suppliers), len(db.Obligations), len(db.Payments))
	}
	if got := calc.Balance(db.Obligations, db.Payments); got != 2_575_492 {
		t.Fatalf("seed задолженность=%d коп., ожидалось 2575492 (25 754,92)", got)
	}
	names := map[string]int64{}
	obs, pays := calc.BySupplier(db)
	for _, s := range db.Suppliers {
		names[s.Name] = calc.Balance(obs[s.ID], pays[s.ID])
	}
	want := map[string]int64{
		"АВИДО": 159045, "Алваис": 23476, "Продмир": 173568, "Культбытхозторг": 85641,
		"ВергоХоз": 48551, "Система ООО": 1649164, "ТД Комплект": 436047,
	}
	for name, cents := range want {
		if names[name] != cents {
			t.Fatalf("%s: баланс %d, ожидалось %d", name, names[name], cents)
		}
	}
	if len(db.Warnings) != 1 {
		t.Fatalf("seed предупреждений=%d, ожидалось 1 (знак выплаты Алваис 11.03.2025)", len(db.Warnings))
	}
}

func TestOpenUpdateReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "data.json")
	st, err := Open(path, false)
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
	st2, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if db := st2.Snapshot(); len(db.Suppliers) != 1 || len(db.Obligations) != 0 {
		t.Fatalf("перезагрузка: %+v", db)
	}
}
