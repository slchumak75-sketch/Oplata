// Пакет calc — все расчёты задолженностей. Чистые функции без I/O: на вход *model.DB
// (или срезы операций одного поставщика), на выходе — структуры для API/GUI.
//
// Главный алгоритм — FIFO-разнесение выплат (Allocate): выплаты погашают начисления
// в порядке срока оплаты (DueDate), от самого раннего. Дата выплаты на очередность
// погашения НЕ влияет (предоплата зачтётся в самое раннее по сроку начисление).
// Это сознательно простое и предсказуемое правило, оно же даёт корректную
// «просроченную задолженность» на дату. Подробно: docs/DATA_MODEL.md.
package calc

import (
	"slices"

	"github.com/slchumak75-sketch/oplata/pkg/model"
)

// Статусы начисления после FIFO-разнесения.
const (
	StatusPaid    = "paid"     // оплачено полностью
	StatusPartial = "partial"  // оплачено частично, срок ещё не наступил
	StatusOverdue = "overdue"  // есть открытый остаток и срок уже прошёл (включая частично оплаченные)
	StatusPending = "pending"  // не оплачено, срок ещё не наступил
	StatusDueSoon = "due_soon" // не оплачено/частично, срок в пределах WarnDays (выставляется только в SupplierCard/Dashboard через MarkDueSoon)
)

// ObligationState — начисление + результат FIFO-разнесения на дату AsOf.
type ObligationState struct {
	Obligation  model.Obligation `json:"obligation"`
	PaidCents   int64            `json:"paid_cents"`
	OpenCents   int64            `json:"open_cents"`
	Status      string           `json:"status"`
	DaysOverdue int              `json:"days_overdue"` // >0 только для StatusOverdue
}

// Allocate разносит выплаты поставщика по его начислениям (FIFO по DueDate, затем по ID
// для стабильности) и определяет статус каждого начисления на дату asOf.
// Возвращает состояния в том же FIFO-порядке и переплату (сумму выплат сверх всех начислений;
// 0, если переплаты нет). Отрицательные выплаты (сторно) уменьшают доступную сумму погашения.
func Allocate(obligations []model.Obligation, payments []model.Payment, asOf model.Date) ([]ObligationState, int64) {
	obs := slices.Clone(obligations)
	slices.SortFunc(obs, func(a, b model.Obligation) int {
		if a.DueDate != b.DueDate {
			if a.DueDate < b.DueDate {
				return -1
			}
			return 1
		}
		return compareStr(a.ID, b.ID)
	})
	var pool int64 // всего выплачено (может стать отрицательным при сторно — тогда погашений нет)
	for _, p := range payments {
		pool += p.AmountCents
	}
	states := make([]ObligationState, 0, len(obs))
	for _, o := range obs {
		var paid int64
		if pool > 0 {
			paid = min(pool, o.AmountCents)
			pool -= paid
		}
		open := o.AmountCents - paid
		st := ObligationState{Obligation: o, PaidCents: paid, OpenCents: open}
		switch {
		case open == 0:
			st.Status = StatusPaid
		case o.DueDate < asOf:
			st.Status = StatusOverdue
			st.DaysOverdue = o.DueDate.DaysUntil(asOf)
		case paid > 0:
			st.Status = StatusPartial
		default:
			st.Status = StatusPending
		}
		states = append(states, st)
	}
	if pool < 0 {
		pool = 0
	}
	return states, pool
}

// MarkDueSoon помечает открытые начисления со сроком в пределах [asOf, asOf+warnDays]
// статусом due_soon (не трогая overdue/paid). Возвращает новый срез.
func MarkDueSoon(states []ObligationState, asOf model.Date, warnDays int) []ObligationState {
	if warnDays < 0 {
		return states
	}
	limit := asOf.AddDays(warnDays)
	out := slices.Clone(states)
	for i := range out {
		st := &out[i]
		if (st.Status == StatusPending || st.Status == StatusPartial) &&
			st.Obligation.DueDate >= asOf && st.Obligation.DueDate <= limit {
			st.Status = StatusDueSoon
		}
	}
	return out
}

// Balance — задолженность: сумма начислений минус сумма выплат (как столбец B в Excel).
// Отрицательное значение = переплата/аванс поставщику.
func Balance(obligations []model.Obligation, payments []model.Payment) int64 {
	var sum int64
	for _, o := range obligations {
		sum += o.AmountCents
	}
	for _, p := range payments {
		sum -= p.AmountCents
	}
	return sum
}

