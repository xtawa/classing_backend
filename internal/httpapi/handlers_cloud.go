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
	item, err := s.store.Membership(r.Context(), principal(r).User.ID)
	if err != nil {
		writeStoreError(w, r, err, "OFFICIAL_CLOUD")
		return false
	}
	if item.ExpiresAt <= time.Now().UnixMilli() {
		writeError(w, r, http.StatusForbidden, "OFFICIAL_CLOUD_MEMBERSHIP_REQUIRED", "active membership is required")
		return false
	}
	return true
}

func (s *Server) cloudPing(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloudMembership(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "provider": "OFFICIAL"})
}

func (s *Server) cloudConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"provider": "OFFICIAL", "maxDocumentBytes": s.cfg.MaxCloudDocumentSize, "etagRequired": true, "idempotencyKeyRecommended": true, "syncScopes": []string{"TIMETABLE", "MOBILE_SETTINGS", "WEAR_SETTINGS"}})
}

func (s *Server) getCloudDocument(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloudMembership(w, r) {
		return
	}
	item, err := s.store.CloudDocument(r.Context(), principal(r).User.ID)
	if err != nil {
		if err == store.ErrNotFound {
			w.Header().Set("ETag", `"0"`)
			writeJSON(w, http.StatusOK, map[string]any{"schema": "classing_cloud_sync_v1", "domains": map[string]any{}})
			return
		}
		writeStoreError(w, r, err, "OFFICIAL_CLOUD")
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(item.Version, 10)+`"`)
	w.Header().Set("Last-Modified", time.UnixMilli(item.UpdatedAt).UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(item.Payload))
}

func (s *Server) putCloudDocument(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloudMembership(w, r) {
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
	if !s.requireCloudMembership(w, r) {
		return
	}
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
