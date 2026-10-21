// Пакет model содержит типы предметной области приложения «Оплаты» и формат файла данных.
//
// Термины (соответствуют исходному Excel, см. docs/EXCEL_FORMAT.md):
//   - Obligation — начисление к оплате, колонка «Собств»: сумма к оплате за поставленный
//     товар. DueDate — срок оплаты с учётом отсрочки (дата в шапке Excel).
//   - Payment — выплата, колонка «Выплаты»: фактически выплаченная сумма. Date — дата выплаты.
//   - Задолженность поставщика = сумма всех Obligation минус сумма всех Payment
//     (аналог столбца B «ЗАДОЛЖЕНОСТЬ» в Excel).
//
// ВСЕ суммы хранятся в целых копейках (int64, поля *_cents) — никаких float в данных и API.
// Даты — строки ISO «2006-01-02» (тип Date).
package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
)

// SchemaVersion — версия формата файла данных (поле DB.Version).
// При изменении формата: увеличить, добавить миграцию в pkg/store и запись в CHANGELOG.md.
// v2: добавлены Supplier.UNP и справочник покупателей (Buyer).
// v3: у начислений добавлена необязательная разбивка суммы поставки без НДС и НДС.
// v4: Supplier.UNP удалён по решению владельца (у покупателей УНП сохранён);
// старое поле suppliers[].unp при загрузке игнорируется и в v4 не записывается.
// Файлы v1–v3 мигрируют в store.Decode.
const SchemaVersion = 4

// Date — календарная дата в формате ISO yyyy-mm-dd. Сравнение строк == сравнение дат.
type Date string

const dateLayout = "2006-01-02"

// Today возвращает сегодняшнюю дату по локальному времени.
func Today() Date { return FromTime(time.Now()) }

// FromTime преобразует time.Time в Date (отбрасывает время суток).
func FromTime(t time.Time) Date { return Date(t.Format(dateLayout)) }

// ParseDate проверяет и нормализует строку даты. Пустая строка допустима только
// для необязательных полей — вызывающий код проверяет это сам.
func ParseDate(s string) (Date, error) {
	s = strings.TrimSpace(s)
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return "", fmt.Errorf("некорректная дата %q (нужен формат гггг-мм-дд)", s)
	}
	return Date(t.Format(dateLayout)), nil
}

// String возвращает дату как строку ISO.
func (d Date) String() string { return string(d) }

// Valid сообщает, является ли строка корректной датой.
func (d Date) Valid() bool {
	_, err := time.Parse(dateLayout, string(d))
	return err == nil
}

// Time возвращает time.Time (полночь UTC). Для невалидной даты вернёт нулевое время.
func (d Date) Time() time.Time {
	t, _ := time.Parse(dateLayout, string(d))
	return t
}

// AddDays прибавляет n дней (n может быть отрицательным).
func (d Date) AddDays(n int) Date { return FromTime(d.Time().AddDate(0, 0, n)) }

// DaysUntil возвращает количество дней от d до other (other - d).
func (d Date) DaysUntil(other Date) int {
	return int(other.Time().Sub(d.Time()).Hours() / 24)
}

// YearMonth возвращает год и месяц даты.
func (d Date) YearMonth() (int, int) {
	t := d.Time()
	return t.Year(), int(t.Month())
}

// Supplier — поставщик (строка таблицы Excel).
type Supplier struct {
	ID string `json:"id"`
	// Name — название, как в Excel (столбец A). Уникальность контролирует store с нормализацией.
	Name string `json:"name"`
	// DeferralDays — отсрочка платежа в днях по умолчанию для этого поставщика.
	// 0 = не задана (срок оплаты вводится вручную).
	DeferralDays int    `json:"deferral_days"`
	Note         string `json:"note"`
	// Archived — скрыт из основных списков, данные и история сохраняются.
	Archived bool `json:"archived"`
}