// BySupplier группирует операции базы по поставщикам.
func BySupplier(db *model.DB) (map[string][]model.Obligation, map[string][]model.Payment) {
	obs := make(map[string][]model.Obligation, len(db.Suppliers))
	pays := make(map[string][]model.Payment, len(db.Suppliers))
	for _, o := range db.Obligations {
		obs[o.SupplierID] = append(obs[o.SupplierID], o)
	}
	for _, p := range db.Payments {
		pays[p.SupplierID] = append(pays[p.SupplierID], p)
	}
	return obs, pays
}

// SupplierSummary — сводка по поставщику для списков и сводки (dashboard).
type SupplierSummary struct {
	Supplier      model.Supplier `json:"supplier"`
	BalanceCents  int64          `json:"balance_cents"`  // всего задолженность (все начисления − все выплаты)
	OverdueCents  int64          `json:"overdue_cents"`  // открытые остатки начислений со сроком < AsOf
	OverpaidCents int64          `json:"overpaid_cents"` // переплата сверх всех начислений
	OpenCount     int            `json:"open_count"`     // число не полностью оплаченных начислений
	NextDueDate   model.Date     `json:"next_due_date"`  // ближайший срок открытого начисления ("" если нет)
	NextDueCents  int64          `json:"next_due_cents"` // открытый остаток этого начисления
	TotalAccrued  int64          `json:"total_accrued_cents"`
	TotalPaid     int64          `json:"total_paid_cents"`
}

// SummarizeSupplier считает сводку по одному поставщику на дату asOf.
func SummarizeSupplier(s model.Supplier, obligations []model.Obligation, payments []model.Payment, asOf model.Date) SupplierSummary {
	states, overpaid := Allocate(obligations, payments, asOf)
	sum := SupplierSummary{Supplier: s, BalanceCents: Balance(obligations, payments), OverpaidCents: overpaid}
	for _, st := range states {
		sum.TotalAccrued += st.Obligation.AmountCents
		if st.OpenCents > 0 {
			sum.OpenCount++
			if st.Status == StatusOverdue {
				sum.OverdueCents += st.OpenCents
			}
			if sum.NextDueDate == "" || st.Obligation.DueDate < sum.NextDueDate {
				sum.NextDueDate = st.Obligation.DueDate
				sum.NextDueCents = st.OpenCents
			}
		}
	}
	for _, p := range payments {
		sum.TotalPaid += p.AmountCents
	}
	return sum
}

// SummarizeAll считает сводки по всем поставщикам в исходном порядке базы.
func SummarizeAll(db *model.DB, asOf model.Date) []SupplierSummary {
	obs, pays := BySupplier(db)
	out := make([]SupplierSummary, 0, len(db.Suppliers))
	for _, s := range db.Suppliers {
		out = append(out, SummarizeSupplier(s, obs[s.ID], pays[s.ID], asOf))
	}
	return out
}

// Dashboard — данные экрана «Сводка» на дату Date.
type Dashboard struct {
	Date           model.Date        `json:"date"`
	TotalDebtCents int64             `json:"total_debt_cents"`     // сумма положительных балансов поставщиков
	TotalOverpaid  int64             `json:"total_overpaid_cents"` // сумма переплат
	OverdueCents   int64             `json:"overdue_cents"`
	DueTodayCents  int64             `json:"due_today_cents"` // открытые начисления со сроком = Date
	DueWeekCents   int64             `json:"due_week_cents"`  // открытые со сроком в (Date, Date+7]
	PaidMonthCents int64             `json:"paid_month_cents"`
	AccruedMonth   int64             `json:"accrued_month_cents"`
	Suppliers      []SupplierSummary `json:"suppliers"`
	OverdueList    []OpenItem        `json:"overdue_list"`  // просроченные, по убыванию дней просрочки
	UpcomingList   []OpenItem        `json:"upcoming_list"` // срок в ближайшие 14 дней, по сроку
}

// OpenItem — открытое (не полностью оплаченное) начисление с названием поставщика.
type OpenItem struct {
	SupplierID   string     `json:"supplier_id"`
	SupplierName string     `json:"supplier_name"`
	ObligationID string     `json:"obligation_id"`
	DueDate      model.Date `json:"due_date"`
	AmountCents  int64      `json:"amount_cents"`
	PaidCents    int64      `json:"paid_cents"`
	OpenCents    int64      `json:"open_cents"`
	Status       string     `json:"status"`
	DaysOverdue  int        `json:"days_overdue"`
	DocNumber    string     `json:"doc_number"`
}

