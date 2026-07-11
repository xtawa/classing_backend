package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xtawa/classing-backend/internal/store"
)

func (s *Server) requireCloudMembership(w http.ResponseWriter, r *http.Request) bool {
	member, err := s.cloudMembership(r)
	if err != nil {
		writeStoreError(w, r, err, "OFFICIAL_CLOUD")
		return false
	}
	if !member {
		writeError(w, r, http.StatusForbidden, "OFFICIAL_CLOUD_MEMBERSHIP_REQUIRED", "active membership is required")
		return false
	}
	return true
}

func (s *Server) cloudMembership(r *http.Request) (bool, error) {
	item, err := s.store.Membership(r.Context(), principal(r).User.ID)
	if err != nil {
		return false, err
	}
	return item.ExpiresAt > time.Now().UnixMilli(), nil
}

func (s *Server) cloudPing(w http.ResponseWriter, r *http.Request) {
	member, err := s.cloudMembership(r)
	if err != nil {
		writeStoreError(w, r, err, "OFFICIAL_CLOUD")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "ok",
		"provider":         "OFFICIAL",
		"canSyncSettings":  true,
		"canSyncTimetable": member,
	})
}

func (s *Server) cloudConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"provider": "OFFICIAL", "maxDocumentBytes": s.cfg.MaxCloudDocumentSize, "etagRequired": true, "idempotencyKeyRecommended": true, "syncScopes": []string{"TIMETABLE", "MOBILE_SETTINGS", "WEAR_SETTINGS"}})
}

func (s *Server) getCloudDocument(w http.ResponseWriter, r *http.Request) {
	member, err := s.cloudMembership(r)
	if err != nil {
		writeStoreError(w, r, err, "OFFICIAL_CLOUD")
		return
	}
	item, err := s.store.CloudDocument(r.Context(), principal(r).User.ID)
	if err != nil {
		if err == store.ErrNotFound {
			w.Header().Set("ETag", `"0"`)
			writeCloudJSON(w, []byte(emptyCloudDocumentV2()))
			return
		}
		writeStoreError(w, r, err, "OFFICIAL_CLOUD")
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(item.Version, 10)+`"`)
	w.Header().Set("Last-Modified", time.UnixMilli(item.UpdatedAt).UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	payload := []byte(item.Payload)
	if !member {
		var err error
		payload, err = filterSettingsCloudDocument(payload)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "OFFICIAL_CLOUD_DOCUMENT_INVALID", "stored cloud document is invalid")
			return
		}
	}
	_, _ = w.Write(payload)
}

func (s *Server) putCloudDocument(w http.ResponseWriter, r *http.Request) {
	member, err := s.cloudMembership(r)
	if err != nil {
		writeStoreError(w, r, err, "OFFICIAL_CLOUD")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxCloudDocumentSize)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "OFFICIAL_CLOUD_DOCUMENT_TOO_LARGE", "cloud document exceeds the configured limit")
		return
	}
	if !json.Valid(payload) {
		writeError(w, r, http.StatusBadRequest, "OFFICIAL_CLOUD_DOCUMENT_INVALID", "cloud document must be valid JSON")
		return
	}
	userID := principal(r).User.ID
	if !member {
		current, err := s.store.CloudDocument(r.Context(), userID)
		currentPayload := []byte(emptyCloudDocumentV2())
		if err == nil {
			currentPayload = []byte(current.Payload)
		} else if err != store.ErrNotFound {
			writeStoreError(w, r, err, "OFFICIAL_CLOUD")
			return
		}
		payload, err = mergeSettingsCloudDocument(currentPayload, payload)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "OFFICIAL_CLOUD_DOCUMENT_INVALID", "cloud document must be valid JSON")
			return
		}
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	requestHash := store.HashRequest(payload)
	if idempotencyKey != "" {
		if record, err := s.store.Idempotency(r.Context(), userID, idempotencyKey); err == nil {
			if record.RequestHash != requestHash {
				writeError(w, r, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "idempotency key was already used for another payload")
				return
			}
			w.WriteHeader(record.ResponseCode)
			_, _ = w.Write([]byte(record.ResponseBody))
			return
		}
	}
	expected := parseVersion(r.Header.Get("If-Match"))
	item, err := s.store.PutCloudDocument(r.Context(), userID, payload, expected)
	if err != nil {
		if err == store.ErrConflict {
			writeError(w, r, http.StatusPreconditionFailed, "OFFICIAL_CLOUD_VERSION_CONFLICT", "cloud document has changed; pull and merge before retrying")
			return
		}
		writeStoreError(w, r, err, "OFFICIAL_CLOUD")
		return
	}
	body := []byte(`{"success":true,"version":` + strconv.FormatInt(item.Version, 10) + `}`)
	w.Header().Set("ETag", `"`+strconv.FormatInt(item.Version, 10)+`"`)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if idempotencyKey != "" {
		_ = s.store.SaveIdempotency(r.Context(), store.IdempotencyRecord{KeyValue: idempotencyKey, UserID: userID, RequestHash: requestHash, ResponseCode: http.StatusOK, ResponseBody: string(body), ExpiresAt: time.Now().Add(24 * time.Hour).UnixMilli()})
	}
	s.audit(r, userID, "OFFICIAL_CLOUD_WRITE", "CLOUD_DOCUMENT", userID, map[string]any{"version": item.Version, "bytes": len(payload)})
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) cloudEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "OFFICIAL_CLOUD_EVENTS_UNAVAILABLE", "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	lastVersion := parseVersion(r.Header.Get("Last-Event-ID"))
	ticker := time.NewTicker(time.Second)
	keepAlive := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	defer keepAlive.Stop()
	userID := principal(r).User.ID
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			_, _ = io.WriteString(w, ": keep-alive\n\n")
			flusher.Flush()
		case <-ticker.C:
			item, err := s.store.CloudDocument(r.Context(), userID)
			if err == store.ErrNotFound {
				continue
			}
			if err != nil {
				return
			}
			if item.Version <= lastVersion {
				continue
			}
			lastVersion = item.Version
			_, _ = io.WriteString(w, "id: "+strconv.FormatInt(item.Version, 10)+"\nevent: settings\ndata: {\"version\":"+strconv.FormatInt(item.Version, 10)+",\"updatedAt\":"+strconv.FormatInt(item.UpdatedAt, 10)+"}\n\n")
			flusher.Flush()
		}
	}
}

