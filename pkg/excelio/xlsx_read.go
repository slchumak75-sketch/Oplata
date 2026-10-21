// Пакет excelio читает и пишет XLSX в формате исходной таблицы
// «Оплаты Строймаркет.xlsx» только средствами стандартной библиотеки Go
// (archive/zip + encoding/xml) — в корневом модуле НЕТ внешних зависимостей.
// Спецификация формата: docs/EXCEL_FORMAT.md.
//
// Этот файл — чтение. Поддерживается то, что реально встречается в файле:
// sharedStrings, inlineStr, числа, формулы с кэшированным значением <v>
// (Excel всегда сохраняет кэш; строка 2 с днями там — формулы вида =A2+1).
package excelio

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/slchumak75-sketch/oplata/pkg/model"
)

// Report — результат импорта для показа пользователю.
type Report struct {
	Year             int      `json:"year"`
	SheetName        string   `json:"sheet_name"`
	Suppliers        int      `json:"suppliers"`
	Obligations      int      `json:"obligations"`
	Payments         int      `json:"payments"`
	TotalSobstvCents int64    `json:"total_sobstv_cents"`
	TotalPaidCents   int64    `json:"total_paid_cents"`
	TotalDebtCents   int64    `json:"total_debt_cents"`
	Warnings         []string `json:"warnings"`
}

// ParseXLSX разбирает книгу Excel в готовую базу (свежие ID) + отчёт.
// yearOverride != 0 перекрывает год из имени листа (если лист назван не годом).
func ParseXLSX(data []byte, yearOverride int) (*model.DB, *Report, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("не удалось открыть файл как XLSX (нужен формат .xlsx): %w", err)
	}
	files := make(map[string]*zip.File)
	for _, f := range zr.File {
		files[f.Name] = f
	}
	shared, err := readSharedStrings(files)
	if err != nil {
		return nil, nil, err
	}
	sheetName, sheetPath, err := firstSheet(files)
	if err != nil {
		return nil, nil, err
	}
	grid, err := readSheet(files[sheetPath], shared)
	if err != nil {
		return nil, nil, fmt.Errorf("лист %q: %w", sheetName, err)
	}

	year := yearOverride
	if year == 0 {
		if y, err := strconv.Atoi(strings.TrimSpace(sheetName)); err == nil && y >= 1900 && y <= 2200 {
			year = y
		}
	}
	if year == 0 {
		return nil, nil, fmt.Errorf("не удалось определить год: лист называется %q, укажите год вручную при импорте", sheetName)
	}
	return gridToDB(grid, year, sheetName)
}

// ---------- низкоуровневое чтение XLSX ----------

type cellValue struct {
	text  string
	num   float64
	isNum bool
	has   bool
}

type grid struct {
	cells  map[int]map[int]cellValue // [row][col], нумерация с 1
	maxRow int
	maxCol int
}

func (g *grid) get(row, col int) cellValue {
	if r, ok := g.cells[row]; ok {
		return r[col]
	}
	return cellValue{}
}

func openZip(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func readSharedStrings(files map[string]*zip.File) ([]string, error) {
	f, ok := files["xl/sharedStrings.xml"]
	if !ok {
		return nil, nil
	}
	data, err := openZip(f)
	if err != nil {
		return nil, err
	}
	var out []string
	dec := xml.NewDecoder(bytes.NewReader(data))
	var cur strings.Builder
	inSI, inT := false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("sharedStrings.xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				inT = inSI
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, cur.String())
				inSI = false
			case "t":
				inT = false
			}
		case xml.CharData:
			if inT {
				cur.WriteString(string(t))
			}
		}
	}
	return out, nil
}

