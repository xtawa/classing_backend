package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/xtawa/classing-backend/internal/model"
)

func (s *Server) adminDashboard(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Dashboard(r.Context())
	if err != nil {
		writeStoreError(w, r, err, "ADMIN_DASHBOARD")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": stats})
}

func (s *Server) adminListUsers(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	items, total, err := s.store.ListUsers(r.Context(), limit, offset, r.URL.Query().Get("q"))
	if err != nil {
		writeStoreError(w, r, err, "ADMIN_USERS")
		return
	}
	users := make([]map[string]any, 0, len(items))
	for _, item := range items {
		payload := accountPayload(item)
		if membership, membershipErr := s.store.Membership(r.Context(), item.ID); membershipErr == nil {
			payload["membership"] = membershipPayload(membership)
		}
		users = append(users, payload)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "total": total})
}

func (s *Server) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role   string `json:"role"`
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	targetID := r.PathValue("id")
	if targetID == principal(r).User.ID && (strings.ToUpper(body.Role) != model.RoleAdmin || strings.ToUpper(body.Status) != model.StatusActive) {
		writeError(w, r, http.StatusBadRequest, "ADMIN_SELF_LOCKOUT", "administrator cannot remove their own access")
		return
	}
	user, err := s.store.AdminUpdateUser(r.Context(), targetID, strings.ToUpper(body.Role), strings.ToUpper(body.Status))
	if err != nil {
		writeStoreError(w, r, err, "ADMIN_USER")
		return
	}
	s.refreshReplays.invalidateUser(user.ID)
	s.audit(r, principal(r).User.ID, "ADMIN_USER_UPDATE", "USER", targetID, map[string]any{"role": user.Role, "status": user.Status})
	writeJSON(w, http.StatusOK, map[string]any{"user": accountPayload(user)})
}

func (s *Server) adminGenerateRedeemCodes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CodeType       string `json:"codeType"`
		Count          int    `json:"count"`
		GrantDays      int    `json:"grantDays"`
		MaxRedemptions int    `json:"maxRedemptions"`
		ExpiresAt      int64  `json:"expiresAt"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	items, err := s.store.CreateRedeemCodes(r.Context(), principal(r).User.ID, body.CodeType, body.Count, body.GrantDays, body.MaxRedemptions, body.ExpiresAt)
	if err != nil {
		writeStoreError(w, r, err, "REDEEM_CODE")
		return
	}
	s.audit(r, principal(r).User.ID, "REDEEM_CODE_GENERATE", "REDEEM_BATCH", "", map[string]any{"count": len(items), "grantDays": body.GrantDays})
	writeJSON(w, http.StatusCreated, map[string]any{"codes": items})
}

func (s *Server) adminListRedeemCodes(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	items, total, err := s.store.ListRedeemCodes(r.Context(), limit, offset)
	if err != nil {
		writeStoreError(w, r, err, "REDEEM_CODE")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"codes": items, "total": total})
}

func (s *Server) adminRevokeRedeemCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.store.RevokeRedeemCode(r.Context(), body.Code); err != nil {
		writeStoreError(w, r, err, "REDEEM_CODE")
		return
	}
	s.audit(r, principal(r).User.ID, "REDEEM_CODE_REVOKE", "REDEEM_CODE", prefix(body.Code, 7), nil)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Server) adminGrantMembership(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID    string `json:"userId"`
		Tier      string `json:"tier"`
		ExpiresAt int64  `json:"expiresAt"`
		GrantDays int    `json:"grantDays"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.ExpiresAt == 0 && body.GrantDays > 0 {
		body.ExpiresAt = time.Now().Add(time.Duration(body.GrantDays) * 24 * time.Hour).UnixMilli()
	}
	item, err := s.store.SetMembership(r.Context(), principal(r).User.ID, body.UserID, body.Tier, body.ExpiresAt, "GRANT")
	if err != nil {
		writeStoreError(w, r, err, "MEMBERSHIP")
		return
	}
	s.audit(r, principal(r).User.ID, "MEMBERSHIP_GRANT", "MEMBERSHIP", body.UserID, map[string]any{"expiresAt": body.ExpiresAt, "tier": body.Tier})
	writeJSON(w, http.StatusOK, map[string]any{"membership": membershipPayload(item)})
}

