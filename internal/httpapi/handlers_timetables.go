package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/xtawa/classing-backend/internal/model"
)

type timetableRequest struct {
	Name            string          `json:"name"`
	Timezone        string          `json:"timezone"`
	SemesterStart   string          `json:"semesterStart"`
	WeekCount       int             `json:"weekCount"`
	Document        json.RawMessage `json:"document"`
	ExpectedVersion int64           `json:"expectedVersion"`
}

func (s *Server) listTimetables(w http.ResponseWriter, r *http.Request) {
	user := principal(r).User
	limit, offset := pageParams(r)
	ownerID := ""
	if user.Role == "ADMIN" {
		ownerID = r.URL.Query().Get("ownerId")
	}
	items, total, err := s.store.ListTimetablesFiltered(r.Context(), user.ID, user.Role == "ADMIN", limit, offset, r.URL.Query().Get("q"), ownerID)
	if err != nil {
		writeStoreError(w, r, err, "TIMETABLE")
		return
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, projectPayload(item, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": result, "total": total})
}

func (s *Server) createTimetable(w http.ResponseWriter, r *http.Request) {
	var body timetableRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	item, err := s.store.CreateTimetable(r.Context(), principal(r).User.ID, body.Name, body.Timezone, body.SemesterStart, body.WeekCount, body.Document)
	if err != nil {
		writeStoreError(w, r, err, "TIMETABLE")
		return
	}
	s.audit(r, principal(r).User.ID, "TIMETABLE_CREATE", "TIMETABLE", item.ID, nil)
	writeJSON(w, http.StatusCreated, map[string]any{"project": projectPayload(item, true)})
}

func (s *Server) getTimetable(w http.ResponseWriter, r *http.Request) {
	user := principal(r).User
	item, err := s.store.Timetable(r.Context(), r.PathValue("id"), user.ID, user.Role == "ADMIN")
	if err != nil {
		writeStoreError(w, r, err, "TIMETABLE")
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(item.Version, 10)+`"`)
	writeJSON(w, http.StatusOK, map[string]any{"project": projectPayload(item, true)})
}

func (s *Server) updateTimetable(w http.ResponseWriter, r *http.Request) {
	var body timetableRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.ExpectedVersion == 0 {
		body.ExpectedVersion = parseVersion(r.Header.Get("If-Match"))
	}
	user := principal(r).User
	item, err := s.store.UpdateTimetable(r.Context(), r.PathValue("id"), user.ID, user.Role == "ADMIN", body.Name, body.Timezone, body.SemesterStart, body.WeekCount, body.Document, body.ExpectedVersion)
	if err != nil {
		writeStoreError(w, r, err, "TIMETABLE")
		return
	}
	s.audit(r, user.ID, "TIMETABLE_UPDATE", "TIMETABLE", item.ID, map[string]any{"version": item.Version})
	w.Header().Set("ETag", `"`+strconv.FormatInt(item.Version, 10)+`"`)
	writeJSON(w, http.StatusOK, map[string]any{"project": projectPayload(item, true)})
}

func (s *Server) deleteTimetable(w http.ResponseWriter, r *http.Request) {
	user := principal(r).User
	id := r.PathValue("id")
	if err := s.store.DeleteTimetable(r.Context(), id, user.ID, user.Role == "ADMIN"); err != nil {
		writeStoreError(w, r, err, "TIMETABLE")
		return
	}
	s.audit(r, user.ID, "TIMETABLE_DELETE", "TIMETABLE", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func projectPayload(item model.TimetableProject, includeDocument bool) map[string]any {
	result := map[string]any{"projectId": item.ID, "ownerId": item.OwnerID, "name": item.Name, "timezone": item.Timezone, "semesterStart": item.SemesterStart, "weekCount": item.WeekCount, "version": item.Version, "createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt}
	if includeDocument {
		var document any
		if err := json.Unmarshal([]byte(item.Document), &document); err != nil {
			document = map[string]any{}
		}
		result["document"] = document
	}
	return result
}