func attrValue(el xml.StartElement, local string) string {
	for _, a := range el.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// firstSheet возвращает имя и путь первого листа книги.
func firstSheet(files map[string]*zip.File) (string, string, error) {
	wb, ok := files["xl/workbook.xml"]
	if !ok {
		return "", "", fmt.Errorf("в файле нет xl/workbook.xml — это не XLSX")
	}
	data, err := openZip(wb)
	if err != nil {
		return "", "", err
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var name, rid string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", err
		}
		if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "sheet" {
			name = attrValue(el, "name")
			rid = attrValue(el, "id") // r:id
			break
		}
	}
	if name == "" {
		return "", "", fmt.Errorf("в книге не найден ни один лист")
	}
	// Связь r:id -> worksheets/sheetN.xml через rels; если rels не разобран — типовой fallback.
	path := "xl/worksheets/sheet1.xml"
	if rf, ok := files["xl/_rels/workbook.xml.rels"]; ok && rid != "" {
		if relData, err := openZip(rf); err == nil {
			d := xml.NewDecoder(bytes.NewReader(relData))
			for {
				tok, err := d.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					break
				}
				if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "Relationship" {
					if attrValue(el, "Id") == rid {
						target := attrValue(el, "Target")
						if !strings.HasPrefix(target, "xl/") && !strings.HasPrefix(target, "/") {
							target = "xl/" + target
						}
						path = strings.TrimPrefix(target, "/")
						break
					}
				}
			}
		}
	}
	if _, ok := files[path]; !ok {
		for n := range files {
			if strings.HasPrefix(n, "xl/worksheets/") && strings.HasSuffix(n, ".xml") {
				path = n
				break
			}
		}
	}
	if _, ok := files[path]; !ok {
		return "", "", fmt.Errorf("не найден XML листа %q", name)
	}
	return name, path, nil
}

func colLettersToNum(s string) int {
	n := 0
	for _, ch := range s {
		if ch >= 'A' && ch <= 'Z' {
			n = n*26 + int(ch-'A'+1)
		} else if ch >= 'a' && ch <= 'z' {
			n = n*26 + int(ch-'a'+1)
		} else {
			break
		}
	}
	return n
}

func splitCellRef(ref string) (row, col int) {
	i := 0
	for i < len(ref) && (ref[i] < '0' || ref[i] > '9') {
		i++
	}
	col = colLettersToNum(ref[:i])
	row, _ = strconv.Atoi(ref[i:])
	return row, col
}

func readSheet(f *zip.File, shared []string) (*grid, error) {
	data, err := openZip(f)
	if err != nil {
		return nil, err
	}
	g := &grid{cells: make(map[int]map[int]cellValue)}
	dec := xml.NewDecoder(bytes.NewReader(data))
	curRow, curCol := 0, 0
	var cellType string
	var valBuf, inlineBuf strings.Builder
	inV, inInlineT, hasF := false, false, false

	flushCell := func() {
		raw := strings.TrimSpace(valBuf.String())
		cv := cellValue{has: true}
		switch cellType {
		case "s": // sharedStrings
			if idx, err := strconv.Atoi(raw); err == nil && idx >= 0 && idx < len(shared) {
				cv.text = shared[idx]
			}
		case "inlineStr":
			cv.text = inlineBuf.String()
		case "str", "e": // строка-результат формулы / ошибка — как текст
			cv.text = raw
		default: // число (или кэш числовой формулы)
			if raw == "" {
				// Пустая ячейка или формула без кэшированного значения — значения нет.
				// (Для строки дней gridToDB восстановит день по порядку.)
				return
			}
			if v, err := strconv.ParseFloat(raw, 64); err == nil {
				cv.num, cv.isNum = v, true
			} else {
				cv.text = raw
			}
		}
		if g.cells[curRow] == nil {
			g.cells[curRow] = make(map[int]cellValue)
		}
		g.cells[curRow][curCol] = cv
		if curRow > g.maxRow {
			g.maxRow = curRow
		}
		if curCol > g.maxCol {
			g.maxCol = curCol
		}
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				if r := attrValue(t, "r"); r != "" {
					curRow, _ = strconv.Atoi(r)
				} else {
					curRow++
				}
				curCol = 0
			case "c":
				curCol++
				if ref := attrValue(t, "r"); ref != "" {
					if r, c := splitCellRef(ref); c > 0 {
						curRow, curCol = r, c
					}
				}
				cellType = attrValue(t, "t")
				valBuf.Reset()
				inlineBuf.Reset()
			case "v":
				inV = true
			case "t":
				inInlineT = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "c":
				if curRow > 0 && curCol > 0 {
					flushCell()
				}
			case "v":
				inV = false
			case "t":
				inInlineT = false
			}
		case xml.CharData:
			if inV {
				valBuf.WriteString(string(t))
			} else if inInlineT {
				inlineBuf.WriteString(string(t))
			}
		}
	}
	return g, nil
}

