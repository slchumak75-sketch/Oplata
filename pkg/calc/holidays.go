package calc

// Нерабочие дни Республики Беларусь для подсветки столбцов «Таблицы»:
// выходные (суббота/воскресенье при пятидневке), государственные праздники
// и праздничные дни (Указ Президента РБ от 26.03.1998 № 157) с учётом ежегодных
// переносов рабочих дней постановлениями Совета Министров.
//
// ВНИМАНИЕ сопровождающим: таблица переносов конечна (2025–2027). После выхода
// очередного постановления Совмина (обычно осенью) добавить год в workdayTransfers
// и тест в holidays_test.go. Организации вправе переносить дни иначе — календарь
// приложения повторяет официальный график для пятидневной рабочей недели.

import (
	"time"

	"github.com/slchumak75-sketch/oplata/pkg/model"
)

// fixedHolidays — нерабочие праздничные дни с фиксированной датой: [месяц][день] -> название.
var fixedHolidays = map[[2]int]string{
	{1, 1}:   "Новый год",
	{1, 2}:   "Новый год",
	{1, 7}:   "Рождество Христово (православное Рождество)",
	{3, 8}:   "День женщин",
	{5, 1}:   "Праздник труда",
	{5, 9}:   "День Победы",
	{7, 3}:   "День Независимости Республики Беларусь",
	{11, 7}:  "День Октябрьской революции",
	{12, 25}: "Рождество Христово (католическое Рождество)",
}

// workdayTransfers — переносы рабочих дней: выходной будний день -> рабочая суббота.
// 2025: постановление Совмина от 14.11.2024 № 839.
// 2026: постановление Совмина от 22.07.2025 № 397.
// 2027: постановление Совмина от 03.10.2026 № 499.
var workdayTransfers = map[model.Date]model.Date{
	"2025-01-06": "2025-01-11",
	"2025-04-28": "2025-04-26",
	"2025-07-04": "2025-07-12",
	"2025-12-26": "2025-12-20",
	"2026-04-20": "2026-04-25",
	"2027-05-10": "2027-05-08",
}

// workingSaturdays — обратный индекс переносов: суббота, которая является рабочей.
var workingSaturdays = func() map[model.Date]bool {
	m := make(map[model.Date]bool, len(workdayTransfers))
	for _, sat := range workdayTransfers {
		m[sat] = true
	}
	return m
}()

// OrthodoxEaster возвращает дату православной Пасхи (григорианский календарь)
// по юлианской пасхалии (алгоритм Миуса для юлианского календаря + сдвиг на григорианский).
func OrthodoxEaster(year int) model.Date {
	a := year % 4
	b := year % 7
	c := year % 19
	d := (19*c + 15) % 30
	e := (2*a + 4*b - d + 34) % 7
	month := (d + e + 114) / 31 // 3 = март, 4 = апрель (юлианский календарь)
	day := (d+e+114)%31 + 1
	julian := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	// Разница юлианского и григорианского календарей для дат после февраля.
	offset := year/100 - year/400 - 2
	return model.FromTime(julian.AddDate(0, 0, offset))
}

// Radunitsa возвращает дату Радуницы: вторник второй недели после православной Пасхи
// (Пасха + 9 дней). Контроль: 2025-04-29, 2026-04-21, 2027-05-11.
func Radunitsa(year int) model.Date {
	return OrthodoxEaster(year).AddDays(9)
}

// HolidayName сообщает, является ли дата государственным праздником/праздничным днём РБ,
// и возвращает его название. Переносы рабочих дней здесь не учитываются (см. DayOffInfo).
func HolidayName(date model.Date) (string, bool) {
	year, month := date.YearMonth()
	if name, ok := fixedHolidays[[2]int{month, date.Time().Day()}]; ok {
		return name, true
	}
	if date == Radunitsa(year) {
		return "Радуница (по календарю православной конфессии)", true
	}
	return "", false
}

// IsWeekend — суббота или воскресенье по календарю (без учёта переносов).
func IsWeekend(date model.Date) bool {
	wd := date.Time().Weekday()
	return wd == time.Saturday || wd == time.Sunday
}

// IsWorkingSaturday — суббота, ставшая рабочей из-за переноса рабочего дня.
func IsWorkingSaturday(date model.Date) bool {
	return workingSaturdays[date]
}

// DayOffInfo определяет эффективный статус дня для пятидневки:
// dayOff=true для выходных, праздников и будней, перенесённых на субботу;
// рабочие субботы (переносы) возвращают dayOff=false. name — подпись для подсказки GUI.
func DayOffInfo(date model.Date) (dayOff bool, name string) {
	if IsWorkingSaturday(date) {
		return false, "Рабочая суббота (перенос рабочего дня)"
	}
	if holiday, ok := HolidayName(date); ok {
		return true, holiday
	}
	if _, transferred := workdayTransfers[date]; transferred {
		return true, "Выходной (рабочий день перенесён на субботу)"
	}
	if IsWeekend(date) {
		return true, "Выходной день"
	}
	return false, ""
}

// NextWorkingDay возвращает саму дату, если она рабочая, иначе ближайший
// следующий рабочий день по календарю РБ (выходные, праздники и переносы).
// Используется для срока оплаты поставки: дата поставки + отсрочка не должна
// оставаться на нерабочем дне.
func NextWorkingDay(date model.Date) model.Date {
	for {
		dayOff, _ := DayOffInfo(date)
		if !dayOff {
			return date
		}
		date = date.AddDays(1)
	}
}

// PaymentDueDate считает срок оплаты поставки: дата поставки + отсрочка в днях,
// а затем перенос вперёд на ближайший рабочий день, если результат нерабочий.
func PaymentDueDate(delivery model.Date, deferralDays int) model.Date {
	return NextWorkingDay(delivery.AddDays(deferralDays))
}

// WeekdayMon1 возвращает день недели: 1 = понедельник … 7 = воскресенье.
func WeekdayMon1(date model.Date) int {
	// time.Weekday: 0 = воскресенье … 6 = суббота.
	return (int(date.Time().Weekday())+6)%7 + 1
}

// MondayOf возвращает понедельник недели, содержащей дату (неделя Пн–Вс).
func MondayOf(date model.Date) model.Date {
	return date.AddDays(-(WeekdayMon1(date) - 1))
}
