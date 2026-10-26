package calc

import (
	"testing"

	"github.com/slchumak75-sketch/oplata/pkg/model"
)

var manualHolidaysForTest = []model.Holiday{
	{Date: "2025-01-04", Name: "Праздник в субботу"},
	{Date: "2025-01-06", Name: "Ручной нерабочий понедельник"},
	{Date: "2025-01-07", Name: "Ручной праздник"},
}

var manualCalendarForTest = model.Settings{Holidays: manualHolidaysForTest, WorkingDays: []model.Date{}}

func TestDayOffInfoManualCalendar(t *testing.T) {
	off := []model.Date{
		"2025-01-04", // суббота
		"2025-01-05", // воскресенье
		"2025-01-06", // вручную введённый нерабочий день
		"2025-01-07", // вручную введённый праздник
	}
	for _, d := range off {
		if dayOff, _ := DayOffInfo(d, manualCalendarForTest); !dayOff {
			t.Errorf("%s должен быть нерабочим днём", d)
		}
	}
	work := []model.Date{
		"2025-01-01", // законодательный праздник НЕ подставляется при пустом ручном списке
		"2025-01-08", // обычная среда
		"2025-05-01", // будний день; станет нерабочим только после ручного ввода
	}
	for _, d := range work {
		if dayOff, name := DayOffInfo(d, model.Settings{}); dayOff {
			t.Errorf("%s без ручного праздника должен быть рабочим днём, получено выходной (%s)", d, name)
		}
	}
	// Без ручной отметки суббота остаётся обычной выходной; дата из WorkingDays
	// делает её рабочей.
	if dayOff, _ := DayOffInfo("2025-01-11", model.Settings{}); !dayOff {
		t.Error("2025-01-11 должна быть обычной выходной субботой")
	}
	workingSaturday := model.Settings{WorkingDays: []model.Date{"2025-01-11"}}
	if dayOff, _ := DayOffInfo("2025-01-11", workingSaturday); dayOff || !IsWorkingSaturday("2025-01-11", workingSaturday) {
		t.Error("2025-01-11 должна стать вручную отмеченной рабочей субботой")
	}
	if name, ok := HolidayName("2025-01-07", manualHolidaysForTest); !ok || name != "Ручной праздник" {
		t.Errorf("ручной праздник не распознан: %q %v", name, ok)
	}
	if _, ok := HolidayName("2025-01-07", nil); ok {
		t.Error("без ручного ввода дата не должна считаться праздником")
	}
}

func TestNextWorkingDayAndPaymentDueDate(t *testing.T) {
	cases := []struct{ in, want model.Date }{
		{"2025-01-08", "2025-01-08"}, // обычный рабочий день не сдвигается
		{"2025-01-04", "2025-01-08"}, // сб → вс → два ручных праздника → ср
		{"2025-01-11", "2025-01-13"}, // обычная суббота → понедельник
	}
	for _, tc := range cases {
		if got := NextWorkingDay(tc.in, manualCalendarForTest); got != tc.want {
			t.Errorf("NextWorkingDay(%s)=%s, ожидалось %s", tc.in, got, tc.want)
		}
	}
	// Без ручного списка 1 января — рабочий будний день.
	if got := PaymentDueDate("2025-01-01", 0, model.Settings{}); got != "2025-01-01" {
		t.Fatalf("PaymentDueDate без ручных праздников=%s", got)
	}
	// Поставка 21.12.2024 + 14 дней = суббота 04.01.2025; с ручными праздниками срок — 08.01.2025.
	if got := PaymentDueDate("2024-12-21", 14, manualCalendarForTest); got != "2025-01-08" {
		t.Fatalf("PaymentDueDate=%s", got)
	}
	// Если суббота вручную отмечена рабочей, тот же срок остаётся на 04.01.2025.
	workingCalendar := model.Settings{WorkingDays: []model.Date{"2025-01-04"}}
	if got := PaymentDueDate("2024-12-21", 14, workingCalendar); got != "2025-01-04" {
		t.Fatalf("PaymentDueDate с рабочей субботой=%s", got)
	}
	// Обратный расчёт для ручного ввода в «Таблице»: самая ранняя поставка,
	// дающая перенесённый срок 08.01.2025 при отсрочке 14 дней, — 21.12.2024.
	if got := DeliveryDateForDueDate("2025-01-08", 14, manualCalendarForTest); got != "2024-12-21" {
		t.Fatalf("DeliveryDateForDueDate(перенесённый срок)=%s", got)
	}
	if got := DeliveryDateForDueDate("2025-01-15", 14, manualCalendarForTest); got != "2025-01-01" {
		t.Fatalf("DeliveryDateForDueDate(обычный срок)=%s", got)
	}
	// Срок вручную оставлен на нерабочем дне: дата поставки = срок - отсрочка.
	if got := DeliveryDateForDueDate("2025-01-04", 14, manualCalendarForTest); got != "2024-12-21" {
		t.Fatalf("DeliveryDateForDueDate(нерабочий срок)=%s", got)
	}
}

func TestMondayOf(t *testing.T) {
	// 2026-10-21 — среда; понедельник той же недели — 19 октября.
	if got := MondayOf("2026-10-21"); got != "2026-10-19" {
		t.Fatalf("MondayOf(среда)=%s", got)
	}
	if got := MondayOf("2026-10-19"); got != "2026-10-19" {
		t.Fatalf("MondayOf(понедельник)=%s", got)
	}
	// Воскресенье относится к той же неделе (Пн–Вс), а не к следующей.
	if got := MondayOf("2026-10-25"); got != "2026-10-19" {
		t.Fatalf("MondayOf(воскресенье)=%s", got)
	}
	if WeekdayMon1("2026-10-19") != 1 || WeekdayMon1("2026-10-25") != 7 {
		t.Fatal("WeekdayMon1 на границах недели")
	}
}