// Buyer — покупатель (справочник «Карточка покупателя»). Операций с покупателями
// приложение пока не ведёт — это адресная книга контрагентов: наименование и УНП.
type Buyer struct {
	ID string `json:"id"`
	// Name — наименование покупателя. Уникально с нормализацией, как у поставщиков.
	Name string `json:"name"`
	// UNP — учётный номер плательщика (9 символов), необязательно.
	UNP  string `json:"unp"`
	Note string `json:"note"`
}

// Obligation — сумма к оплате за поставленный товар (колонка «Собств»).
type Obligation struct {
	ID         string `json:"id"`
	SupplierID string `json:"supplier_id"`
	// DueDate — срок оплаты с учётом отсрочки (главная дата записи, дата колонки Excel).
	DueDate Date `json:"due_date"`
	// AmountCents — сумма к оплате в копейках, всегда > 0. Для поставки с разбивкой НДС
	// это сумма С НДС: AmountExclVATCents + VATCents.
	AmountCents int64 `json:"amount_cents"`
	// AmountExclVATCents и VATCents — необязательная разбивка поставки (схема v3):
	// сумма без НДС и сумма НДС в копейках. Оба нуля означают «разбивки нет»
	// (например, начисление импортировано из Excel, где хранится только итог).
	AmountExclVATCents int64 `json:"amount_excl_vat_cents,omitempty"`
	VATCents           int64 `json:"vat_cents,omitempty"`
	// DeliveryDate — необязательная дата поставки. Если задана вместе с отсрочкой,
	// интерфейс предлагает DueDate = DeliveryDate + отсрочка, но хранится именно DueDate.
	DeliveryDate Date   `json:"delivery_date,omitempty"`
	DocNumber    string `json:"doc_number"`
	Comment      string `json:"comment"`
}

// Payment — выплата поставщику (колонка «Выплаты»).
type Payment struct {
	ID         string `json:"id"`
	SupplierID string `json:"supplier_id"`
	// Date — фактическая дата выплаты.
	Date Date `json:"date"`
	// AmountCents — сумма выплаты в копейках. Обычно > 0 (уменьшает задолженность).
	// Отрицательное значение допустимо как сторно / результат импорта ошибочной записи Excel.
	AmountCents int64  `json:"amount_cents"`
	DocNumber   string `json:"doc_number"`
	Comment     string `json:"comment"`
}

// Settings — настройки приложения, хранятся внутри файла данных.
type Settings struct {
	CompanyName   string `json:"company_name"`
	CurrencyLabel string `json:"currency_label"`
	// WarnDays — за сколько дней до срока подсвечивать «скоро срок».
	WarnDays int `json:"warn_days"`
	// SeededFrom — устаревшая пометка стартовых данных из старых версий; в новых базах пусто.
	SeededFrom string `json:"seeded_from,omitempty"`
}

// DefaultSettings возвращает настройки для новой пустой базы.
func DefaultSettings() Settings {
	return Settings{CompanyName: "Строймаркет", CurrencyLabel: "руб.", WarnDays: 3}
}

// DB — всё содержимое файла данных (JSON). Порядок срезов — порядок отображения:
// поставщики в порядке Excel, операции сортируются при расчётах/выводе.
type DB struct {
	Version     int          `json:"version"`
	Settings    Settings     `json:"settings"`
	Suppliers   []Supplier   `json:"suppliers"`
	Obligations []Obligation `json:"obligations"`
	Payments    []Payment    `json:"payments"`
	// Buyers — справочник покупателей (схема v2). С расчётами оплат не связан.
	Buyers []Buyer `json:"buyers"`
	// Warnings — предупреждения импорта/seed, показываются пользователю баннером,
	// пока он их не скроет (очистка через PUT /api/settings или сброс базы).
	Warnings []string `json:"warnings"`
}

// NewEmptyDB создаёт пустую базу текущей версии схемы.
func NewEmptyDB() *DB {
	return &DB{
		Version:     SchemaVersion,
		Settings:    DefaultSettings(),
		Suppliers:   []Supplier{},
		Obligations: []Obligation{},
		Payments:    []Payment{},
		Buyers:      []Buyer{},
		Warnings:    []string{},
	}
}

