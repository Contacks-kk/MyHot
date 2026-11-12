package reports

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func parseTime(value string) (int, int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid edition time %q", value)
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid edition time %q", value)
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid edition time %q", value)
	}
	return hour, minute, nil
}

func beijingAt(date, clock string, location *time.Location) (time.Time, error) {
	day, err := time.Parse("2006-01-02", date)
	if err != nil || day.Format("2006-01-02") != date {
		return time.Time{}, fmt.Errorf("invalid report date %q", date)
	}
	hour, minute, err := parseTime(clock)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, location), nil
}

func addDate(value string, days int) (string, error) {
	day, err := time.Parse("2006-01-02", value)
	if err != nil || day.Format("2006-01-02") != value {
		return "", fmt.Errorf("invalid date %q", value)
	}
	return day.AddDate(0, 0, days).Format("2006-01-02"), nil
}

func dueDaily(now time.Time, editionTime string, location *time.Location) (string, error) {
	today := now.In(location).Format("2006-01-02")
	due, err := beijingAt(today, editionTime, location)
	if err != nil {
		return "", err
	}
	if !now.Before(due) {
		return today, nil
	}
	return addDate(today, -1)
}

func dueWeekly(now time.Time, editionTime string, location *time.Location) (string, error) {
	now = now.In(location)
	today := now.Format("2006-01-02")
	day, _ := time.Parse("2006-01-02", today)
	daysFromMonday := (int(day.Weekday()) + 6) % 7
	dueToday, err := beijingAt(today, editionTime, location)
	if err != nil {
		return "", err
	}
	back := daysFromMonday + 7
	if daysFromMonday == 0 && now.Before(dueToday) {
		back = 14
	}
	completeWeekStart, err := addDate(today, -back)
	if err != nil {
		return "", err
	}
	start, _ := time.Parse("2006-01-02", completeWeekStart)
	year, week := start.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week), nil
}

func dueMonthly(now time.Time, editionTime string, location *time.Location) (string, error) {
	now = now.In(location)
	today := now.Format("2006-01-02")
	day, _ := time.Parse("2006-01-02", today)
	dueToday, err := beijingAt(today, editionTime, location)
	if err != nil {
		return "", err
	}
	back := 1
	if day.Day() == 1 && now.Before(dueToday) {
		back = 2
	}
	month := time.Date(day.Year(), day.Month()-time.Month(back), 1, 0, 0, 0, 0, location)
	return month.Format("2006-01"), nil
}

var isoWeekPattern = regexp.MustCompile(`^(\d{4})-W(\d{2})$`)

func isoWeekRange(label string) (string, string, bool) {
	match := isoWeekPattern.FindStringSubmatch(label)
	if match == nil {
		return "", "", false
	}
	year, _ := strconv.Atoi(match[1])
	week, _ := strconv.Atoi(match[2])
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
	mondayOffset := (int(jan4.Weekday()) + 6) % 7
	start := jan4.AddDate(0, 0, -mondayOffset+7*(week-1))
	checkYear, checkWeek := start.ISOWeek()
	if checkYear != year || checkWeek != week || week < 1 || week > 53 {
		return "", "", false
	}
	return start.Format("2006-01-02"), start.AddDate(0, 0, 6).Format("2006-01-02"), true
}

func monthRange(label string) (string, string, bool) {
	if !regexp.MustCompile(`^\d{4}-\d{2}$`).MatchString(label) {
		return "", "", false
	}
	start, err := time.Parse("2006-01-02", label+"-01")
	if err != nil || start.Format("2006-01") != label {
		return "", "", false
	}
	return start.Format("2006-01-02"), start.AddDate(0, 1, -1).Format("2006-01-02"), true
}

func nextWeek(label string) (string, error) {
	start, _, ok := isoWeekRange(label)
	if !ok {
		return "", fmt.Errorf("bad week label %s", label)
	}
	day, _ := time.Parse("2006-01-02", start)
	year, week := day.AddDate(0, 0, 7).ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week), nil
}

func nextMonth(label string) (string, error) {
	start, _, ok := monthRange(label)
	if !ok {
		return "", fmt.Errorf("bad month label %s", label)
	}
	day, _ := time.Parse("2006-01-02", start)
	return day.AddDate(0, 1, 0).Format("2006-01"), nil
}
