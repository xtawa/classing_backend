package worker

import (
	"strings"
	"testing"
	"time"
)

func TestBriefingDigestReadsCloudV2AndAppliesExceptions(t *testing.T) {
	raw := []byte(`{
		"records": {
			"timetable.lessons": [
				{"id":"math","payload":"{\"id\":\"math\",\"title\":\"高等数学\",\"teacher\":\"陈老师\",\"location\":\"A101\",\"dayOfWeek\":6,\"startMinute\":480,\"endMinute\":570,\"startWeek\":1,\"endWeek\":30,\"weekParity\":\"ALL\"}","deletedAt":null},
				{"id":"old","payload":"{\"id\":\"old\",\"title\":\"已删除\",\"dayOfWeek\":6}","deletedAt":1}
			],
			"timetable.exceptions": [
				{"id":"cancel","payload":"{\"id\":\"cancel\",\"lessonId\":\"math\",\"type\":\"CANCEL\",\"date\":\"2026-07-18\"}","deletedAt":null},
				{"id":"makeup","payload":"{\"id\":\"makeup\",\"type\":\"MAKE_UP\",\"date\":\"2026-07-18\",\"title\":\"实验课\",\"location\":\"实验楼\",\"startMinute\":600,\"endMinute\":690}","deletedAt":null}
			],
			"mobile.settings": [
				{"id":"weekNumberMode","payload":"{\"value\":\"SEMESTER\"}","deletedAt":null},
				{"id":"semesterWeekStartDate","payload":"{\"value\":\"2026-07-13\"}","deletedAt":null}
			]
		}
	}`)
	schedule, recognized, err := parseBriefingDocument(raw, nil)
	if err != nil || !recognized {
		t.Fatalf("parse schedule: recognized=%v err=%v", recognized, err)
	}
	lessons := schedule.lessonsForDate(time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC))
	if len(lessons) != 1 || lessons[0].Title != "实验课" || lessons[0].StartMinute != 600 {
		t.Fatalf("unexpected lessons: %+v", lessons)
	}
	body := renderBriefingMail("alice", briefingDigest{TargetDate: "2026-07-18", Available: true, Source: "Classing 官方云同步", Lessons: lessons})
	for _, expected := range []string{"实验课", "10:00–11:30", "实验楼", "课表来源：Classing 官方云同步"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("mail body missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "打开 Classing") {
		t.Fatalf("mail body still delegates schedule content to client: %s", body)
	}
}

func TestBriefingDigestSupportsLegacyTimeAndParity(t *testing.T) {
	raw := []byte(`{"timetable":{"weekNumberMode":"SEMESTER","semesterWeekStartDate":"2026-07-13","baseLessons":[{"id":"odd","title":"单周课","dayOfWeek":1,"startTime":"08:30","endTime":"09:15","startWeek":1,"endWeek":16,"weekParity":"ODD"}]}}`)
	schedule, recognized, err := parseBriefingDocument(raw, nil)
	if err != nil || !recognized {
		t.Fatalf("parse legacy: recognized=%v err=%v", recognized, err)
	}
	weekOne := schedule.lessonsForDate(time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC))
	weekTwo := schedule.lessonsForDate(time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC))
	if len(weekOne) != 1 || weekOne[0].StartMinute != 510 || len(weekTwo) != 0 {
		t.Fatalf("unexpected parity projection: weekOne=%+v weekTwo=%+v", weekOne, weekTwo)
	}
}

func TestBriefingMailDistinguishesEmptyDayFromMissingTimetable(t *testing.T) {
	empty := renderBriefingMail("alice", briefingDigest{TargetDate: "2026-07-18", Available: true, Source: "Classing 官方云同步"})
	if !strings.Contains(empty, "当天没有课程安排") {
		t.Fatalf("empty schedule body = %s", empty)
	}
	missing := renderBriefingMail("alice", briefingDigest{TargetDate: "2026-07-18"})
	if !strings.Contains(missing, "尚未读取到已同步的课表数据") {
		t.Fatalf("missing schedule body = %s", missing)
	}
}
