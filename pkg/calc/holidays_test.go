package calc

import (
	"testing"

	"github.com/slchumak75-sketch/oplata/pkg/model"
)

func TestOrthodoxEasterAndRadunitsa(t *testing.T) {
	cases := []struct {
		year              int
		easter, radunitsa model.Date
	}{
		{2025, "2025-04-20", "2025-04-29"},
		{2026, "2026-04-12", "2026-04-21"},
		{2027, "2027-05-02", "2027-05-11"},
		{2024, "2024-05-05", "2024-05-14"},
	}
	for _, tc := range cases {
		if got := OrthodoxEaster(tc.year); got != tc.easter {
			t.Errorf("Пасха %d: получено %s, ожидалось %s", tc.year, got, tc.easter)
		}
		if got := Radunitsa(tc.year); got != tc.radunitsa {
			t.Errorf("Радуница %d: получено %s, ожидалось %s", tc.year, got, tc.radunitsa)
		}
	}
}

func TestDayOffInfoBelarus(t *testing.T) {
	off := []model.Date{
		"2025-01-01", "2025-01-02", "2025-01-07", "2025-03-08", "2025-04-29",
		"2025-05-01", "2025-05-09", "2025-07-03", "2025-11-07", "2025-12-25",
		"2025-01-04", // суббота
		"2025-01-05", // воскресенье
		// Переносы 2025 (постановление № 839): будни становятся выходными.
		"2025-01-06", "2025-04-28", "2025-07-04", "2025-12-26",
		// Переносы 2026 (№ 397) и 2027 (№ 499).
		"2026-04-20", "2027-05-10",
	}
	for _, d := range off {
		if dayOff, _ := DayOffInfo(d); !dayOff {
			t.Errorf("%s должен быть нерабочим днём", d)
		}
	}
	work := []model.Date{
		"2025-01-08", // обычная среда
		"2025-01-11", // рабочая суббота за 06.01
		"2025-04-26", // рабочая суббота за 28.04
		"2025-07-12", // рабочая суббота за 04.07
		"2025-12-20", // рабочая суббота за 26.12
		"2026-04-25", // рабочая суббота за 20.04
		"2027-05-08", // рабочая суббота за 10.05
	}
	for _, d := range work {
		if dayOff, name := DayOffInfo(d); dayOff {
			t.Errorf("%s должен быть рабочим днём, получено выходной (%s)", d, name)
		}
	}
	if name, ok := HolidayName("2026-04-21"); !ok || name == "" {
		t.Errorf("Радуница 2026 не распознана: %q %v", name, ok)
	}
	if _, ok := HolidayName("2025-06-12"); ok {
		t.Error("12 июня не является праздником РБ")
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