// SupplierByID ищет поставщика по ID (nil, если нет).
func (db *DB) SupplierByID(id string) *Supplier {
	for i := range db.Suppliers {
		if db.Suppliers[i].ID == id {
			return &db.Suppliers[i]
		}
	}
	return nil
}

// BuyerByID ищет покупателя по ID (nil, если нет).
func (db *DB) BuyerByID(id string) *Buyer {
	for i := range db.Buyers {
		if db.Buyers[i].ID == id {
			return &db.Buyers[i]
		}
	}
	return nil
}

// NormalizeUNP приводит УНП к каноническому виду для хранения и сравнения:
// без пробелов, в верхнем регистре.
func NormalizeUNP(s string) string {
	return strings.ToUpper(strings.Join(strings.Fields(strings.TrimSpace(s)), ""))
}

// ValidateUNP проверяет УНП, если он указан (пустое значение допустимо).
// УНП в Беларуси — 9 символов: у организаций цифры, у физлиц — буквы и цифры.
// Проверка намеренно мягкая (9–15 букв/цифр), чтобы не отвергать нестандартные записи.
func ValidateUNP(unp string) error {
	n := NormalizeUNP(unp)
	if n == "" {
		return nil
	}
	runes := []rune(n)
	if len(runes) < 9 || len(runes) > 15 {
		return fmt.Errorf("УНП %q: ожидается 9 символов (допустимо 9–15)", unp)
	}
	for _, r := range runes {
		isDigit := r >= '0' && r <= '9'
		isLatin := (r >= 'A' && r <= 'Z')
		isCyrillic := (r >= 'А' && r <= 'Я') || r == 'Ё'
		if !isDigit && !isLatin && !isCyrillic {
			return fmt.Errorf("УНП %q: допустимы только буквы и цифры", unp)
		}
	}
	return nil
}

// NormalizeName нормализует название для сравнения (регистр, пробелы, ё).
func NormalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "ё", "е")
	return strings.Join(strings.Fields(s), " ")
}