func writeCloudJSON(w http.ResponseWriter, payload []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(payload)
}

func emptyCloudDocumentV2() string {
	return `{"format":"classing_cloud_sync_v2","updatedAt":0,"records":{},"changes":[],"devices":[]}`
}

var settingsCloudDomains = map[string]bool{
	"mobile.settings": true,
	"wear.settings":   true,
	"cloud.config":    true,
	"app.commands":    true,
}

func filterSettingsCloudDocument(payload []byte) ([]byte, error) {
	doc, err := decodeCloudDocument(payload)
	if err != nil {
		return nil, err
	}
	records := map[string]any{}
	for domain, value := range cloudRecords(doc) {
		if settingsCloudDomains[domain] {
			records[domain] = value
		}
	}
	doc["records"] = records
	doc["changes"] = filterCloudChanges(doc["changes"])
	return json.Marshal(doc)
}

func mergeSettingsCloudDocument(currentPayload, incomingPayload []byte) ([]byte, error) {
	current, err := decodeCloudDocument(currentPayload)
	if err != nil {
		return nil, err
	}
	incoming, err := decodeCloudDocument(incomingPayload)
	if err != nil {
		return nil, err
	}
	current["format"] = "classing_cloud_sync_v2"
	current["updatedAt"] = incoming["updatedAt"]
	records := cloudRecords(current)
	for domain, value := range cloudRecords(incoming) {
		if settingsCloudDomains[domain] {
			records[domain] = value
		}
	}
	current["records"] = records
	current["changes"] = append(filterCloudChanges(current["changes"]), filterCloudChanges(incoming["changes"])...)
	if devices, ok := incoming["devices"].([]any); ok {
		current["devices"] = devices
	}
	return json.Marshal(current)
}

func decodeCloudDocument(payload []byte) (map[string]any, error) {
	if len(payload) == 0 {
		payload = []byte(emptyCloudDocumentV2())
	}
	doc := map[string]any{}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, err
	}
	if doc["format"] == nil {
		doc["format"] = "classing_cloud_sync_v2"
	}
	if doc["records"] == nil {
		doc["records"] = map[string]any{}
	}
	if doc["changes"] == nil {
		doc["changes"] = []any{}
	}
	if doc["devices"] == nil {
		doc["devices"] = []any{}
	}
	return doc, nil
}

func cloudRecords(doc map[string]any) map[string]any {
	records, ok := doc["records"].(map[string]any)
	if !ok {
		records = map[string]any{}
		doc["records"] = records
	}
	return records
}

func filterCloudChanges(value any) []any {
	changes, ok := value.([]any)
	if !ok {
		return []any{}
	}
	result := []any{}
	for _, raw := range changes {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		domain, _ := item["domain"].(string)
		if settingsCloudDomains[domain] {
			result = append(result, item)
		}
	}
	return result
}

func (s *Server) enqueueAppBriefingTestCommand(r *http.Request, userID string) error {
	now := time.Now().UnixMilli()
	commandID := "daily-briefing-test-" + requestID(r)
	for attempt := 0; attempt < 4; attempt++ {
		current, err := s.store.CloudDocument(r.Context(), userID)
		expected := int64(0)
		payload := []byte(emptyCloudDocumentV2())
		if err == nil {
			expected = current.Version
			payload = []byte(current.Payload)
		} else if err != store.ErrNotFound {
			return err
		}
		next, err := appendAppCommand(payload, commandID, "DAILY_BRIEFING_TEST", now)
		if err != nil {
			return err
		}
		if _, err := s.store.PutCloudDocument(r.Context(), userID, next, expected); err != nil {
			if err == store.ErrConflict {
				continue
			}
			return err
		}
		return nil
	}
	return store.ErrConflict
}

func appendAppCommand(payload []byte, commandID, commandType string, now int64) ([]byte, error) {
	doc, err := decodeCloudDocument(payload)
	if err != nil {
		return nil, err
	}
	records := cloudRecords(doc)
	commands, _ := records["app.commands"].([]any)
	commandPayload, err := json.Marshal(map[string]any{
		"type":      commandType,
		"createdAt": now,
	})
	if err != nil {
		return nil, err
	}
	version := map[string]any{
		"counter":   now,
		"deviceId":  "server-briefing",
		"changedAt": now,
	}
	commands = append(commands, map[string]any{
		"id":               commandID,
		"payload":          string(commandPayload),
		"version":          version,
		"deletedAt":        nil,
		"recoverableUntil": now + int64((10*time.Minute)/time.Millisecond),
	})
	records["app.commands"] = commands
	doc["records"] = records
	changes, _ := doc["changes"].([]any)
	doc["changes"] = append([]any{map[string]any{
		"id":         "chg-" + commandID,
		"domain":     "app.commands",
		"recordId":   commandID,
		"action":     "created",
		"version":    version,
		"occurredAt": now,
		"detail":     "daily briefing app test",
	}}, changes...)
	doc["updatedAt"] = now
	return json.Marshal(doc)
}
