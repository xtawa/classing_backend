package httpapi

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/xtawa/classing-backend/internal/auth"
	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/model"
	"github.com/xtawa/classing-backend/internal/store"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,40}$`)

const (
	loginFailureLimit  = 5
	loginFailureWindow = 15 * time.Minute
)

type authRequest struct {
	Username       string `json:"username"`
	Email          string `json:"email"`
	Identifier     string `json:"identifier"`
	Password       string `json:"password"`
	TurnstileToken string `json:"turnstileToken"`
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusConflict, "AUTH_EMAIL_VERIFICATION_REQUIRED", "use the email verification registration flow")
}

func (s *Server) registrationConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"turnstileRequired":         s.cfg.TurnstileRequired,
		"turnstileSiteKey":          s.cfg.TurnstileSiteKey,
		"emailVerificationRequired": true,
	})
}

func (s *Server) requestRegistrationEmail(w http.ResponseWriter, r *http.Request) {
	if enabled, err := s.store.Setting(r.Context(), "registration.enabled", "true"); err != nil || enabled == "false" {
		writeError(w, r, http.StatusForbidden, "AUTH_REGISTRATION_DISABLED", "new account registration is disabled")
		return
	}
	var body authRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if ok, err := s.verifyTurnstile(r, body.TurnstileToken); err != nil {
		s.log.Error("turnstile verification", "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusServiceUnavailable, "AUTH_TURNSTILE_UNAVAILABLE", "human verification is temporarily unavailable")
		return
	} else if !ok {
		writeError(w, r, http.StatusBadRequest, "AUTH_TURNSTILE_INVALID", "human verification failed")
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
	user, err := s.store.CreateOrRefreshPendingUser(r.Context(), body.Username, body.Email, hash)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, r, http.StatusConflict, "AUTH_ACCOUNT_EXISTS", "username or email already exists")
			return
		}
		writeStoreError(w, r, err, "AUTH_REGISTER")
		return
	}
	code, err := numericVerificationCode()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "AUTH_VERIFICATION_FAILED", "could not create verification code")
		return
	}
	expiresAt := time.Now().Add(s.cfg.EmailVerificationTTL).UnixMilli()
	challengeID, err := s.store.CreateEmailVerificationChallenge(r.Context(), user.ID, auth.HashOpaqueToken(code), expiresAt, s.clientIP(r), r.UserAgent())
	if err != nil {
		if errors.Is(err, store.ErrUnavailable) {
			w.Header().Set("Retry-After", "60")
			writeError(w, r, http.StatusTooManyRequests, "AUTH_RATE_LIMITED", "wait before requesting another verification code")
			return
		}
		writeStoreError(w, r, err, "AUTH_EMAIL_VERIFICATION")
		return
	}
	if _, err := s.store.QueueEmailVerificationJob(r.Context(), user.ID, code, expiresAt); err != nil {
		_ = s.store.CancelEmailVerificationChallenge(r.Context(), challengeID)
		s.log.Error("queue registration verification email", "user_id", user.ID, "error", err, "request_id", requestID(r))
		writeError(w, r, http.StatusServiceUnavailable, "AUTH_EMAIL_DELIVERY_FAILED", "verification email could not be queued")
		return
	}
	s.audit(r, user.ID, "AUTH_EMAIL_VERIFICATION_REQUEST", "USER", user.ID, map[string]any{"email": user.Email})
	response := map[string]any{"challenge": map[string]any{"challengeId": challengeID, "expiresAt": expiresAt, "resendAfterSeconds": 60}}
	if s.cfg.ExposeVerificationCode && s.cfg.Environment != "production" {
		response["devVerificationCode"] = code
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (s *Server) confirmRegistrationEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChallengeID      string `json:"challengeId"`
		VerificationCode string `json:"verificationCode"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	user, err := s.store.ConsumeEmailVerificationChallenge(r.Context(), strings.TrimSpace(body.ChallengeID), auth.HashOpaqueToken(strings.TrimSpace(body.VerificationCode)))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "AUTH_EMAIL_VERIFICATION_INVALID", "verification code is invalid, expired or already used")
		return
	}
	s.audit(r, user.ID, "AUTH_REGISTER", "USER", user.ID, map[string]any{"email": user.Email})
	s.writeSession(w, r, user, http.StatusCreated)
}

func numericVerificationCode() (string, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}

func (s *Server) verifyTurnstile(r *http.Request, token string) (bool, error) {
	if s.cfg.TurnstileSecret == "" {
		return !s.cfg.TurnstileRequired, nil
	}
	if strings.TrimSpace(token) == "" {
		return !s.cfg.TurnstileRequired, nil
	}
	form := url.Values{
		"secret":   {s.cfg.TurnstileSecret},
		"response": {strings.TrimSpace(token)},
		"remoteip": {s.clientIP(r)},
	}
	client := &http.Client{Timeout: 8 * time.Second}
	response, err := client.PostForm("https://challenges.cloudflare.com/turnstile/v0/siteverify", form)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	var result struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return false, err
	}
	return response.StatusCode == http.StatusOK && result.Success, nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body authRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	identifier := strings.ToLower(strings.TrimSpace(body.Identifier))
	if s.loginFailLimiter.isLimited(identifier) {
		w.Header().Set("Retry-After", "900")
		writeError(w, r, http.StatusTooManyRequests, "AUTH_LOGIN_LOCKED", "too many failed login attempts, try again later")
		return
	}
	user, err := s.store.UserByIdentifier(r.Context(), identifier)
	if err != nil || !auth.VerifyPassword(user.PasswordHash, body.Password) || user.Status != model.StatusActive {
		s.loginFailLimiter.recordFailure(identifier)
		if err == nil && user.Status != model.StatusActive {
			s.audit(r, user.ID, "AUTH_LOGIN_REJECTED", "USER", user.ID, map[string]any{"reason": "ACCOUNT_UNAVAILABLE"})
		}
		writeError(w, r, http.StatusUnauthorized, "AUTH_INVALID_CREDENTIALS", "identifier or password is incorrect")
		return
	}
	s.loginFailLimiter.reset(identifier)
	s.audit(r, user.ID, "AUTH_LOGIN", "SESSION", "", nil)
	s.writeSession(w, r, user, http.StatusOK)
}