// BuildDashboard собирает сводку на дату asOf.
func BuildDashboard(db *model.DB, asOf model.Date) Dashboard {
	year, month := asOf.YearMonth()
	d := Dashboard{Date: asOf, Suppliers: SummarizeAll(db, asOf), OverdueList: []OpenItem{}, UpcomingList: []OpenItem{}}
	obs, pays := BySupplier(db)
	weekLimit := asOf.AddDays(7)
	upcomingLimit := asOf.AddDays(14)
	names := make(map[string]string, len(db.Suppliers))
	for _, s := range db.Suppliers {
		names[s.ID] = s.Name
	}
	for _, sum := range d.Suppliers {
		if sum.BalanceCents > 0 {
			d.TotalDebtCents += sum.BalanceCents
		} else {
			d.TotalOverpaid += -sum.BalanceCents
		}
		d.OverdueCents += sum.OverdueCents
	}
	for _, s := range db.Suppliers {
		states, _ := Allocate(obs[s.ID], pays[s.ID], asOf)
		for _, st := range states {
			if st.OpenCents <= 0 {
				continue
			}
			due := st.Obligation.DueDate
			if due == asOf {
				d.DueTodayCents += st.OpenCents
			}
			if due > asOf && due <= weekLimit {
				d.DueWeekCents += st.OpenCents
			}
			item := OpenItem{
				SupplierID: s.ID, SupplierName: s.Name, ObligationID: st.Obligation.ID,
				DueDate: due, AmountCents: st.Obligation.AmountCents, PaidCents: st.PaidCents,
				OpenCents: st.OpenCents, Status: st.Status, DaysOverdue: st.DaysOverdue,
				DocNumber: st.Obligation.DocNumber,
			}
			if st.Status == StatusOverdue {
				d.OverdueList = append(d.OverdueList, item)
			} else if due <= upcomingLimit {
				d.UpcomingList = append(d.UpcomingList, item)
			}
		}
		for _, o := range obs[s.ID] {
			if y, m := o.DueDate.YearMonth(); y == year && m == month {
				d.AccruedMonth += o.AmountCents
			}
		}
		for _, p := range pays[s.ID] {
			if y, m := p.Date.YearMonth(); y == year && m == month {
				d.PaidMonthCents += p.AmountCents
			}
		}
	}
	slices.SortFunc(d.OverdueList, func(a, b OpenItem) int {
		if a.DaysOverdue != b.DaysOverdue {
			return b.DaysOverdue - a.DaysOverdue
		}
		return compareStr(string(a.DueDate), string(b.DueDate))
	})
	slices.SortFunc(d.UpcomingList, func(a, b OpenItem) int {
		if a.DueDate != b.DueDate {
			return compareStr(string(a.DueDate), string(b.DueDate))
		}
		return compareStr(a.SupplierName, b.SupplierName)
	})
	return d
}

// Matrix — таблица «как в Excel» за один месяц: поставщики × дни, пара (Собств, Выплаты) в день.
type Matrix struct {
	Year      int         `json:"year"`
	Month     int         `json:"month"`
	Days      []MatrixDay `json:"days"`
	Rows      []MatrixRow `json:"rows"`
	DayTotals []DayTotal  `json:"day_totals"`       // строка «ВСЕГО» (строка 4 Excel)
	GrandYear int64       `json:"grand_year_cents"` // сумма столбца B за выбранный год (все поставщики)
}

// MatrixDay — один календарный день месяца.
type MatrixDay struct {
	Date model.Date `json:"date"`
	Day  int        `json:"day"`
}

// DayTotal — итоги всех поставщиков за день.
type DayTotal struct {
	SobstvCents  int64 `json:"sobstv_cents"`
	VyplatyCents int64 `json:"vyplaty_cents"` // знак как в приложении: выплата положительна (в Excel пишется с минусом)
}

// MatrixCell — суммы одного поставщика за один день (несколько операций суммируются).
type MatrixCell struct {
	Date         model.Date `json:"date"`
	SobstvCents  int64      `json:"sobstv_cents"`
	VyplatyCents int64      `json:"vyplaty_cents"`
}

// MatrixRow — строка поставщика.
type MatrixRow struct {
	Supplier model.Supplier `json:"supplier"`
	// YearBalanceCents — столбец B: сумма по строке за выбранный год (Собств − Выплаты).
	YearBalanceCents int64 `json:"year_balance_cents"`
	// BalanceCents — фактическая задолженность за всё время (все годы).
	BalanceCents int64        `json:"balance_cents"`
	Cells        []MatrixCell `json:"cells"` // только непустые дни месяца
}

