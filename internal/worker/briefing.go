package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xtawa/classing-backend/internal/store"
)

type briefingLesson struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Teacher     string `json:"teacher"`
	Location    string `json:"location"`
	Note        string `json:"note"`
	DayOfWeek   int    `json:"dayOfWeek"`
	StartMinute int    `json:"startMinute"`
	EndMinute   int    `json:"endMinute"`
	StartTime   string `json:"startTime"`
	EndTime     string `json:"endTime"`
	StartWeek   int    `json:"startWeek"`
	EndWeek     int    `json:"endWeek"`
	WeekParity  string `json:"weekParity"`
}

type briefingException struct {
	ID          string `json:"id"`
	LessonID    string `json:"lessonId"`
	Type        string `json:"type"`
	Date        string `json:"date"`
	Title       string `json:"title"`
	Teacher     string `json:"teacher"`
	Location    string `json:"location"`
	Note        string `json:"note"`
	DayOfWeek   *int   `json:"dayOfWeek"`
	StartMinute *int   `json:"startMinute"`
	EndMinute   *int   `json:"endMinute"`
}

type briefingDigest struct {
	TargetDate string
	Available  bool
	Source     string
	Lessons    []briefingLesson
}

type cloudRecord struct {
	ID        string          `json:"id"`
	Payload   string          `json:"payload"`
	DeletedAt json.RawMessage `json:"deletedAt"`
}

type briefingDocument struct {
	Lessons    []briefingLesson    `json:"lessons"`
	Exceptions []briefingException `json:"exceptions"`
	Timetable  *struct {
		Lessons               []briefingLesson    `json:"lessons"`
		BaseLessons           []briefingLesson    `json:"baseLessons"`
		Exceptions            []briefingException `json:"exceptions"`
		WeekNumberMode        string              `json:"weekNumberMode"`
		SemesterWeekStartDate string              `json:"semesterWeekStartDate"`
	} `json:"timetable"`
	MobileSettings *struct {
		Settings map[string]any `json:"settings"`
	} `json:"mobileSettings"`
	Records map[string][]cloudRecord `json:"records"`
}

type briefingSchedule struct {
	Lessons               []briefingLesson
	Exceptions            []briefingException
	WeekNumberMode        string
	SemesterWeekStartDate string
	WeekStartDay          string
}

func (w *Worker) buildBriefingDigest(ctx context.Context, job store.ClaimedJob) briefingDigest {
	targetDate := briefingTargetDate(job.TargetDate)
	digest := briefingDigest{TargetDate: targetDate.Format("2006-01-02")}
	if cloud, err := w.store.CloudDocument(ctx, job.UserID); err == nil {
		if schedule, recognized, parseErr := parseBriefingDocument([]byte(cloud.Payload), nil); parseErr == nil && recognized {
			digest.Available = true
			digest.Source = "Classing 官方云同步"
			digest.Lessons = schedule.lessonsForDate(targetDate)
			return digest
		} else if parseErr != nil {
			w.log.Warn("parse briefing cloud timetable", "job_id", job.ID, "user_id", job.UserID, "error", parseErr)
		}
	} else if err != store.ErrNotFound {
		w.log.Warn("load briefing cloud timetable", "job_id", job.ID, "user_id", job.UserID, "error", err)
	}

	projects, _, err := w.store.ListTimetables(ctx, job.UserID, false, 1, 0)
	if err != nil {
		w.log.Warn("load briefing timetable project", "job_id", job.ID, "user_id", job.UserID, "error", err)
		return digest
	}
	if len(projects) == 0 {
		return digest
	}
	project := projects[0]
	defaults := &briefingSchedule{WeekNumberMode: "SEMESTER", SemesterWeekStartDate: project.SemesterStart, WeekStartDay: "MONDAY"}
	schedule, recognized, err := parseBriefingDocument([]byte(project.Document), defaults)
	if err != nil {
		w.log.Warn("parse briefing timetable project", "job_id", job.ID, "user_id", job.UserID, "project_id", project.ID, "error", err)
		return digest
	}
	if recognized {
		digest.Available = true
		digest.Source = "Classing 课表项目「" + project.Name + "」"
		digest.Lessons = schedule.lessonsForDate(targetDate)
	}
	return digest
}

