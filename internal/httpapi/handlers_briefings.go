package httpapi

import (
	"net/http"
	"time"

	"github.com/xtawa/classing-backend/internal/model"
	"github.com/xtawa/classing-backend/internal/store"
)

func (s *Server) getBriefing(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.Briefing(r.Context(), principal(r).User.ID)
	if err == store.ErrNotFound {
		writeJSON(w, http.StatusOK, map[string]any{"briefing": map[string]any{"enabled": false, "channel": "APP_NOTIFICATION", "time": "20:00", "timezone": "Asia/Shanghai"}})
		return
	}
	if err != nil {
		writeStoreError(w, r, err, "BRIEFING")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"briefing": briefingPayload(item)})
}

func (s *Server) putBriefing(w http.ResponseWriter, r *http.Request) {
	if enabled, err := s.store.Setting(r.Context(), "briefing.enabled", "true"); err != nil || enabled == "false" {
		writeError(w, r, http.StatusServiceUnavailable, "BRIEFING_DISABLED", "briefing service is disabled")
		return
	}
	var body struct {
		Enabled  bool   `json:"enabled"`
		Channel  string `json:"channel"`
		Time     string `json:"time"`
		Timezone string `json:"timezone"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	item, err := s.store.UpsertBriefing(r.Context(), principal(r).User.ID, body.Enabled, body.Channel, body.Time, body.Timezone)
	if err != nil {
		writeStoreError(w, r, err, "BRIEFING")
		return
	}
	s.audit(r, principal(r).User.ID, "BRIEFING_UPDATE", "BRIEFING", principal(r).User.ID, map[string]any{"enabled": body.Enabled, "channel": body.Channel})
	writeJSON(w, http.StatusOK, map[string]any{"briefing": briefingPayload(item)})
}

func (s *Server) deleteBriefing(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteBriefing(r.Context(), principal(r).User.ID); err != nil {
		writeStoreError(w, r, err, "BRIEFING")
		return
	}
	s.audit(r, principal(r).User.ID, "BRIEFING_DELETE", "BRIEFING", principal(r).User.ID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testBriefing(w http.ResponseWriter, r *http.Request) {
	if enabled, err := s.store.Setting(r.Context(), "briefing.enabled", "true"); err != nil || enabled == "false" {
		writeError(w, r, http.StatusServiceUnavailable, "BRIEFING_DISABLED", "briefing service is disabled")
		return
	}
	user := principal(r).User
	job, err := s.store.QueueBriefingJob(r.Context(), user.ID, time.Now().Format("2006-01-02")+"-test-"+requestID(r), "EMAIL_TEST", time.Now().UnixMilli())
	if err != nil {
		writeStoreError(w, r, err, "BRIEFING_TEST")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job, "preview": map[string]any{"subject": "Classing 每日课程简报", "recipient": user.Email, "status": "QUEUED"}})
}

func briefingPayload(item model.BriefingSubscription) map[string]any {
	return map[string]any{"enabled": item.Enabled != 0, "channel": item.Channel, "time": item.Time, "timezone": item.Timezone, "lastScheduledAt": item.LastScheduledAt, "updatedAt": item.UpdatedAt}
}
