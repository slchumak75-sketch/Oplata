package excelio

import (
	"archive/zip"
	"bytes"
	"testing"

	"github.com/slchumak75-sketch/oplata/pkg/calc"
	"github.com/slchumak75-sketch/oplata/pkg/model"
)

func testDB() *model.DB {
	db := model.NewEmptyDB()
	db.Suppliers = []model.Supplier{
		{ID: "s1", Name: "АВИДО"},
		{ID: "s2", Name: "Поставщик & Co <тест>"},
	}
	db.Obligations = []model.Obligation{
		{ID: "o1", SupplierID: "s1", DueDate: "2025-01-03", AmountCents: 101349, DocNumber: "ТН-1", Comment: "комментарий & <xml>"},
		{ID: "o2", SupplierID: "s1", DueDate: "2025-01-03", AmountCents: 100}, // тот же день — в матрице суммируется
		{ID: "o3", SupplierID: "s2", DueDate: "2025-06-30", AmountCents: 5000},
		{ID: "o4", SupplierID: "s2", DueDate: "2024-12-31", AmountCents: 700}, // другой год — не попадает в матрицу 2025
	}
	db.Payments = []model.Payment{
		{ID: "p1", SupplierID: "s1", Date: "2025-01-05", AmountCents: 101349},
	}
	return db
}

// TestRoundTrip: ExportXLSX(2025) -> ParseXLSX даёт те же итоги за 2025 год.
func TestRoundTrip(t *testing.T) {
	db := testDB()
	data, err := ExportXLSX(db, 2025)
	if err != nil {
		t.Fatal(err)
	}
	got, report, err := ParseXLSX(data, 0)
	if err != nil {
		t.Fatalf("ParseXLSX собственного экспорта: %v", err)
	}
	if report.Year != 2025 || report.Suppliers != 2 {
		t.Fatalf("report: %+v", report)
	}
	// Две операции АВИДО на 03.01 в матрице склеились в одну ячейку (как в Excel).
	if report.Obligations != 2 || report.Payments != 1 {
		t.Fatalf("операции: %+v", report)
	}
	wantSobstv := int64(101349 + 100 + 5000)
	if report.TotalSobstvCents != wantSobstv || report.TotalDebtCents != wantSobstv-101349 {
		t.Fatalf("итоги: %+v", report)
	}
	if bal := calc.Balance(got.Obligations, got.Payments); bal != wantSobstv-101349 {
		t.Fatalf("баланс импортированной базы: %d", bal)
	}
}

// TestParseOriginalStyleFixture: книга в стиле оригинала (sharedStrings, формулы дней
// с кэшем, опечатка «июню», выплата с плюсом) разбирается корректно.
func TestParseOriginalStyleFixture(t *testing.T) {
	shared := []string{"июню", "Собств", "Выплаты", "ЗАДОЛЖЕНОСТЬ", "ВСЕГО", "ПОСТАВЩИК", "Алваис"}
	var ssb bytes.Buffer
	ssb.WriteString(xmlHeader + `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	for _, s := range shared {
		ssb.WriteString("<si><t>" + s + "</t></si>")
	}
	ssb.WriteString("</sst>")

	sheet := xmlHeader + `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` +
		// строка 1: месяц только над Собств (C, E)
		`<row r="1"><c r="C1" t="s"><v>0</v></c><c r="E1" t="s"><v>0</v></c></row>` +
		// строка 2: дни-формулы с кэшем: 11 и 12
		`<row r="2"><c r="C2"><f>A2+11</f><v>11</v></c><c r="E2"><f>C2+1</f><v>12</v></c></row>` +
		// строка 3: типы
		`<row r="3"><c r="B3" t="s"><v>3</v></c><c r="C3" t="s"><v>1</v></c><c r="D3" t="s"><v>2</v></c><c r="E3" t="s"><v>1</v></c><c r="F3" t="s"><v>2</v></c></row>` +
		`<row r="4"><c r="A4" t="s"><v>4</v></c></row>` +
		`<row r="5"><c r="A5" t="s"><v>5</v></c></row>` +
		// Алваис: Собств 117.38 на 11.06; выплата с плюсом 117.38 на 12.06 (ошибка знака, как в оригинале)
		`<row r="6"><c r="A6" t="s"><v>6</v></c><c r="C6"><v>117.38</v></c><c r="F6"><v>117.38</v></c></row>` +
		`</sheetData></worksheet>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	parts := map[string]string{
		"[Content_Types].xml":        contentTypesXML,
		"_rels/.rels":                rootRelsXML,
		"xl/workbook.xml":            workbookXML(2025),
		"xl/_rels/workbook.xml.rels": workbookRelsXML,
		"xl/styles.xml":              stylesXML(),
		"xl/sharedStrings.xml":       ssb.String(),
		"xl/worksheets/sheet1.xml":   sheet,
		"xl/worksheets/sheet2.xml":   sheet,
	}
	for name, body := range parts {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()

	db, report, err := ParseXLSX(buf.Bytes(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Suppliers != 1 || report.Obligations != 1 || report.Payments != 1 {
		t.Fatalf("report: %+v", report)
	}
	if db.Obligations[0].DueDate != "2025-06-11" || db.Obligations[0].AmountCents != 11738 {
		t.Fatalf("obligation: %+v", db.Obligations[0])
	}
	if db.Payments[0].Date != "2025-06-12" || db.Payments[0].AmountCents != -11738 {
		t.Fatalf("payment: %+v", db.Payments[0])
	}
	if len(report.Warnings) != 1 {
		t.Fatalf("warnings: %v", report.Warnings)
	}
}

func TestColLetters(t *testing.T) {
	cases := map[int]string{1: "A", 26: "Z", 27: "AA", 732: "ABD", 703: "AAA"}
	for n, want := range cases {
		if got := colNumToLetters(n); got != want {
			t.Fatalf("colNumToLetters(%d)=%q, want %q", n, got, want)
		}
		if got := colLettersToNum(want); got != n {
			t.Fatalf("colLettersToNum(%q)=%d, want %d", want, got, n)
		}
	}
}
