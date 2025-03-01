package utils

import "time"

// WeekdayToRussian converts weekday to Russian name
func WeekdayToRussian(w time.Weekday) string {
	switch w {
	case time.Monday:
		return "понедельник"
	case time.Tuesday:
		return "вторник"
	case time.Wednesday:
		return "среду"
	case time.Thursday:
		return "четверг"
	case time.Friday:
		return "пятницу"
	case time.Saturday:
		return "субботу"
	case time.Sunday:
		return "воскресенье"
	default:
		return w.String()
	}
}
