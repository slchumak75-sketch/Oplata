package excelio

// Запись XLSX в том же раскладе, что исходный «Оплаты Строймаркет.xlsx»:
// лист «<год>» (матрица поставщики × даты, пары Собств/Выплаты, строка ВСЕГО,
// столбец B ЗАДОЛЖЕННОСТЬ с формулами) + лист «Операции» (плоский журнал без потерь).
// Формат и отличия от оригинала — docs/EXCEL_FORMAT.md.

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/slchumak75-sketch/oplata/pkg/calc"
	"github.com/slchumak75-sketch/oplata/pkg/model"
)

// Индексы стилей в styles.xml (cellXfs). Держать в синхрони с stylesXML().
const (
	styleNormal    = 0
	styleMoney     = 1 // #,##0.00 + рамка
	styleHeader    = 2 // жирный, серая заливка, рамка, по центру
	styleMonth     = 3 // жирный, синяя заливка, рамка, по центру
	styleDay       = 4 // жирный, светло-красная заливка, рамка, по центру
	styleText      = 5 // текст с рамкой
	styleMoneyBold = 6 // деньги жирные (итоги)
)

// ExportXLSX собирает книгу за указанный год (матричный лист) плюс журнал операций за все годы.
func ExportXLSX(db *model.DB, year int) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	sheet1, err := buildMatrixSheet(db, year)
	if err != nil {
		return nil, err
	}
	files := []struct{ name, body string }{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", rootRelsXML},
		{"xl/workbook.xml", workbookXML(year)},
		{"xl/_rels/workbook.xml.rels", workbookRelsXML},
		{"xl/styles.xml", stylesXML()},
		{"xl/worksheets/sheet1.xml", sheet1},
		{"xl/worksheets/sheet2.xml", buildOperationsSheet(db)},
	}
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func strCell(ref string, style int, text string) string {
	return fmt.Sprintf(`<c r="%s" s="%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, style, esc(text))
}

func numCell(ref string, style int, decimal string) string {
	return fmt.Sprintf(`<c r="%s" s="%d"><v>%s</v></c>`, ref, style, decimal)
}

// formulaMoneyCell — формула с кэшированным значением (чтобы просмотрщики без пересчёта не показывали 0).
func formulaMoneyCell(ref string, style int, formula string, cachedCents int64) string {
	return fmt.Sprintf(`<c r="%s" s="%d"><f>%s</f><v>%s</v></c>`, ref, style, esc(formula), model.CentsToDecimal(cachedCents))
}

func buildMatrixSheet(db *model.DB, year int) (string, error) {
	// Все даты года: пара колонок на день, начиная с C.
	type dayCol struct {
		date model.Date
		col  int // колонка «Собств»; «Выплаты» = col+1
	}
	var days []dayCol
	col := 3
	for m := 1; m <= 12; m++ {
		dim := time.Date(year, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day()
		for d := 1; d <= dim; d++ {
			days = append(days, dayCol{date: model.Date(fmt.Sprintf("%04d-%02d-%02d", year, m, d)), col: col})
			col += 2
		}
	}
	lastCol := col - 1
	lastRef := colNumToLetters(lastCol)

	// Агрегаты: supplierID -> date -> {sobstv, vyplaty} в копейках; итоги по колонкам.
	type agg struct{ sobstv, vyplaty int64 }
	perSupplier := make(map[string]map[model.Date]*agg, len(db.Suppliers))
	colTotals := make(map[model.Date]*agg, len(days))
	getAgg := func(m map[model.Date]*agg, d model.Date) *agg {
		if a, ok := m[d]; ok {
			return a
		}
		a := &agg{}
		m[d] = a
		return a
	}
	inYear := func(d model.Date) bool { y, _ := d.YearMonth(); return y == year }
	for _, o := range db.Obligations {
		if !inYear(o.DueDate) {
			continue
		}
		if perSupplier[o.SupplierID] == nil {
			perSupplier[o.SupplierID] = make(map[model.Date]*agg)
		}
		getAgg(perSupplier[o.SupplierID], o.DueDate).sobstv += o.AmountCents
		getAgg(colTotals, o.DueDate).sobstv += o.AmountCents
	}
	for _, p := range db.Payments {
		if !inYear(p.Date) {
			continue
		}
		if perSupplier[p.SupplierID] == nil {
			perSupplier[p.SupplierID] = make(map[model.Date]*agg)
		}
		getAgg(perSupplier[p.SupplierID], p.Date).vyplaty += p.AmountCents
		getAgg(colTotals, p.Date).vyplaty += p.AmountCents
	}

	firstSupRow := 6
	lastSupRow := firstSupRow + len(db.Suppliers) - 1
	if len(db.Suppliers) == 0 {
		lastSupRow = firstSupRow
	}

	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	fmt.Fprintf(&b, `<dimension ref="A1:%s%d"/>`, lastRef, lastSupRow)
	b.WriteString(`<sheetViews><sheetView workbookViewId="0"><pane xSplit="2" ySplit="4" topLeftCell="C5" activePane="bottomRight" state="frozen"/></sheetView></sheetViews>`)
	b.WriteString(`<cols><col min="1" max="1" width="28" customWidth="1"/><col min="2" max="2" width="13" customWidth="1"/>`)
	for c := 3; c <= lastCol; c++ {
		width := "11"
		if c%2 == 0 {
			width = "9"
		}
		fmt.Fprintf(&b, `<col min="%d" max="%d" width="%s" customWidth="1"/>`, c, c, width)
	}
	b.WriteString(`</cols><sheetData>`)

	// Строка 1 — месяцы, строка 2 — дни, строка 3 — Собств/Выплаты.
	b.WriteString(`<row r="1">`)
	for _, dc := range days {
		_, m := dc.date.YearMonth()
		b.WriteString(strCell(fmt.Sprintf("%s1", colNumToLetters(dc.col)), styleMonth, MonthNames[m]))
		b.WriteString(strCell(fmt.Sprintf("%s1", colNumToLetters(dc.col+1)), styleMonth, MonthNames[m]))
	}
	b.WriteString(`</row><row r="2">`)
	for _, dc := range days {
		b.WriteString(numCell(fmt.Sprintf("%s2", colNumToLetters(dc.col)), styleDay, fmt.Sprintf("%d", dc.date.Time().Day())))
	}
	b.WriteString(`</row><row r="3">`)
	b.WriteString(strCell("B3", styleHeader, "ЗАДОЛЖЕННОСТЬ"))
	for _, dc := range days {
		b.WriteString(strCell(fmt.Sprintf("%s3", colNumToLetters(dc.col)), styleHeader, "Собств"))
		b.WriteString(strCell(fmt.Sprintf("%s3", colNumToLetters(dc.col+1)), styleHeader, "Выплаты"))
	}
	b.WriteString(`</row>`)

	// Строка 4 — ВСЕГО (суммы по колонкам), строка 5 — заголовок ПОСТАВЩИК.
	var grand int64
	rowSums := make([]int64, len(db.Suppliers))
	for i, s := range db.Suppliers {
		for _, a := range perSupplier[s.ID] {
			rowSums[i] += a.sobstv - a.vyplaty
		}
		grand += rowSums[i]
	}
	b.WriteString(`<row r="4">`)
	b.WriteString(strCell("A4", styleHeader, "ВСЕГО"))
	b.WriteString(formulaMoneyCell("B4", styleMoneyBold, fmt.Sprintf("SUM(C4:%s4)", lastRef), grand))
	for _, dc := range days {
		tot := colTotals[dc.date]
		var sCents, vCents int64
		if tot != nil {
			sCents, vCents = tot.sobstv, tot.vyplaty
		}
		sc := colNumToLetters(dc.col)
		vc := colNumToLetters(dc.col + 1)
		b.WriteString(formulaMoneyCell(sc+"4", styleMoneyBold, fmt.Sprintf("SUM(%s%d:%s%d)", sc, firstSupRow, sc, lastSupRow), sCents))
		// Выплаты в Excel пишутся с минусом, в том числе и в строке ВСЕГО.
		b.WriteString(formulaMoneyCell(vc+"4", styleMoneyBold, fmt.Sprintf("SUM(%s%d:%s%d)", vc, firstSupRow, vc, lastSupRow), -vCents))
	}
	b.WriteString(`</row><row r="5">`)
	b.WriteString(strCell("A5", styleHeader, "ПОСТАВЩИК"))
	if len(db.Suppliers) > 0 {
		b.WriteString(formulaMoneyCell("B5", styleMoneyBold, fmt.Sprintf("SUM(B%d:B%d)", firstSupRow, lastSupRow), grand))
	}
	b.WriteString(`</row>`)

	// Строки поставщиков.
	for i, s := range db.Suppliers {
		row := firstSupRow + i
		fmt.Fprintf(&b, `<row r="%d">`, row)
		b.WriteString(strCell(fmt.Sprintf("A%d", row), styleText, s.Name))
		b.WriteString(formulaMoneyCell(fmt.Sprintf("B%d", row), styleMoney, fmt.Sprintf("SUM(C%d:%s%d)", row, lastRef, row), rowSums[i]))
		for _, dc := range days {
			a := perSupplier[s.ID][dc.date]
			if a == nil {
				continue
			}
			if a.sobstv != 0 {
				b.WriteString(numCell(fmt.Sprintf("%s%d", colNumToLetters(dc.col), row), styleMoney, model.CentsToDecimal(a.sobstv)))
			}
			if a.vyplaty != 0 {
				// Выплаты в Excel — с минусом (AmountCents приложения инвертируется).
				b.WriteString(numCell(fmt.Sprintf("%s%d", colNumToLetters(dc.col+1), row), styleMoney, model.CentsToDecimal(-a.vyplaty)))
			}
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String(), nil
}

// buildOperationsSheet — плоский журнал всех операций (все годы, без агрегации и потерь полей).
func buildOperationsSheet(db *model.DB) string {
	names := make(map[string]string, len(db.Suppliers))
	for _, s := range db.Suppliers {
		names[s.ID] = s.Name
	}
	type op struct {
		kind, supplier, doc, comment string
		date, delivery               model.Date
		cents                        int64
	}
	ops := make([]op, 0, len(db.Obligations)+len(db.Payments))
	for _, o := range db.Obligations {
		ops = append(ops, op{"Собств (к оплате)", names[o.SupplierID], o.DocNumber, o.Comment, o.DueDate, o.DeliveryDate, o.AmountCents})
	}
	for _, p := range db.Payments {
		ops = append(ops, op{"Выплата", names[p.SupplierID], p.DocNumber, p.Comment, p.Date, "", p.AmountCents})
	}
	slices.SortFunc(ops, func(a, b op) int {
		if a.date != b.date {
			if a.date < b.date {
				return -1
			}
			return 1
		}
		return strings.Compare(a.supplier, b.supplier)
	})
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	b.WriteString(`<sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews>`)
	b.WriteString(`<cols><col min="1" max="1" width="18" customWidth="1"/><col min="2" max="2" width="28" customWidth="1"/><col min="3" max="3" width="13" customWidth="1"/><col min="4" max="4" width="13" customWidth="1"/><col min="5" max="5" width="13" customWidth="1"/><col min="6" max="6" width="16" customWidth="1"/><col min="7" max="7" width="40" customWidth="1"/></cols><sheetData>`)
	headers := []string{"Тип", "Поставщик", "Дата*", "Сумма", "Дата поставки", "№ документа", "Комментарий"}
	b.WriteString(`<row r="1">`)
	for i, h := range headers {
		b.WriteString(strCell(fmt.Sprintf("%s1", colNumToLetters(i+1)), styleHeader, h))
	}
	b.WriteString(`</row>`)
	for i, o := range ops {
		row := i + 2
		fmt.Fprintf(&b, `<row r="%d">`, row)
		b.WriteString(strCell(fmt.Sprintf("A%d", row), styleText, o.kind))
		b.WriteString(strCell(fmt.Sprintf("B%d", row), styleText, o.supplier))
		b.WriteString(strCell(fmt.Sprintf("C%d", row), styleText, o.date.Time().Format("02.01.2006")))
		b.WriteString(numCell(fmt.Sprintf("D%d", row), styleMoney, model.CentsToDecimal(o.cents)))
		delivery := ""
		if o.delivery != "" {
			delivery = o.delivery.Time().Format("02.01.2006")
		}
		b.WriteString(strCell(fmt.Sprintf("E%d", row), styleText, delivery))
		b.WriteString(strCell(fmt.Sprintf("F%d", row), styleText, o.doc))
		b.WriteString(strCell(fmt.Sprintf("G%d", row), styleText, o.comment))
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

const contentTypesXML = xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
	`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
	`<Default Extension="xml" ContentType="application/xml"/>` +
	`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
	`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
	`<Override PartName="/xl/worksheets/sheet2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
	`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
	`</Types>`

const rootRelsXML = xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
	`</Relationships>`

const workbookRelsXML = xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
	`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>` +
	`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
	`</Relationships>`

func workbookXML(year int) string {
	return xmlHeader + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		fmt.Sprintf(`<sheets><sheet name="%d" sheetId="1" r:id="rId1"/><sheet name="Операции" sheetId="2" r:id="rId2"/></sheets>`, year) +
		`<calcPr fullCalcOnLoad="1"/></workbook>`
}

// stylesXML: шрифты/заливки/рамки в духе оригинала (серые шапки, красная строка дат).
// Формат денег — встроенный numFmtId=4 (#,##0.00).
func stylesXML() string {
	return xmlHeader + `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
		`<fonts count="2">` +
		`<font><sz val="11"/><name val="Calibri"/></font>` +
		`<font><b/><sz val="11"/><name val="Calibri"/></font>` +
		`</fonts>` +
		`<fills count="5">` +
		`<fill><patternFill patternType="none"/></fill>` +
		`<fill><patternFill patternType="gray125"/></fill>` +
		`<fill><patternFill patternType="solid"><fgColor rgb="FFD9D9D9"/><bgColor rgb="FFD9D9D9"/></patternFill></fill>` + // 2 grey
		`<fill><patternFill patternType="solid"><fgColor rgb="FFBDD7EE"/><bgColor rgb="FFBDD7EE"/></patternFill></fill>` + // 3 blue(month)
		`<fill><patternFill patternType="solid"><fgColor rgb="FFF8CBAD"/><bgColor rgb="FFF8CBAD"/></patternFill></fill>` + // 4 light red(day)
		`</fills>` +
		`<borders count="2">` +
		`<border><left/><right/><top/><bottom/><diagonal/></border>` +
		`<border><left style="thin"><color auto="1"/></left><right style="thin"><color auto="1"/></right><top style="thin"><color auto="1"/></top><bottom style="thin"><color auto="1"/></bottom><diagonal/></border>` +
		`</borders>` +
		`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
		`<cellXfs count="7">` +
		`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` + // 0 normal
		`<xf numFmtId="4" fontId="0" fillId="0" borderId="1" xfId="0" applyNumberFormat="1"/>` + // 1 money
		`<xf numFmtId="0" fontId="1" fillId="2" borderId="1" xfId="0"><alignment horizontal="center" vertical="center"/></xf>` + // 2 header
		`<xf numFmtId="0" fontId="1" fillId="3" borderId="1" xfId="0"><alignment horizontal="center" vertical="center"/></xf>` + // 3 month
		`<xf numFmtId="0" fontId="1" fillId="4" borderId="1" xfId="0"><alignment horizontal="center" vertical="center"/></xf>` + // 4 day
		`<xf numFmtId="0" fontId="0" fillId="0" borderId="1" xfId="0"/>` + // 5 text
		`<xf numFmtId="4" fontId="1" fillId="2" borderId="1" xfId="0" applyNumberFormat="1"/>` + // 6 money bold
		`</cellXfs>` +
		`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
		`</styleSheet>`
}