// BuildMatrix строит матрицу за месяц year-month. Дни месяца вычисляются по календарю.
func BuildMatrix(db *model.DB, year, month int) Matrix {
	// Последний день месяца: нулевой день следующего месяца.
	daysInMonth := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()

	m := Matrix{Year: year, Month: month, Days: []MatrixDay{}, Rows: []MatrixRow{}}
	for d := 1; d <= daysInMonth; d++ {
		m.Days = append(m.Days, MatrixDay{Date: model.Date(dateStr(year, month, d)), Day: d})
	}
	m.DayTotals = make([]DayTotal, len(m.Days))

	obs, pays := BySupplier(db)
	for _, s := range db.Suppliers {
		row := MatrixRow{Supplier: s, Cells: []MatrixCell{}}
		byDay := make(map[int]*MatrixCell)
		for _, o := range obs[s.ID] {
			y, mo := o.DueDate.YearMonth()
			if y == year {
				row.YearBalanceCents += o.AmountCents
				if mo == month {
					c := cellFor(byDay, o.DueDate)
					c.SobstvCents += o.AmountCents
					m.DayTotals[o.DueDate.Time().Day()-1].SobstvCents += o.AmountCents
				}
			}
			row.BalanceCents += o.AmountCents
		}
		for _, p := range pays[s.ID] {
			y, mo := p.Date.YearMonth()
			if y == year {
				row.YearBalanceCents -= p.AmountCents
				if mo == month {
					c := cellFor(byDay, p.Date)
					c.VyplatyCents += p.AmountCents
					m.DayTotals[p.Date.Time().Day()-1].VyplatyCents += p.AmountCents
				}
			}
			row.BalanceCents -= p.AmountCents
		}
		for d := 1; d <= int(daysInMonth); d++ {
			if c, ok := byDay[d]; ok {
				row.Cells = append(row.Cells, *c)
			}
		}
		m.GrandYear += row.YearBalanceCents
		m.Rows = append(m.Rows, row)
	}
	return m
}

func cellFor(byDay map[int]*MatrixCell, date model.Date) *MatrixCell {
	d := date.Time().Day()
	if c, ok := byDay[d]; ok {
		return c
	}
	c := &MatrixCell{Date: date}
	byDay[d] = c
	return c
}

// SupplierCard — карточка поставщика: FIFO-состояния начислений, выплаты и динамика баланса.
type SupplierCard struct {
	Supplier      model.Supplier    `json:"supplier"`
	AsOf          model.Date        `json:"as_of"`
	BalanceCents  int64             `json:"balance_cents"`
	OverdueCents  int64             `json:"overdue_cents"`
	OverpaidCents int64             `json:"overpaid_cents"`
	Obligations   []ObligationState `json:"obligations"` // в FIFO-порядке (по сроку)
	Payments      []model.Payment   `json:"payments"`    // по дате выплаты
	Running       []RunningPoint    `json:"running"`     // баланс нарастающим итогом по датам операций
}

// RunningPoint — точка графика баланса.
type RunningPoint struct {
	Date         model.Date `json:"date"`
	BalanceCents int64      `json:"balance_cents"`
}

// BuildSupplierCard собирает карточку поставщика на дату asOf (nil, если поставщик не найден).
func BuildSupplierCard(db *model.DB, supplierID string, asOf model.Date) *SupplierCard {
	s := db.SupplierByID(supplierID)
	if s == nil {
		return nil
	}
	obs, pays := BySupplier(db)
	states, overpaid := Allocate(obs[supplierID], pays[supplierID], asOf)
	states = MarkDueSoon(states, asOf, db.Settings.WarnDays)
	card := &SupplierCard{
		Supplier: *s, AsOf: asOf, OverpaidCents: overpaid,
		Payments: slices.Clone(pays[supplierID]), Running: []RunningPoint{},
	}
	if card.Payments == nil {
		card.Payments = []model.Payment{}
	}
	slices.SortFunc(card.Payments, func(a, b model.Payment) int {
		if a.Date != b.Date {
			return compareStr(string(a.Date), string(b.Date))
		}
		return compareStr(a.ID, b.ID)
	})
	card.Obligations = states
	card.BalanceCents = Balance(obs[supplierID], pays[supplierID])
	for _, st := range states {
		if st.Status == StatusOverdue {
			card.OverdueCents += st.OpenCents
		}
	}
	// Нарастающий баланс по объединённой хронологии (начисление датой срока, выплата датой выплаты).
	type ev struct {
		date  model.Date
		delta int64
	}
	evs := make([]ev, 0, len(obs[supplierID])+len(pays[supplierID]))
	for _, o := range obs[supplierID] {
		evs = append(evs, ev{o.DueDate, o.AmountCents})
	}
	for _, p := range pays[supplierID] {
		evs = append(evs, ev{p.Date, -p.AmountCents})
	}
	slices.SortFunc(evs, func(a, b ev) int { return compareStr(string(a.date), string(b.date)) })
	var run int64
	for _, e := range evs {
		run += e.delta
		if n := len(card.Running); n > 0 && card.Running[n-1].Date == e.date {
			card.Running[n-1].BalanceCents = run
		} else {
			card.Running = append(card.Running, RunningPoint{Date: e.date, BalanceCents: run})
		}
	}
	return card
}

func dateStr(y, m, d int) string { return model.FromTime(mkTime(y, m, d)).String() }

func compareStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