// ---------- интерпретация сетки в формате «Оплаты Строймаркет» ----------

var monthByName = map[string]int{
	"январь": 1, "февраль": 2, "март": 3, "апрель": 4, "май": 5,
	"июнь": 6, "июню": 6, // опечатка в исходном файле 2025 года
	"июль": 7, "август": 8, "сентябрь": 9, "октябрь": 10, "ноябрь": 11, "декабрь": 12,
}

// Названия месяцев для экспорта (1..12).
var MonthNames = []string{"", "январь", "февраль", "март", "апрель", "май", "июнь",
	"июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь"}

type parsedColumn struct {
	col  int
	date model.Date
	kind string // "sobstv" | "vyplaty"
}

func gridToDB(g *grid, year int, sheetName string) (*model.DB, *Report, error) {
	report := &Report{Year: year, SheetName: sheetName, Warnings: []string{}}

	// Строка заголовка поставщиков: столбец A == "ПОСТАВЩИК" (в исходнике строка 5).
	headerRow := 0
	for r := 1; r <= min(g.maxRow, 20); r++ {
		if strings.TrimSpace(g.get(r, 1).text) == "ПОСТАВЩИК" {
			headerRow = r
			break
		}
	}
	if headerRow == 0 || headerRow < 4 {
		return nil, nil, fmt.Errorf("не найдена строка «ПОСТАВЩИК» в столбце A — файл не похож на «Оплаты Строймаркет.xlsx»")
	}
	// Строка типов колонок: та, где больше всего подписей Собств/Выплаты (в исходнике строка 3).
	kindRow, best := 0, 0
	for r := 1; r < headerRow; r++ {
		n := 0
		for c := 3; c <= g.maxCol; c++ {
			txt := strings.TrimSpace(g.get(r, c).text)
			if txt == "Собств" || txt == "Выплаты" {
				n++
			}
		}
		if n > best {
			best, kindRow = n, r
		}
	}
	if kindRow < 3 || best == 0 {
		return nil, nil, fmt.Errorf("не найдена строка с подписями «Собств»/«Выплаты»")
	}
	monthRow, dayRow := kindRow-2, kindRow-1

	// Колонки -> (дата, тип). Месяц и день «протягиваются» вправо:
	// в исходнике месяц/день стоят только над «Собств», «Выплаты» наследуют дату пары.
	var cols []parsedColumn
	curMonth, curDay, sobstvInMonth := 0, 0, 0
	for c := 3; c <= g.maxCol; c++ {
		if mv := strings.ToLower(strings.TrimSpace(g.get(monthRow, c).text)); mv != "" {
			if m, ok := monthByName[mv]; ok {
				if m != curMonth {
					curMonth, curDay, sobstvInMonth = m, 0, 0
				}
			} else {
				report.Warnings = append(report.Warnings, fmt.Sprintf("Колонка %s: неизвестный месяц %q — колонка пропущена.", colNumToLetters(c), mv))
				curMonth = 0
			}
		}
		kind := ""
		switch strings.TrimSpace(g.get(kindRow, c).text) {
		case "Собств":
			kind = "sobstv"
			sobstvInMonth++
			if dv := g.get(dayRow, c); dv.isNum {
				curDay = int(dv.num)
			} else {
				curDay = sobstvInMonth // формула без кэша: дни идут по порядку
			}
		case "Выплаты":
			kind = "vyplaty"
		default:
			continue
		}
		if curMonth == 0 || curDay == 0 {
			continue
		}
		t := time.Date(year, time.Month(curMonth), curDay, 0, 0, 0, 0, time.UTC)
		if int(t.Month()) != curMonth || t.Day() != curDay {
			report.Warnings = append(report.Warnings, fmt.Sprintf("Колонка %s: несуществующая дата %02d.%02d.%d — пропущена.", colNumToLetters(c), curDay, curMonth, year))
			continue
		}
		cols = append(cols, parsedColumn{col: c, date: model.FromTime(t), kind: kind})
	}
	if len(cols) == 0 {
		return nil, nil, fmt.Errorf("не найдено колонок дат (Собств/Выплаты)")
	}

	db := model.NewEmptyDB()
	nameToID := make(map[string]string)
	for r := headerRow + 1; r <= g.maxRow; r++ {
		name := strings.TrimSpace(g.get(r, 1).text)
		if name == "" {
			continue
		}
		if name == "ВСЕГО" {
			continue
		}
		sid, dup := nameToID[model.NormalizeName(name)]
		if !dup {
			sid = model.NewID("sup")
			nameToID[model.NormalizeName(name)] = sid
			db.Suppliers = append(db.Suppliers, model.Supplier{ID: sid, Name: name})
		} else {
			report.Warnings = append(report.Warnings, fmt.Sprintf("Поставщик %q встречается повторно (строка %d) — операции объединены.", name, r))
		}
		for _, pc := range cols {
			cv := g.get(r, pc.col)
			if !cv.has {
				continue
			}
			if !cv.isNum {
				if strings.TrimSpace(cv.text) != "" {
					report.Warnings = append(report.Warnings, fmt.Sprintf("%s, %s: текст %q вместо суммы — пропущено.", name, pc.date, cv.text))
				}
				continue
			}
			if cv.num == 0 {
				continue
			}
			if pc.kind == "sobstv" {
				if cv.num < 0 {
					report.Warnings = append(report.Warnings, fmt.Sprintf("%s, %s: «Собств» с отрицательным знаком (%s) — импортировано как выплата.", name, pc.date, strconv.FormatFloat(cv.num, 'f', 2, 64)))
					db.Payments = append(db.Payments, model.Payment{
						ID: model.NewID("pay"), SupplierID: sid, Date: pc.date,
						AmountCents: -model.FloatToCents(cv.num),
						Comment:     "Импорт из Excel: отрицательное значение в «Собств».",
					})
					continue
				}
				db.Obligations = append(db.Obligations, model.Obligation{
					ID: model.NewID("obl"), SupplierID: sid, DueDate: pc.date,
					AmountCents: model.FloatToCents(cv.num),
				})
			} else {
				// В Excel выплаты хранятся со знаком минус; в приложении AmountCents > 0 = выплачено.
				p := model.Payment{
					ID: model.NewID("pay"), SupplierID: sid, Date: pc.date,
					AmountCents: -model.FloatToCents(cv.num),
				}
				if cv.num > 0 {
					report.Warnings = append(report.Warnings, fmt.Sprintf("%s, %s: «Выплаты» с положительным знаком (%s) — в Excel выплаты принято писать с минусом. Импортировано как отрицательная выплата (увеличивает задолженность), проверьте запись.", name, pc.date, strconv.FormatFloat(cv.num, 'f', 2, 64)))
					p.Comment = "Импорт из Excel: выплата была с положительным знаком, проверьте."
				}
				db.Payments = append(db.Payments, p)
			}
		}
	}

	report.Suppliers = len(db.Suppliers)
	report.Obligations = len(db.Obligations)
	report.Payments = len(db.Payments)
	for _, o := range db.Obligations {
		report.TotalSobstvCents += o.AmountCents
	}
	for _, p := range db.Payments {
		report.TotalPaidCents += p.AmountCents
	}
	report.TotalDebtCents = report.TotalSobstvCents - report.TotalPaidCents
	db.Warnings = append(db.Warnings, report.Warnings...)
	if err := db.Validate(); err != nil {
		return nil, nil, fmt.Errorf("импортированные данные не прошли проверку: %w", err)
	}
	return db, report, nil
}

// colNumToLetters: 1 -> A, 27 -> AA, 732 -> ABD.
func colNumToLetters(n int) string {
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}
