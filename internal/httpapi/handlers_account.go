package httpapi

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/xtawa/classing-backend/internal/auth"
	"github.com/xtawa/classing-backend/internal/store"
)

func (s *Server) accountMe(w http.ResponseWriter, r *http.Request) {
	user := principal(r).User
	response := map[string]any{"account": accountPayload(user)}
	if newEmail, expiresAt, err := s.store.PendingEmailChange(r.Context(), user.ID); err == nil && newEmail != "" {
		response["pendingEmailChange"] = map[string]any{"newEmail": newEmail, "expiresAt": expiresAt}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username        string `json:"username"`
		Email           string `json:"email"`
		CurrentPassword string `json:"currentPassword"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if !usernamePattern.MatchString(body.Username) {
		writeError(w, r, http.StatusBadRequest, "ACCOUNT_USERNAME_INVALID", "username is invalid")
		return
	}
	currentUser := principal(r).User
	emailChanging := body.Email != "" && !strings.EqualFold(body.Email, currentUser.Email)
	profileChanging := body.Username != currentUser.Username || emailChanging
	if profileChanging && !auth.VerifyPassword(currentUser.PasswordHash, body.CurrentPassword) {
		writeError(w, r, http.StatusForbidden, "ACCOUNT_PASSWORD_CURRENT_INVALID", "current password is incorrect")
		return
	}
	if emailChanging {
		if _, err := mail.ParseAddress(body.Email); err != nil {
			writeError(w, r, http.StatusBadRequest, "ACCOUNT_EMAIL_INVALID", "email address is invalid")
			return
		}
		existing, err := s.store.UserByIdentifier(r.Context(), body.Email)
		if err == nil && existing.ID != currentUser.ID {
			writeError(w, r, http.StatusConflict, "ACCOUNT_EMAIL_CONFLICT", "email is already in use")
			return
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeStoreError(w, r, err, "ACCOUNT_EMAIL")
			return
		}
	}
	user, err := s.store.UpdateUsername(r.Context(), currentUser.ID, body.Username)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, r, http.StatusConflict, "ACCOUNT_PROFILE_CONFLICT", "username is already in use")
			return
		}
		writeStoreError(w, r, err, "ACCOUNT_PROFILE")
		return
	}
	if !emailChanging {
		s.audit(r, user.ID, "ACCOUNT_PROFILE_UPDATE", "USER", user.ID, map[string]any{"username": user.Username})
		writeJSON(w, http.StatusOK, map[string]any{"account": accountPayload(user)})
		return
	}
	code, err := numericVerificationCode()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "AUTH_VERIFICATION_FAILED", "could not create verification code")
		return
	}
	expiresAt := time.Now().Add(s.cfg.EmailVerificationTTL).UnixMilli()
	changeRequestID, err := s.store.CreateEmailChangeRequest(r.Context(), currentUser.ID, body.Email, auth.HashOpaqueToken(code), expiresAt, s.clientIP(r), r.UserAgent())
	if err != nil {
		if errors.Is(err, store.ErrUnavailable) {
			w.Header().Set("Retry-After", "60")
			writeError(w, r, http.StatusTooManyRequests, "ACCOUNT_EMAIL_RATE_LIMITED", "wait before requesting another email change")
			return
		}
		writeStoreError(w, r, err, "ACCOUNT_EMAIL")
		return
	}
	if _, err := s.store.QueueEmailChangeVerificationJob(r.Context(), currentUser.ID, code, expiresAt, body.Email); err != nil {
		s.log.Error("queue email change verification", "user_id", currentUser.ID, "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusServiceUnavailable, "AUTH_EMAIL_DELIVERY_FAILED", "verification email could not be queued")
		return
	}
	if _, err := s.store.QueueEmailChangeNotifyJob(r.Context(), currentUser.ID, body.Email, currentUser.Email); err != nil {
		s.log.Error("queue email change notification", "user_id", currentUser.ID, "error", err, "request_id", requestID(r))
	}
	s.audit(r, currentUser.ID, "ACCOUNT_EMAIL_CHANGE_REQUEST", "USER", currentUser.ID, map[string]any{"oldEmail": currentUser.Email, "newEmail": body.Email})
	response := map[string]any{
		"account":     accountPayload(user),
		"emailChange": map[string]any{"requestId": changeRequestID, "expiresAt": expiresAt, "resendAfterSeconds": 60},
	}
	if s.cfg.ExposeVerificationCode && s.cfg.Environment != "production" {
		response["devVerificationCode"] = code
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (s *Server) confirmEmailChange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RequestID        string `json:"requestId"`
		VerificationCode string `json:"verificationCode"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	user, err := s.store.ConsumeEmailChangeRequest(r.Context(), strings.TrimSpace(body.RequestID), auth.HashOpaqueToken(strings.TrimSpace(body.VerificationCode)))
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, r, http.StatusConflict, "ACCOUNT_EMAIL_CONFLICT", "email is already in use")
			return
		}
		writeError(w, r, http.StatusBadRequest, "ACCOUNT_EMAIL_VERIFICATION_INVALID", "verification code is invalid, expired or already used")
		return
	}
	s.refreshReplays.invalidateUser(user.ID)
	s.audit(r, user.ID, "ACCOUNT_EMAIL_CHANGE_CONFIRM", "USER", user.ID, map[string]any{"email": user.Email})
	writeJSON(w, http.StatusOK, map[string]any{"account": accountPayload(user), "sessionsRevoked": true})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	user := principal(r).User
	if !auth.VerifyPassword(user.PasswordHash, body.CurrentPassword) {
		writeError(w, r, http.StatusForbidden, "ACCOUNT_PASSWORD_CURRENT_INVALID", "current password is incorrect")
		return
	}
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "ACCOUNT_PASSWORD_WEAK", err.Error())
		return
	}
	if err := s.store.UpdatePassword(r.Context(), user.ID, hash); err != nil {
		writeStoreError(w, r, err, "ACCOUNT_PASSWORD")
		return
	}
	s.refreshReplays.invalidateUser(user.ID)
	s.audit(r, user.ID, "ACCOUNT_PASSWORD_CHANGE", "USER", user.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "sessionsRevoked": true})
}
