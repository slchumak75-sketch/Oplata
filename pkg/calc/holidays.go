package calc

// Рабочий календарь приложения (вариант 1 по решению владельца):
// субботы и воскресенья считаются выходными автоматически, а праздничные
// нерабочие дни берутся только из списка, введённого вручную на вкладке
// «Данные» (model.Settings.Holidays). Государственные праздники, Радуница
// и переносы рабочих дней из законодательства приложение само не подставляет.

import (
	"strings"
	"time"

	"github.com/slchumak75-sketch/oplata/pkg/model"
)

// HolidayName сообщает, входит ли дата в вручную ведённый список праздников,
// и возвращает её название.
func HolidayName(date model.Date, holidays []model.Holiday) (string, bool) {
	for _, h := range holidays {
		if h.Date == date {
			name := strings.TrimSpace(h.Name)
			if name == "" {
				name = "Праздничный день"
			}
			return name, true
		}
	}
	return "", false
}

// IsWeekend — суббота или воскресенье по календарю.
func IsWeekend(date model.Date) bool {
	wd := date.Time().Weekday()
	return wd == time.Saturday || wd == time.Sunday
}

// IsWorkingSaturday сохранён для поля матрицы is_workday_transfer.
// В ручном календаре рабочих суббот нет: все субботы остаются выходными.
func IsWorkingSaturday(date model.Date) bool {
	return false
}

// DayOffInfo определяет эффективный статус дня: вручную введённый праздник
// или календарная суббота/воскресенье — нерабочий день; остальные дни рабочие.
// name — подпись для подсказки GUI.
func DayOffInfo(date model.Date, holidays []model.Holiday) (dayOff bool, name string) {
	if holiday, ok := HolidayName(date, holidays); ok {
		return true, holiday
	}
	if IsWeekend(date) {
		return true, "Выходной день"
	}
	return false, ""
}

// NextWorkingDay возвращает саму дату, если она рабочая, иначе ближайший
// следующий рабочий день с учётом вручную введённых праздников.
func NextWorkingDay(date model.Date, holidays []model.Holiday) model.Date {
	for {
		dayOff, _ := DayOffInfo(date, holidays)
		if !dayOff {
			return date
		}
		date = date.AddDays(1)
	}
}

// PaymentDueDate считает срок оплаты поставки: дата поставки + отсрочка в днях,
// а затем перенос вперёд на ближайший рабочий день, если результат нерабочий.
func PaymentDueDate(delivery model.Date, deferralDays int, holidays []model.Holiday) model.Date {
	return NextWorkingDay(delivery.AddDays(deferralDays), holidays)
}

// DeliveryDateForDueDate восстанавливает дату поставки по известному сроку оплаты
// и отсрочке. Если срок был перенесён через несколько нерабочих дней, ему может
// соответствовать несколько дат поставки; выбирается самая ранняя из них — та,
// которую дала бы обычная поставка перед этим отрезком нерабочих дней. Для срока,
// вручную поставленного на нерабочий день, возвращается due - отсрочка.
func DeliveryDateForDueDate(due model.Date, deferralDays int, holidays []model.Holiday) model.Date {
	candidate := due.AddDays(-deferralDays)
	for {
		previous := candidate.AddDays(-1)
		if PaymentDueDate(previous, deferralDays, holidays) != due {
			return candidate
		}
		candidate = previous
	}
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
