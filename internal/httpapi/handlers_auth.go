package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/xtawa/classing-backend/internal/auth"
	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/model"
	"github.com/xtawa/classing-backend/internal/store"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,40}$`)

type authRequest struct {
	Username   string `json:"username"`
	Email      string `json:"email"`
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if enabled, err := s.store.Setting(r.Context(), "registration.enabled", "true"); err != nil || enabled == "false" {
		writeError(w, r, http.StatusForbidden, "AUTH_REGISTRATION_DISABLED", "new account registration is disabled")
		return
	}
	var body authRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if !usernamePattern.MatchString(body.Username) {
		writeError(w, r, http.StatusBadRequest, "AUTH_USERNAME_INVALID", "username must contain 3 to 40 letters, numbers, dots, dashes or underscores")
		return
	}
	if _, err := mail.ParseAddress(body.Email); err != nil {
		writeError(w, r, http.StatusBadRequest, "AUTH_EMAIL_INVALID", "email address is invalid")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "AUTH_PASSWORD_WEAK", err.Error())
		return
	}
	user, err := s.store.CreateUser(r.Context(), body.Username, body.Email, hash, model.RoleUser)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, r, http.StatusConflict, "AUTH_ACCOUNT_EXISTS", "username or email already exists")
			return
		}
		writeStoreError(w, r, err, "AUTH_REGISTER")
		return
	}
	s.audit(r, user.ID, "AUTH_REGISTER", "USER", user.ID, map[string]any{"email": user.Email})
	s.writeSession(w, r, user, http.StatusCreated)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body authRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	user, err := s.store.UserByIdentifier(r.Context(), body.Identifier)
	if err != nil || !auth.VerifyPassword(user.PasswordHash, body.Password) {
		writeError(w, r, http.StatusUnauthorized, "AUTH_INVALID_CREDENTIALS", "identifier or password is incorrect")
		return
	}
	if user.Status != model.StatusActive {
		writeError(w, r, http.StatusForbidden, "AUTH_ACCOUNT_DISABLED", "account is disabled")
		return
	}
	s.audit(r, user.ID, "AUTH_LOGIN", "SESSION", "", nil)
	s.writeSession(w, r, user, http.StatusOK)
}

func (s *Server) writeSession(w http.ResponseWriter, r *http.Request, user model.User, status int) {
	accessToken, accessExpiresAt, err := s.tokens.Issue(user)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "AUTH_SESSION_FAILED", "could not create session")
		return
	}
	refreshToken := ids.Token(32)
	refreshExpiresAt := time.Now().Add(s.cfg.RefreshTokenTTL).UnixMilli()
	if _, err := s.store.CreateRefreshToken(r.Context(), user.ID, auth.HashOpaqueToken(refreshToken), refreshExpiresAt, clientIP(r), r.UserAgent()); err != nil {
		writeError(w, r, http.StatusInternalServerError, "AUTH_SESSION_FAILED", "could not create session")
		return
	}
	writeJSON(w, status, map[string]any{"session": map[string]any{"accessToken": accessToken, "refreshToken": refreshToken, "accessExpiresAt": accessExpiresAt, "refreshExpiresAt": refreshExpiresAt}})
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.RefreshToken) == "" {
		writeError(w, r, http.StatusBadRequest, "AUTH_REFRESH_REQUIRED", "refresh token is required")
		return
	}
	newRefresh := ids.Token(32)
	newExpiresAt := time.Now().Add(s.cfg.RefreshTokenTTL).UnixMilli()
	user, err := s.store.RotateRefreshToken(r.Context(), auth.HashOpaqueToken(body.RefreshToken), auth.HashOpaqueToken(newRefresh), newExpiresAt, clientIP(r), r.UserAgent())
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "AUTH_REFRESH_REVOKED", "refresh token is invalid, expired or already used")
		return
	}
	accessToken, accessExpiresAt, err := s.tokens.Issue(user)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "AUTH_SESSION_FAILED", "could not refresh session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": map[string]any{"accessToken": accessToken, "refreshToken": newRefresh, "accessExpiresAt": accessExpiresAt, "refreshExpiresAt": newExpiresAt}})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	user := principal(r).User
	_ = s.store.RevokeRefreshToken(r.Context(), user.ID, auth.HashOpaqueToken(body.RefreshToken))
	s.audit(r, user.ID, "AUTH_LOGOUT", "SESSION", "", nil)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Server) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	response := map[string]any{"message": "If the account exists, password reset instructions have been queued."}
	user, err := s.store.UserByIdentifier(r.Context(), strings.ToLower(strings.TrimSpace(body.Email)))
	if err == nil && strings.EqualFold(user.Email, body.Email) {
		token := ids.Token(32)
		expiresAt := time.Now().Add(s.cfg.ResetTokenTTL).UnixMilli()
		if err := s.store.CreateResetToken(r.Context(), user.ID, auth.HashOpaqueToken(token), expiresAt, clientIP(r), r.UserAgent()); err == nil {
			s.audit(r, user.ID, "AUTH_PASSWORD_RESET_REQUEST", "USER", user.ID, nil)
			if _, queueErr := s.store.QueuePasswordResetJob(r.Context(), user.ID, token, expiresAt); queueErr != nil {
				s.log.Error("queue password reset email", "user_id", user.ID, "error", queueErr, "request_id", requestID(r))
			}
			if s.cfg.ExposeResetToken && s.cfg.Environment != "production" {
				response["devResetToken"] = token
			}
		}
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (s *Server) confirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token       string `json:"token"`
		NewPassword string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "AUTH_PASSWORD_WEAK", err.Error())
		return
	}
	userID, err := s.store.ConsumeResetToken(r.Context(), auth.HashOpaqueToken(strings.TrimSpace(body.Token)), hash)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "AUTH_RESET_TOKEN_INVALID", "reset token is invalid, expired or already used")
		return
	}
	s.audit(r, userID, "AUTH_PASSWORD_RESET_CONFIRM", "USER", userID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Server) audit(r *http.Request, actorID, action, targetType, targetID string, metadata map[string]any) {
	encoded := []byte(`{}`)
	if metadata != nil {
		encoded, _ = json.Marshal(metadata)
	}
	if err := s.store.Audit(r.Context(), model.AuditLog{ActorID: actorID, Action: action, TargetType: targetType, TargetID: targetID, RequestID: requestID(r), IPAddress: clientIP(r), UserAgent: r.UserAgent(), Metadata: string(encoded)}); err != nil {
		s.log.Warn("write audit log", "error", err, "request_id", requestID(r))
	}
}