func (s *Server) adminRevokeMembership(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID string `json:"userId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	item, err := s.store.SetMembership(r.Context(), principal(r).User.ID, body.UserID, "FREE", 0, "REVOKE")
	if err != nil {
		writeStoreError(w, r, err, "MEMBERSHIP")
		return
	}
	s.audit(r, principal(r).User.ID, "MEMBERSHIP_REVOKE", "MEMBERSHIP", body.UserID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"membership": membershipPayload(item)})
}

func (s *Server) adminListMailboxes(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListMailboxes(r.Context())
	if err != nil {
		writeStoreError(w, r, err, "MAILBOX")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mailboxes": items})
}

func (s *Server) adminCreateMailbox(w http.ResponseWriter, r *http.Request) {
	var body model.Mailbox
	if !decodeJSON(w, r, &body) {
		return
	}
	item, err := s.store.CreateMailbox(r.Context(), body)
	if err != nil {
		writeStoreError(w, r, err, "MAILBOX")
		return
	}
	s.audit(r, principal(r).User.ID, "MAILBOX_CREATE", "MAILBOX", item.ID, map[string]any{"host": item.SMTPHost, "secretRef": item.PasswordSecretRef})
	writeJSON(w, http.StatusCreated, map[string]any{"mailbox": item})
}

func (s *Server) adminUpdateMailbox(w http.ResponseWriter, r *http.Request) {
	var body model.Mailbox
	if !decodeJSON(w, r, &body) {
		return
	}
	body.ID = r.PathValue("id")
	item, err := s.store.UpdateMailbox(r.Context(), body)
	if err != nil {
		writeStoreError(w, r, err, "MAILBOX")
		return
	}
	s.audit(r, principal(r).User.ID, "MAILBOX_UPDATE", "MAILBOX", item.ID, map[string]any{"host": item.SMTPHost, "secretRef": item.PasswordSecretRef})
	writeJSON(w, http.StatusOK, map[string]any{"mailbox": item})
}

func (s *Server) adminDeleteMailbox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteMailbox(r.Context(), id); err != nil {
		writeStoreError(w, r, err, "MAILBOX")
		return
	}
	s.audit(r, principal(r).User.ID, "MAILBOX_DELETE", "MAILBOX", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminListJobs(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	items, total, err := s.store.ListBriefingJobs(r.Context(), limit, offset)
	if err != nil {
		writeStoreError(w, r, err, "BRIEFING_JOB")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": items, "total": total})
}

func (s *Server) adminRetryJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.RetryBriefingJob(r.Context(), id); err != nil {
		writeStoreError(w, r, err, "BRIEFING_JOB")
		return
	}
	s.audit(r, principal(r).User.ID, "BRIEFING_JOB_RETRY", "BRIEFING_JOB", id, nil)
	writeJSON(w, http.StatusAccepted, map[string]any{"success": true})
}

func (s *Server) adminListAudit(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	items, total, err := s.store.ListAudit(r.Context(), limit, offset)
	if err != nil {
		writeStoreError(w, r, err, "AUDIT")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"auditLogs": items, "total": total})
}

func (s *Server) adminListSettings(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListSettings(r.Context())
	if err != nil {
		writeStoreError(w, r, err, "SETTINGS")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": items})
}

func (s *Server) adminSetSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Settings map[string]string `json:"settings"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	allowed := map[string]bool{"registration.enabled": true, "briefing.enabled": true, "cloud.max_document_bytes": true, "maintenance.message": true}
	for key, value := range body.Settings {
		if !allowed[key] {
			writeError(w, r, http.StatusBadRequest, "SETTING_NOT_ALLOWED", "one or more settings cannot be changed at runtime")
			return
		}
		if err := s.store.SetSetting(r.Context(), principal(r).User.ID, key, value); err != nil {
			writeStoreError(w, r, err, "SETTINGS")
			return
		}
	}
	s.audit(r, principal(r).User.ID, "SYSTEM_SETTINGS_UPDATE", "SYSTEM_SETTINGS", "", map[string]any{"keys": len(body.Settings)})
	s.adminListSettings(w, r)
}
