package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/model"
)

const maxTimetableDocumentBytes = 2 * 1024 * 1024

func validateTimetableFields(name, timezone, semesterStart string, weekCount int, document json.RawMessage, documentRequired bool) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return ErrInvalid
	}
	if timezone == "" {
		timezone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return ErrInvalid
	}
	if semesterStart != "" {
		if _, err := time.Parse("2006-01-02", semesterStart); err != nil {
			return ErrInvalid
		}
	}
	if weekCount < 1 || weekCount > 60 {
		return ErrInvalid
	}
	if len(document) == 0 && !documentRequired {
		return nil
	}
	if len(document) == 0 || len(document) > maxTimetableDocumentBytes || !json.Valid(document) {
		return ErrInvalid
	}
	var root map[string]any
	if err := json.Unmarshal(document, &root); err != nil {
		return ErrInvalid
	}
	lessons, ok := root["lessons"].([]any)
	if !ok || len(lessons) > 2000 {
		return ErrInvalid
	}
	if exceptions, exists := root["exceptions"]; exists {
		items, ok := exceptions.([]any)
		if !ok || len(items) > 2000 {
			return ErrInvalid
		}
	}
	return nil
}

func (s *Store) CreateTimetable(ctx context.Context, ownerID, name, timezone, semesterStart string, weekCount int, document json.RawMessage) (model.TimetableProject, error) {
	name = strings.TrimSpace(name)
	if timezone == "" {
		timezone = "Asia/Shanghai"
	}
	if weekCount == 0 {
		weekCount = 20
	}
	if len(document) == 0 {
		document = json.RawMessage(`{"lessons":[],"exceptions":[]}`)
	}
	if err := validateTimetableFields(name, timezone, semesterStart, weekCount, document, true); err != nil {
		return model.TimetableProject{}, err
	}
	now := nowMillis()
	item := model.TimetableProject{ID: ids.New("ttb"), OwnerID: ownerID, Name: name, Timezone: timezone, SemesterStart: semesterStart, WeekCount: weekCount, Document: string(document), Version: 1, CreatedAt: now, UpdatedAt: now}
	_, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO timetable_projects (id, owner_id, name, timezone, semester_start, week_count, document, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`), item.ID, item.OwnerID, item.Name, item.Timezone, item.SemesterStart, item.WeekCount, item.Document, now, now)
	return item, normalizeDBError(err)
}

func (s *Store) ListTimetables(ctx context.Context, userID string, admin bool, limit, offset int) ([]model.TimetableProject, int, error) {
	return s.ListTimetablesFiltered(ctx, userID, admin, limit, offset, "", "")
}

func (s *Store) ListTimetablesFiltered(ctx context.Context, userID string, admin bool, limit, offset int, query, ownerID string) ([]model.TimetableProject, int, error) {
	where := ""
	args := []any{}
	if !admin {
		where = ` WHERE owner_id = ?`
		args = append(args, userID)
	} else if strings.TrimSpace(ownerID) != "" {
		where = ` WHERE owner_id = ?`
		args = append(args, strings.TrimSpace(ownerID))
	}
	if strings.TrimSpace(query) != "" {
		if where == "" {
			where = ` WHERE `
		} else {
			where += ` AND `
		}
		where += `LOWER(name) LIKE ?`
		args = append(args, "%"+strings.ToLower(strings.TrimSpace(query))+"%")
	}
	var total int
	if err := s.db.GetContext(ctx, &total, s.rebind(`SELECT COUNT(*) FROM timetable_projects`+where), args...); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	items := []model.TimetableProject{}
	if err := s.db.SelectContext(ctx, &items, s.rebind(`SELECT * FROM timetable_projects`+where+` ORDER BY updated_at DESC LIMIT ? OFFSET ?`), args...); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Store) Timetable(ctx context.Context, id, userID string, admin bool) (model.TimetableProject, error) {
	var item model.TimetableProject
	err := s.db.GetContext(ctx, &item, s.rebind(`SELECT * FROM timetable_projects WHERE id = ?`), id)
	if err != nil {
		return item, normalizeDBError(err)
	}
	if !admin && item.OwnerID != userID {
		return model.TimetableProject{}, ErrForbidden
	}
	return item, nil
}

func (s *Store) UpdateTimetable(ctx context.Context, id, userID string, admin bool, name, timezone, semesterStart string, weekCount int, document json.RawMessage, expectedVersion int64) (model.TimetableProject, error) {
	current, err := s.Timetable(ctx, id, userID, admin)
	if err != nil {
		return model.TimetableProject{}, err
	}
	if expectedVersion > 0 && current.Version != expectedVersion {
		return model.TimetableProject{}, ErrConflict
	}
	if name == "" {
		name = current.Name
	}
	if timezone == "" {
		timezone = current.Timezone
	}
	if semesterStart == "" {
		semesterStart = current.SemesterStart
	}
	if weekCount < 1 {
		weekCount = current.WeekCount
	}
	doc := current.Document
	if len(document) > 0 {
		doc = string(document)
	}
	if err := validateTimetableFields(strings.TrimSpace(name), timezone, semesterStart, weekCount, json.RawMessage(doc), true); err != nil {
		return model.TimetableProject{}, err
	}
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE timetable_projects SET name = ?, timezone = ?, semester_start = ?, week_count = ?, document = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ?`), strings.TrimSpace(name), timezone, semesterStart, weekCount, doc, nowMillis(), id, current.Version)
	if err != nil {
		return model.TimetableProject{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.TimetableProject{}, ErrConflict
	}
	return s.Timetable(ctx, id, userID, admin)
}

func (s *Store) DeleteTimetable(ctx context.Context, id, userID string, admin bool) error {
	current, err := s.Timetable(ctx, id, userID, admin)
	if err != nil {
		return err
	}
	if !admin && current.OwnerID != userID {
		return ErrForbidden
	}
	_, err = s.db.ExecContext(ctx, s.rebind(`DELETE FROM timetable_projects WHERE id = ?`), id)
	return err
}
