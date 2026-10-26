package calc

// Рабочий календарь приложения: субботы и воскресенья по умолчанию выходные,
// остальные дни по умолчанию рабочие. На вкладке «Данные» пользователь вручную
// отмечает исключения: Settings.Holidays делает дату нерабочей, а
// Settings.WorkingDays — рабочей (включая субботу и воскресенье). Даты из
// законодательства приложение само не подставляет.

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

// IsManualWorkingDay сообщает, отмечена ли дата рабочей в календаре «Данные».
func IsManualWorkingDay(date model.Date, settings model.Settings) bool {
	for _, d := range settings.WorkingDays {
		if d == date {
			return true
		}
	}
	return false
}

// IsWorkingSaturday сохранён для поля матрицы is_workday_transfer: теперь это
// суббота или воскресенье, вручную отмеченные рабочим днём.
func IsWorkingSaturday(date model.Date, settings model.Settings) bool {
	return IsWeekend(date) && IsManualWorkingDay(date, settings)
}

// DayOffInfo определяет эффективный статус дня. Вручную отмеченный рабочий день
// имеет приоритет; далее ручной праздник или календарные суббота/воскресенье
// считаются нерабочими. name — подпись для подсказки GUI.
func DayOffInfo(date model.Date, settings model.Settings) (dayOff bool, name string) {
	if IsManualWorkingDay(date, settings) {
		return false, "Рабочий день"
	}
	if holiday, ok := HolidayName(date, settings.Holidays); ok {
		return true, holiday
	}
	if IsWeekend(date) {
		return true, "Выходной день"
	}
	return false, ""
}

// NextWorkingDay возвращает саму дату, если она рабочая, иначе ближайший
// следующий рабочий день по ручному календарю выходных/рабочих дней.
func NextWorkingDay(date model.Date, settings model.Settings) model.Date {
	for {
		dayOff, _ := DayOffInfo(date, settings)
		if !dayOff {
			return date
		}
		date = date.AddDays(1)
	}
}

// PaymentDueDate считает срок оплаты поставки: дата поставки + отсрочка в днях,
// а затем перенос вперёд на ближайший рабочий день, если результат нерабочий.
func PaymentDueDate(delivery model.Date, deferralDays int, settings model.Settings) model.Date {
	return NextWorkingDay(delivery.AddDays(deferralDays), settings)
}

// DeliveryDateForDueDate восстанавливает дату поставки по известному сроку оплаты
// и отсрочке. Если срок был перенесён через несколько нерабочих дней, ему может
// соответствовать несколько дат поставки; выбирается самая ранняя из них — та,
// которую дала бы обычная поставка перед этим отрезком нерабочих дней. Для срока,
// вручную поставленного на нерабочий день, возвращается due - отсрочка.
func DeliveryDateForDueDate(due model.Date, deferralDays int, settings model.Settings) model.Date {
	candidate := due.AddDays(-deferralDays)
	for {
		previous := candidate.AddDays(-1)
		if PaymentDueDate(previous, deferralDays, settings) != due {
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