func briefingTargetDate(raw string) time.Time {
	if len(raw) >= len("2006-01-02") {
		if parsed, err := time.Parse("2006-01-02", raw[:10]); err == nil {
			return parsed
		}
	}
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

func parseBriefingDocument(raw []byte, defaults *briefingSchedule) (briefingSchedule, bool, error) {
	schedule := briefingSchedule{WeekNumberMode: "NATURAL", WeekStartDay: "MONDAY"}
	if defaults != nil {
		schedule = *defaults
	}
	var doc briefingDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return schedule, false, err
	}
	if records, ok := doc.Records["timetable.lessons"]; ok {
		schedule.Lessons = decodeLiveRecords[briefingLesson](records)
		schedule.Exceptions = decodeLiveRecords[briefingException](doc.Records["timetable.exceptions"])
		settings := decodeSettingRecords(doc.Records["mobile.settings"])
		schedule.WeekNumberMode = stringSetting(settings, "weekNumberMode", schedule.WeekNumberMode)
		schedule.SemesterWeekStartDate = stringSetting(settings, "semesterWeekStartDate", schedule.SemesterWeekStartDate)
		schedule.WeekStartDay = stringSetting(settings, "weekStartDay", schedule.WeekStartDay)
		return schedule, true, nil
	}
	if doc.Timetable != nil {
		schedule.Lessons = doc.Timetable.BaseLessons
		if schedule.Lessons == nil {
			schedule.Lessons = doc.Timetable.Lessons
		}
		schedule.Exceptions = doc.Timetable.Exceptions
		schedule.WeekNumberMode = nonBlank(doc.Timetable.WeekNumberMode, schedule.WeekNumberMode)
		schedule.SemesterWeekStartDate = nonBlank(doc.Timetable.SemesterWeekStartDate, schedule.SemesterWeekStartDate)
		if doc.MobileSettings != nil {
			schedule.WeekNumberMode = anyString(doc.MobileSettings.Settings["weekNumberMode"], schedule.WeekNumberMode)
			schedule.SemesterWeekStartDate = anyString(doc.MobileSettings.Settings["semesterWeekStartDate"], schedule.SemesterWeekStartDate)
			schedule.WeekStartDay = anyString(doc.MobileSettings.Settings["weekStartDay"], schedule.WeekStartDay)
		}
		return schedule, true, nil
	}
	if doc.Lessons != nil {
		schedule.Lessons = doc.Lessons
		schedule.Exceptions = doc.Exceptions
		return schedule, true, nil
	}
	return schedule, false, nil
}

func decodeLiveRecords[T any](records []cloudRecord) []T {
	items := make([]T, 0, len(records))
	for _, record := range records {
		deleted := strings.TrimSpace(string(record.DeletedAt))
		if record.Payload == "" || (deleted != "" && deleted != "null") {
			continue
		}
		var item T
		if json.Unmarshal([]byte(record.Payload), &item) == nil {
			items = append(items, item)
		}
	}
	return items
}

func decodeSettingRecords(records []cloudRecord) map[string]any {
	settings := map[string]any{}
	for _, record := range records {
		deleted := strings.TrimSpace(string(record.DeletedAt))
		if record.ID == "" || record.Payload == "" || (deleted != "" && deleted != "null") {
			continue
		}
		var wrapper struct {
			Value any `json:"value"`
		}
		if json.Unmarshal([]byte(record.Payload), &wrapper) == nil {
			settings[record.ID] = wrapper.Value
		}
	}
	return settings
}

