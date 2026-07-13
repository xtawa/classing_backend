package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
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
			if etagMatches(r.Header.Get("If-None-Match"), 0) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			writeCloudJSON(w, []byte(emptyCloudDocumentV2()))
			return
		}
		writeStoreError(w, r, err, "OFFICIAL_CLOUD")
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(item.Version, 10)+`"`)
	if etagMatches(r.Header.Get("If-None-Match"), item.Version) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
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
	if err := validateCloudDocument(payload); err != nil {
		writeError(w, r, http.StatusBadRequest, "OFFICIAL_CLOUD_DOCUMENT_INVALID", err.Error())
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
	if len(idempotencyKey) > maxHeaderIDLen {
		writeError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_TOO_LONG", "idempotency key must be at most 128 characters")
		return
	}
	expected, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		if errors.Is(err, errPreconditionRequired) {
			writeError(w, r, http.StatusPreconditionRequired, "OFFICIAL_CLOUD_PRECONDITION_REQUIRED", "If-Match is required")
		} else {
			writeError(w, r, http.StatusBadRequest, "OFFICIAL_CLOUD_PRECONDITION_INVALID", "If-Match must be a quoted non-negative document version")
		}
		return
	}
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
	item, replay, err := s.store.PutCloudDocumentIdempotent(r.Context(), userID, payload, expected, idempotencyKey, requestHash, s.auditContext(r, userID, "OFFICIAL_CLOUD_WRITE", "CLOUD_DOCUMENT", userID, nil))
	if err != nil {
		if errors.Is(err, store.ErrIdempotencyKeyReused) {
			writeError(w, r, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "idempotency key was already used for another payload")
			return
		}
		if err == store.ErrConflict {
			writeError(w, r, http.StatusPreconditionFailed, "OFFICIAL_CLOUD_VERSION_CONFLICT", "cloud document has changed; pull and merge before retrying")
			return
		}
		writeStoreError(w, r, err, "OFFICIAL_CLOUD")
		return
	}
	if replay != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(replay.ResponseCode)
		_, _ = w.Write([]byte(replay.ResponseBody))
		return
	}
	body := []byte(`{"success":true,"version":` + strconv.FormatInt(item.Version, 10) + `}`)
	w.Header().Set("ETag", `"`+strconv.FormatInt(item.Version, 10)+`"`)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

var errPreconditionRequired = errors.New("precondition required")

func parseIfMatch(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errPreconditionRequired
	}
	if strings.HasPrefix(value, "W/") || len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, store.ErrInvalid
	}
	version, err := strconv.ParseInt(value[1:len(value)-1], 10, 64)
	if err != nil || version < 0 {
		return 0, store.ErrInvalid
	}
	return version, nil
}

func etagMatches(value string, version int64) bool {
	target := `"` + strconv.FormatInt(version, 10) + `"`
	for _, candidate := range strings.Split(value, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == target || candidate == "W/"+target {
			return true
		}
	}
	return false
}

func validateCloudDocument(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return errors.New("cloud document must be valid JSON")
	}
	root, ok := value.(map[string]any)
	if !ok {
		return errors.New("cloud document root must be an object")
	}
	allowed := map[string]bool{"format": true, "updatedAt": true, "records": true, "changes": true, "devices": true}
	for key := range root {
		if !allowed[key] {
			return errors.New("cloud document contains an unsupported top-level field")
		}
	}
	if format, ok := root["format"].(string); !ok || format != "classing_cloud_sync_v2" {
		return errors.New("cloud document format must be classing_cloud_sync_v2")
	}
	updatedAt, ok := root["updatedAt"].(json.Number)
	if !ok {
		return errors.New("cloud document updatedAt must be a non-negative integer")
	}
	if parsed, err := updatedAt.Int64(); err != nil || parsed < 0 {
		return errors.New("cloud document updatedAt must be a non-negative integer")
	}
	records, ok := root["records"].(map[string]any)
	if !ok || len(records) > 256 {
		return errors.New("cloud document records must be an object with at most 256 domains")
	}
	changes, ok := root["changes"].([]any)
	if !ok || len(changes) > 2048 {
		return errors.New("cloud document changes must be an array with at most 2048 entries")
	}
	devices, ok := root["devices"].([]any)
	if !ok || len(devices) > 64 {
		return errors.New("cloud document devices must be an array with at most 64 entries")
	}
	if cloudJSONDepth(root, 1) > 32 {
		return errors.New("cloud document nesting exceeds 32 levels")
	}
	return nil
}