// Validate проверяет целостность базы перед сохранением/после импорта.
// Возвращает первую найденную ошибку в человекочитаемом виде (на русском —
// сообщения показываются пользователю в GUI).
func (db *DB) Validate() error {
	if db.Version != SchemaVersion {
		return fmt.Errorf("неподдерживаемая версия формата данных: %d (нужна %d)", db.Version, SchemaVersion)
	}
	ids := make(map[string]bool, len(db.Suppliers))
	names := make(map[string]string, len(db.Suppliers))
	for _, s := range db.Suppliers {
		if s.ID == "" {
			return fmt.Errorf("поставщик %q: пустой id", s.Name)
		}
		if ids[s.ID] {
			return fmt.Errorf("дублирующийся id поставщика: %s", s.ID)
		}
		ids[s.ID] = true
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("поставщик %s: пустое название", s.ID)
		}
		if s.DeferralDays < 0 || s.DeferralDays > 3650 {
			return fmt.Errorf("поставщик %q: отсрочка вне диапазона 0..3650: %d", s.Name, s.DeferralDays)
		}
		if prev, ok := names[NormalizeName(s.Name)]; ok {
			return fmt.Errorf("поставщики %q и %q: одинаковые названия", prev, s.Name)
		}
		names[NormalizeName(s.Name)] = s.Name
	}
	buyerIDs := make(map[string]bool, len(db.Buyers))
	buyerNames := make(map[string]string, len(db.Buyers))
	buyerUNPs := make(map[string]string, len(db.Buyers))
	for _, b := range db.Buyers {
		if b.ID == "" {
			return fmt.Errorf("покупатель %q: пустой id", b.Name)
		}
		if buyerIDs[b.ID] {
			return fmt.Errorf("дублирующийся id покупателя: %s", b.ID)
		}
		buyerIDs[b.ID] = true
		if strings.TrimSpace(b.Name) == "" {
			return fmt.Errorf("покупатель %s: пустое наименование", b.ID)
		}
		if err := ValidateUNP(b.UNP); err != nil {
			return fmt.Errorf("покупатель %q: %w", b.Name, err)
		}
		if unp := NormalizeUNP(b.UNP); unp != "" {
			if prev, ok := buyerUNPs[unp]; ok {
				return fmt.Errorf("покупатели %q и %q: одинаковый УНП %s", prev, b.Name, unp)
			}
			buyerUNPs[unp] = b.Name
		}
		if prev, ok := buyerNames[NormalizeName(b.Name)]; ok {
			return fmt.Errorf("покупатели %q и %q: одинаковые наименования", prev, b.Name)
		}
		buyerNames[NormalizeName(b.Name)] = b.Name
	}
	for _, o := range db.Obligations {
		if !ids[o.SupplierID] {
			return fmt.Errorf("начисление %s: неизвестный поставщик %s", o.ID, o.SupplierID)
		}
		if !o.DueDate.Valid() {
			return fmt.Errorf("начисление %s: некорректный срок оплаты %q", o.ID, o.DueDate)
		}
		if o.AmountCents <= 0 {
			return fmt.Errorf("начисление %s: сумма должна быть положительной: %d", o.ID, o.AmountCents)
		}
		if o.AmountExclVATCents != 0 || o.VATCents != 0 {
			if o.AmountExclVATCents < 0 || o.VATCents < 0 {
				return fmt.Errorf("начисление %s: сумма без НДС и НДС не могут быть отрицательными", o.ID)
			}
			if o.AmountExclVATCents+o.VATCents != o.AmountCents {
				return fmt.Errorf("начисление %s: сумма с НДС должна равняться сумме без НДС + НДС", o.ID)
			}
		}
		if o.DeliveryDate != "" && !o.DeliveryDate.Valid() {
			return fmt.Errorf("начисление %s: некорректная дата поставки %q", o.ID, o.DeliveryDate)
		}
	}
	for _, p := range db.Payments {
		if !ids[p.SupplierID] {
			return fmt.Errorf("оплата %s: неизвестный поставщик %s", p.ID, p.SupplierID)
		}
		if !p.Date.Valid() {
			return fmt.Errorf("оплата %s: некорректная дата %q", p.ID, p.Date)
		}
		if p.AmountCents == 0 {
			return fmt.Errorf("оплата %s: нулевая сумма", p.ID)
		}
	}
	return nil
}

// NewID генерирует ID вида "sup-1a2b3c4d5e6f" (префикс + 6 случайных байт hex).
// Случайность вместо счётчика — чтобы ID не зависели от состояния и не конфликтовали при импортах.
func NewID(prefix string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand на поддерживаемых ОС не отказывает; запасной вариант — время.
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

// FloatToCents округляет сумму в рублях (float из Excel) до целых копеек.
func FloatToCents(v float64) int64 { return int64(math.Round(v * 100)) }

// CentsToDecimal форматирует копейки как десятичную строку с точкой: 123456 -> "1234.56".
// Используется при записи чисел в XLSX (без float, чтобы не было артефактов).
func CentsToDecimal(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

// FormatCents форматирует копейки для человека: 123456 -> "1 234,56" (как в Excel #,##0.00).
func FormatCents(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	rubles := fmt.Sprintf("%d", c/100)
	// Группировка тысяч обычным пробелом (в GUI группирует фронтенд, здесь — для CSV/логов).
	var parts []string
	for len(rubles) > 3 {
		parts = append([]string{rubles[len(rubles)-3:]}, parts...)
		rubles = rubles[:len(rubles)-3]
	}
	parts = append([]string{rubles}, parts...)
	return fmt.Sprintf("%s%s,%02d", sign, strings.Join(parts, " "), c%100)
}