func (s *Server) writeSession(w http.ResponseWriter, r *http.Request, user model.User, status int) {
	refreshToken := ids.Token(32)
	refreshExpiresAt := time.Now().Add(s.cfg.RefreshTokenTTL).UnixMilli()
	sessionID, err := s.store.CreateRefreshToken(r.Context(), user.ID, auth.HashOpaqueToken(refreshToken), refreshExpiresAt, s.clientIP(r), r.UserAgent())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "AUTH_SESSION_FAILED", "could not create session")
		return
	}
	accessToken, accessExpiresAt, err := s.tokens.Issue(user, sessionID)
	if err != nil {
		_ = s.store.RevokeRefreshToken(r.Context(), user.ID, auth.HashOpaqueToken(refreshToken))
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
	refreshToken := strings.TrimSpace(body.RefreshToken)
	cacheKey := refreshReplayKey(refreshToken, s.clientIP(r), r.UserAgent())
	session, err := s.refreshReplays.do(r.Context(), cacheKey, func() (refreshSession, error) {
		newRefresh := ids.Token(32)
		newExpiresAt := time.Now().Add(s.cfg.RefreshTokenTTL).UnixMilli()
		user, sessionID, err := s.store.RotateRefreshTokenSession(r.Context(), auth.HashOpaqueToken(refreshToken), auth.HashOpaqueToken(newRefresh), newExpiresAt, s.clientIP(r), r.UserAgent())
		if err != nil {
			return refreshSession{}, err
		}
		accessToken, accessExpiresAt, err := s.tokens.Issue(user, sessionID)
		if err != nil {
			return refreshSession{}, fmt.Errorf("%w: %v", errRefreshSessionIssue, err)
		}
		return refreshSession{
			UserID:           user.ID,
			AccessToken:      accessToken,
			RefreshToken:     newRefresh,
			AccessExpiresAt:  accessExpiresAt,
			RefreshExpiresAt: newExpiresAt,
		}, nil
	})
	if err != nil {
		if errors.Is(err, errRefreshSessionIssue) {
			writeError(w, r, http.StatusInternalServerError, "AUTH_SESSION_FAILED", "could not refresh session")
			return
		}
		writeError(w, r, http.StatusUnauthorized, "AUTH_REFRESH_REVOKED", "refresh token is invalid, expired or already used")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": session})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	user := principal(r).User
	_ = s.store.RevokeSession(r.Context(), user.ID, principal(r).SessionID)
	s.refreshReplays.invalidateUser(user.ID)
	s.audit(r, user.ID, "AUTH_LOGOUT", "SESSION", principal(r).SessionID, nil)
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
		tokenHash := auth.HashOpaqueToken(token)
		expiresAt := time.Now().Add(s.cfg.ResetTokenTTL).UnixMilli()
		if err := s.store.CreateResetToken(r.Context(), user.ID, tokenHash, expiresAt, s.clientIP(r), r.UserAgent()); err == nil {
			if _, queueErr := s.store.QueuePasswordResetJob(r.Context(), user.ID, token, expiresAt); queueErr != nil {
				_ = s.store.CancelResetToken(r.Context(), user.ID, tokenHash)
				s.log.Error("queue password reset email", "user_id", user.ID, "error", queueErr, "request_id", requestID(r))
			} else {
				s.audit(r, user.ID, "AUTH_PASSWORD_RESET_REQUEST", "USER", user.ID, nil)
				if s.cfg.ExposeResetToken && s.cfg.Environment != "production" {
					response["devResetToken"] = token
				}
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
	s.refreshReplays.invalidateUser(userID)
	s.audit(r, userID, "AUTH_PASSWORD_RESET_CONFIRM", "USER", userID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Server) audit(r *http.Request, actorID, action, targetType, targetID string, metadata map[string]any) {
	encoded := []byte(`{}`)
	if metadata != nil {
		encoded, _ = json.Marshal(metadata)
	}
	if err := s.store.Audit(r.Context(), model.AuditLog{ActorID: actorID, Action: action, TargetType: targetType, TargetID: targetID, RequestID: requestID(r), IPAddress: s.clientIP(r), UserAgent: r.UserAgent(), Metadata: string(encoded)}); err != nil {
		s.log.Warn("write audit log", "error", err, "request_id", requestID(r))
	}
}

func (s *Server) auditContext(r *http.Request, actorID, action, targetType, targetID string, metadata map[string]any) store.AuditContext {
	return store.AuditContext{
		ActorID:    actorID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		RequestID:  requestID(r),
		IPAddress:  s.clientIP(r),
		UserAgent:  r.UserAgent(),
		Metadata:   metadata,
	}
}