func (schedule briefingSchedule) lessonsForDate(date time.Time) []briefingLesson {
	week := schedule.weekIndex(date)
	day := weekdayNumber(date)
	lessons := make([]briefingLesson, 0)
	for _, lesson := range schedule.Lessons {
		normalizeLesson(&lesson)
		if lesson.Title == "" || lesson.DayOfWeek != day || week < lesson.StartWeek || week > lesson.EndWeek {
			continue
		}
		parity := strings.ToUpper(lesson.WeekParity)
		if (parity == "ODD" && week%2 == 0) || (parity == "EVEN" && week%2 != 0) {
			continue
		}
		lessons = append(lessons, lesson)
	}
	for _, exception := range schedule.Exceptions {
		if exception.Date != date.Format("2006-01-02") {
			continue
		}
		kind := strings.ToUpper(exception.Type)
		var original *briefingLesson
		if exception.LessonID != "" {
			for index := range lessons {
				if lessons[index].ID == exception.LessonID {
					copy := lessons[index]
					original = &copy
					break
				}
			}
		}
		if exception.LessonID != "" && (kind == "CANCEL" || kind == "RESCHEDULE") {
			lessons = removeLesson(lessons, exception.LessonID)
		}
		if kind != "RESCHEDULE" && kind != "MAKE_UP" {
			continue
		}
		lesson := briefingLesson{ID: exception.LessonID, Title: exception.Title, Teacher: exception.Teacher, Location: exception.Location, Note: exception.Note, DayOfWeek: day, StartWeek: week, EndWeek: week, WeekParity: "ALL"}
		if original != nil {
			lesson = *original
			lesson.DayOfWeek = day
			lesson.StartWeek = week
			lesson.EndWeek = week
			lesson.WeekParity = "ALL"
			lesson.Title = nonBlank(exception.Title, lesson.Title)
			lesson.Teacher = nonBlank(exception.Teacher, lesson.Teacher)
			lesson.Location = nonBlank(exception.Location, lesson.Location)
			lesson.Note = nonBlank(exception.Note, lesson.Note)
		}
		if exception.DayOfWeek != nil {
			lesson.DayOfWeek = *exception.DayOfWeek
		}
		if exception.StartMinute != nil {
			lesson.StartMinute = *exception.StartMinute
		}
		if exception.EndMinute != nil {
			lesson.EndMinute = *exception.EndMinute
		}
		if lesson.Title == "" {
			lesson.Title = "未命名课程"
		}
		normalizeLesson(&lesson)
		lessons = append(lessons, lesson)
	}
	sort.SliceStable(lessons, func(i, j int) bool {
		if lessons[i].StartMinute != lessons[j].StartMinute {
			return lessons[i].StartMinute < lessons[j].StartMinute
		}
		return lessons[i].Title < lessons[j].Title
	})
	return lessons
}

func normalizeLesson(lesson *briefingLesson) {
	if lesson.StartMinute == 0 && lesson.StartTime != "" {
		lesson.StartMinute = parseClockMinute(lesson.StartTime)
	}
	if lesson.EndMinute == 0 && lesson.EndTime != "" {
		lesson.EndMinute = parseClockMinute(lesson.EndTime)
	}
	if lesson.EndMinute <= lesson.StartMinute {
		lesson.EndMinute = lesson.StartMinute + 90
	}
	if lesson.StartWeek < 1 {
		lesson.StartWeek = 1
	}
	if lesson.EndWeek < lesson.StartWeek {
		lesson.EndWeek = 30
	}
	lesson.Title = strings.TrimSpace(lesson.Title)
}

func parseClockMinute(value string) int {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed.Hour()*60 + parsed.Minute()
}

func removeLesson(items []briefingLesson, lessonID string) []briefingLesson {
	result := items[:0]
	for _, item := range items {
		if item.ID != lessonID {
			result = append(result, item)
		}
	}
	return result
}

func (schedule briefingSchedule) weekIndex(date time.Time) int {
	startDay := parseWeekday(schedule.WeekStartDay)
	if strings.EqualFold(schedule.WeekNumberMode, "SEMESTER") {
		anchor, err := time.Parse("2006-01-02", schedule.SemesterWeekStartDate)
		if err == nil {
			anchor = startOfWeek(anchor, startDay)
			days := int(date.Sub(anchor).Hours() / 24)
			return floorDiv(days, 7) + 1
		}
	}
	return weekOfWeekBasedYear(date, startDay)
}

