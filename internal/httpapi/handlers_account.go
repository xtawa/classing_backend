package httpapi

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/xtawa/classing-backend/internal/auth"
	"github.com/xtawa/classing-backend/internal/store"
)

func (s *Server) accountMe(w http.ResponseWriter, r *http.Request) {
	user := principal(r).User
	writeJSON(w, http.StatusOK, map[string]any{"account": accountPayload(user)})
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Email    string `json:"email"`
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
	if _, err := mail.ParseAddress(body.Email); err != nil {
		writeError(w, r, http.StatusBadRequest, "ACCOUNT_EMAIL_INVALID", "email address is invalid")
		return
	}
	user, err := s.store.UpdateProfile(r.Context(), principal(r).User.ID, body.Username, body.Email)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, r, http.StatusConflict, "ACCOUNT_PROFILE_CONFLICT", "username or email is already in use")
			return
		}
		writeStoreError(w, r, err, "ACCOUNT_PROFILE")
		return
	}
	s.audit(r, user.ID, "ACCOUNT_PROFILE_UPDATE", "USER", user.ID, map[string]any{"email": user.Email, "username": user.Username})
	writeJSON(w, http.StatusOK, map[string]any{"account": accountPayload(user)})
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
