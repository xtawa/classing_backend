package httpapi

import (
	"net/http"
	"strings"
	"time"
)

func (s *Server) testBriefingV2(w http.ResponseWriter, r *http.Request) {
	if enabled, err := s.store.Setting(r.Context(), "briefing.enabled", "true"); err != nil || enabled == "false" {
		writeError(w, r, http.StatusServiceUnavailable, "BRIEFING_DISABLED", "briefing service is disabled")
		return
	}
	var body struct {
		Channel string `json:"channel"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	channel := strings.ToUpper(strings.TrimSpace(body.Channel))
	if channel == "" {
		channel = "EMAIL"
	}
	if channel != "EMAIL" && channel != "APP_NOTIFICATION" && channel != "BOTH" {
		writeError(w, r, http.StatusBadRequest, "BRIEFING_CHANNEL_INVALID", "briefing test channel is invalid")
		return
	}
	user := principal(r).User
	var job any
	if channel == "EMAIL" || channel == "BOTH" {
		queued, err := s.store.QueueBriefingJob(r.Context(), user.ID, time.Now().Format("2006-01-02")+"-test-"+requestID(r), "EMAIL_TEST", time.Now().UnixMilli())
		if err != nil {
			writeStoreError(w, r, err, "BRIEFING_TEST")
			return
		}
		job = queued
	}
	appQueued := false
	if channel == "APP_NOTIFICATION" || channel == "BOTH" {
		if err := s.enqueueAppBriefingTestCommand(r, user.ID); err != nil {
			writeStoreError(w, r, err, "BRIEFING_TEST")
			return
		}
		appQueued = true
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"job":                   job,
		"appNotificationQueued": appQueued,
		"preview": map[string]any{
			"subject":   "Classing 每日课程简报",
			"recipient": user.Email,
			"status":    "QUEUED",
		},
	})
}