func cloudJSONDepth(value any, depth int) int {
	maxDepth := depth
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			if childDepth := cloudJSONDepth(child, depth+1); childDepth > maxDepth {
				maxDepth = childDepth
			}
		}
	case []any:
		for _, child := range typed {
			if childDepth := cloudJSONDepth(child, depth+1); childDepth > maxDepth {
				maxDepth = childDepth
			}
		}
	}
	return maxDepth
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
	lastEventID := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
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
			events, err := s.store.RuntimeEvents(r.Context(), userID, lastEventID, 100)
			if err != nil {
				return
			}
			for _, event := range events {
				lastEventID = event.ID
				_, _ = io.WriteString(w, "id: "+event.ID+"\nevent: "+event.EventType+"\ndata: "+event.Payload+"\n\n")
			}
			if len(events) > 0 {
				flusher.Flush()
			}
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
	current["changes"] = mergeCloudChanges(current["changes"], filterCloudChanges(incoming["changes"]))
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
	allowed := []any{}
	for _, raw := range changes {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		domain, _ := item["domain"].(string)
		if settingsCloudDomains[domain] {
			allowed = append(allowed, item)
		}
	}
	return mergeCloudChanges(allowed)
}

func mergeCloudChanges(values ...any) []any {
	result := []any{}
	positions := map[string]int{}
	for _, value := range values {
		changes, ok := value.([]any)
		if !ok {
			continue
		}
		for _, raw := range changes {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			key := cloudChangeKey(item)
			if position, exists := positions[key]; exists {
				existing, _ := result[position].(map[string]any)
				if cloudChangeIsNewer(item, existing) {
					result[position] = item
				}
				continue
			}
			positions[key] = len(result)
			result = append(result, item)
		}
	}
	return result
}

func cloudChangeKey(item map[string]any) string {
	if id, _ := item["id"].(string); strings.TrimSpace(id) != "" {
		domain, _ := item["domain"].(string)
		return "id:" + domain + ":" + strings.TrimSpace(id)
	}
	encoded, _ := json.Marshal(item)
	return "anonymous:" + string(encoded)
}

func cloudChangeIsNewer(candidate, existing map[string]any) bool {
	candidateCounter, candidateDeviceID, candidateChangedAt, candidateOccurredAt := cloudChangeOrder(candidate)
	existingCounter, existingDeviceID, existingChangedAt, existingOccurredAt := cloudChangeOrder(existing)
	if candidateCounter != existingCounter {
		return candidateCounter > existingCounter
	}
	if candidateDeviceID != existingDeviceID {
		return candidateDeviceID > existingDeviceID
	}
	if candidateChangedAt != existingChangedAt {
		return candidateChangedAt > existingChangedAt
	}
	return candidateOccurredAt > existingOccurredAt
}

func cloudChangeOrder(item map[string]any) (counter int64, deviceID string, changedAt, occurredAt int64) {
	if version, ok := item["version"].(map[string]any); ok {
		counter = cloudJSONInt64(version["counter"])
		deviceID, _ = version["deviceId"].(string)
		changedAt = cloudJSONInt64(version["changedAt"])
	}
	return counter, deviceID, changedAt, cloudJSONInt64(item["occurredAt"])
}

func cloudJSONInt64(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	default:
		return 0
	}
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
