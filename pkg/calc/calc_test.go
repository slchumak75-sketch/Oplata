package calc

import (
	"testing"

	"github.com/slchumak75-sketch/oplata/pkg/model"
)

func obl(id string, due string, cents int64) model.Obligation {
	return model.Obligation{ID: id, SupplierID: "s1", DueDate: model.Date(due), AmountCents: cents}
}

func pay(id string, date string, cents int64) model.Payment {
	return model.Payment{ID: id, SupplierID: "s1", Date: model.Date(date), AmountCents: cents}
}

func TestAllocateFIFO(t *testing.T) {
	obs := []model.Obligation{obl("o2", "2025-02-01", 100_00), obl("o1", "2025-01-10", 50_00)}
	pays := []model.Payment{pay("p1", "2025-01-20", 80_00)}
	states, overpaid := Allocate(obs, pays, model.Date("2025-01-25"))
	if overpaid != 0 {
		t.Fatalf("overpaid=%d, want 0", overpaid)
	}
	if states[0].Obligation.ID != "o1" || states[0].Status != StatusPaid || states[0].PaidCents != 50_00 {
		t.Fatalf("первое начисление должно быть оплачено: %+v", states[0])
	}
	if states[1].PaidCents != 30_00 || states[1].OpenCents != 70_00 || states[1].Status != StatusPartial {
		t.Fatalf("второе начисление частично: %+v", states[1])
	}
}

func TestAllocateOverdueAndPrepayment(t *testing.T) {
	obs := []model.Obligation{obl("o1", "2025-01-10", 100_00)}
	// Выплата раньше срока и больше суммы: переплата 20.
	pays := []model.Payment{pay("p1", "2025-01-05", 120_00)}
	states, overpaid := Allocate(obs, pays, model.Date("2025-02-01"))
	if states[0].Status != StatusPaid || overpaid != 20_00 {
		t.Fatalf("предоплата: %+v overpaid=%d", states[0], overpaid)
	}
	// Без выплат: просрочка на 22 дня (10.01 -> 01.02).
	states, _ = Allocate(obs, nil, model.Date("2025-02-01"))
	if states[0].Status != StatusOverdue || states[0].DaysOverdue != 22 {
		t.Fatalf("просрочка: %+v", states[0])
	}
	// В день срока — ещё не просрочено.
	states, _ = Allocate(obs, nil, model.Date("2025-01-10"))
	if states[0].Status != StatusPending {
		t.Fatalf("день срока: %+v", states[0])
	}
}

func TestAllocateStorno(t *testing.T) {
	obs := []model.Obligation{obl("o1", "2025-01-10", 100_00)}
	pays := []model.Payment{pay("p1", "2025-01-10", 100_00), pay("p2", "2025-01-11", -30_00)}
	states, overpaid := Allocate(obs, pays, model.Date("2025-01-12"))
	if overpaid != 0 || states[0].PaidCents != 70_00 || states[0].OpenCents != 30_00 || states[0].Status != StatusOverdue {
		t.Fatalf("сторно: %+v overpaid=%d", states[0], overpaid)
	}
}

func testDB() *model.DB {
	db := model.NewEmptyDB()
	db.Suppliers = []model.Supplier{{ID: "s1", Name: "АВИДО"}, {ID: "s2", Name: "МАВ"}}
	db.Obligations = []model.Obligation{
		obl("o1", "2025-01-10", 100_00),
		{ID: "o2", SupplierID: "s2", DueDate: "2025-01-31", AmountCents: 200_00},
		{ID: "o3", SupplierID: "s1", DueDate: "2025-02-05", AmountCents: 50_00},
	}
	db.Payments = []model.Payment{pay("p1", "2025-01-10", 40_00)}
	return db
}

func TestDashboardAndMatrix(t *testing.T) {
	db := testDB()
	d := BuildDashboard(db, model.Date("2025-01-31"))
	if d.TotalDebtCents != (100_00-40_00+50_00)+200_00 {
		t.Fatalf("total debt=%d", d.TotalDebtCents)
	}
	if d.OverdueCents != 60_00 { // o1: 100-40, срок 10.01 < 31.01; o2 в день срока не просрочено
		t.Fatalf("overdue=%d, want 6000", d.OverdueCents)
	}
	if d.DueTodayCents != 200_00 || d.DueWeekCents != 50_00 {
		t.Fatalf("today=%d week=%d", d.DueTodayCents, d.DueWeekCents)
	}
	if d.AccruedMonth != 300_00 || d.PaidMonthCents != 40_00 {
		t.Fatalf("month accrued=%d paid=%d", d.AccruedMonth, d.PaidMonthCents)
	}

	m := BuildMatrix(db, 2025, 1)
	if len(m.Days) != 31 || m.GrandRange != d.TotalDebtCents {
		t.Fatalf("matrix days=%d grand=%d", len(m.Days), m.GrandRange)
	}
	if m.DayTotals[9].SobstvCents != 100_00 || m.DayTotals[9].VyplatyCents != 40_00 {
		t.Fatalf("day totals 10.01: %+v", m.DayTotals[9])
	}
	mFeb := BuildMatrix(db, 2025, 2)
	if len(mFeb.Days) != 28 {
		t.Fatalf("февраль 2025: %d дней", len(mFeb.Days))
	}

	// Недельный диапазон через BuildMatrixRange + календарь РБ.
	// 2025-01-06 (пн) — переведённый выходной, 2025-01-07 — православное Рождество,
	// 2025-01-11 — рабочая суббота, 2025-01-12 — воскресенье.
	w := BuildMatrixRange(db, "2025-01-06", "2025-01-12")
	if len(w.Days) != 7 || w.Days[0].Weekday != 1 || w.Days[6].Weekday != 7 {
		t.Fatalf("неделя пн–вс: %+v", w.Days)
	}
	if !w.Days[0].IsDayOff || !w.Days[1].IsHoliday || w.Days[1].HolidayName == "" {
		t.Fatalf("выходной/праздник: %+v %+v", w.Days[0], w.Days[1])
	}
	if w.Days[5].IsDayOff || !w.Days[5].IsWorkdayTransfer {
		t.Fatalf("рабочая суббота 2025-01-11: %+v", w.Days[5])
	}
	if !w.Days[6].IsWeekend || !w.Days[6].IsDayOff {
		t.Fatalf("воскресенье: %+v", w.Days[6])
	}
	// Диапазон через границу месяца: 30.01–02.02 — сегменты месяцев в днях.
	x := BuildMatrixRange(db, "2025-01-30", "2025-02-02")
	if len(x.Days) != 4 || x.Days[0].Month != 1 || x.Days[3].Month != 2 || x.Days[3].Year != 2025 {
		t.Fatalf("граница месяцев: %+v", x.Days)
	}

	card := BuildSupplierCard(db, "s1", model.Date("2025-01-31"))
	if card == nil || card.BalanceCents != 110_00 || card.OverdueCents != 60_00 {
		t.Fatalf("card: %+v", card)
	}
	if got := card.Running[len(card.Running)-1].BalanceCents; got != 110_00 {
		t.Fatalf("running last=%d", got)
	}
}