func floorDiv(value, divisor int) int {
	quotient := value / divisor
	if value%divisor != 0 && ((value < 0) != (divisor < 0)) {
		quotient--
	}
	return quotient
}

func weekOfWeekBasedYear(date time.Time, startDay time.Weekday) int {
	weekStart := startOfWeek(date, startDay)
	first := firstWeekStart(date.Year(), startDay)
	if weekStart.Before(first) {
		first = firstWeekStart(date.Year()-1, startDay)
	} else if next := firstWeekStart(date.Year()+1, startDay); !weekStart.Before(next) {
		return 1
	}
	return int(weekStart.Sub(first).Hours()/24)/7 + 1
}

func firstWeekStart(year int, startDay time.Weekday) time.Time {
	jan1 := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	start := startOfWeek(jan1, startDay)
	daysBefore := int(jan1.Sub(start).Hours() / 24)
	if 7-daysBefore < 4 {
		start = start.AddDate(0, 0, 7)
	}
	return start
}

func startOfWeek(date time.Time, startDay time.Weekday) time.Time {
	offset := (7 + int(date.Weekday()) - int(startDay)) % 7
	return time.Date(date.Year(), date.Month(), date.Day()-offset, 0, 0, 0, 0, time.UTC)
}

func parseWeekday(value string) time.Weekday {
	weekdays := map[string]time.Weekday{"SUNDAY": time.Sunday, "MONDAY": time.Monday, "TUESDAY": time.Tuesday, "WEDNESDAY": time.Wednesday, "THURSDAY": time.Thursday, "FRIDAY": time.Friday, "SATURDAY": time.Saturday}
	if day, ok := weekdays[strings.ToUpper(strings.TrimSpace(value))]; ok {
		return day
	}
	return time.Monday
}

func weekdayNumber(date time.Time) int {
	if date.Weekday() == time.Sunday {
		return 7
	}
	return int(date.Weekday())
}

func renderBriefingMail(username string, digest briefingDigest) string {
	var body strings.Builder
	fmt.Fprintf(&body, "你好 %s，\r\n\r\n", username)
	if !digest.Available {
		fmt.Fprintf(&body, "这是 %s 的 Classing 课程简报。\r\n\r\n尚未读取到已同步的课表数据，请先将课表同步到 Classing 官方云。\r\n", digest.TargetDate)
		return body.String()
	}
	if len(digest.Lessons) == 0 {
		fmt.Fprintf(&body, "%s 当天没有课程安排。\r\n\r\n课表来源：%s\r\n", digest.TargetDate, digest.Source)
		return body.String()
	}
	fmt.Fprintf(&body, "%s 共有 %d 节课程：\r\n\r\n", digest.TargetDate, len(digest.Lessons))
	for index, lesson := range digest.Lessons {
		fmt.Fprintf(&body, "%d. %s–%s  %s\r\n", index+1, formatMinute(lesson.StartMinute), formatMinute(lesson.EndMinute), lesson.Title)
		if lesson.Location != "" {
			fmt.Fprintf(&body, "   地点：%s\r\n", lesson.Location)
		}
		if lesson.Teacher != "" {
			fmt.Fprintf(&body, "   教师：%s\r\n", lesson.Teacher)
		}
		if lesson.Note != "" {
			fmt.Fprintf(&body, "   备注：%s\r\n", lesson.Note)
		}
	}
	fmt.Fprintf(&body, "\r\n课表来源：%s\r\n", digest.Source)
	return body.String()
}

func formatMinute(minute int) string {
	if minute < 0 {
		minute = 0
	}
	if minute > 24*60-1 {
		minute = 24*60 - 1
	}
	return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
}

func stringSetting(settings map[string]any, key, fallback string) string {
	return anyString(settings[key], fallback)
}

func anyString(value any, fallback string) string {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return text
	}
	return fallback
}

func nonBlank(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
