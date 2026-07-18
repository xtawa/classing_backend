package httpapi

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/xtawa/classing-backend/internal/auth"
	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/store"
)

const (
	deviceAuthorizationTTL          = 5 * time.Minute
	deviceAuthorizationPollInterval = 5
)

func (s *Server) startDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceName string `json:"deviceName"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	deviceName := strings.TrimSpace(body.DeviceName)
	if len([]rune(deviceName)) > 80 {
		writeError(w, r, http.StatusBadRequest, "DEVICE_AUTH_NAME_TOO_LONG", "device name must be at most 80 characters")
		return
	}
	authorizationID := ids.New("dva")
	pollSecret := ids.Token(32)
	expiresAt := time.Now().Add(deviceAuthorizationTTL).UnixMilli()
	if err := s.store.CreateDeviceAuthorization(
		r.Context(),
		authorizationID,
		auth.HashOpaqueToken(pollSecret),
		deviceName,
		expiresAt,
		s.clientIP(r),
		r.UserAgent(),
	); err != nil {
		writeStoreError(w, r, err, "DEVICE_AUTH_START")
		return
	}
	qrPayload := fmt.Sprintf("classing://wear-login?authorizationId=%s", url.QueryEscape(authorizationID))
	qrPNG, err := qrcode.Encode(qrPayload, qrcode.Medium, 320)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "DEVICE_AUTH_QR_FAILED", "could not generate device authorization QR code")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"authorizationId": authorizationID,
		"pollSecret":      pollSecret,
		"qrPayload":       qrPayload,
		"qrImage":         "data:image/png;base64," + base64.StdEncoding.EncodeToString(qrPNG),
		"expiresAt":       expiresAt,
		"intervalSeconds": deviceAuthorizationPollInterval,
	})
}

func (s *Server) approveDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AuthorizationID string `json:"authorizationId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	authorizationID := strings.TrimSpace(body.AuthorizationID)
	if authorizationID == "" || len(authorizationID) > 128 {
		writeError(w, r, http.StatusBadRequest, "DEVICE_AUTH_ID_REQUIRED", "authorization id is required")
		return
	}
	userID := principal(r).User.ID
	expiresAt, err := s.store.ApproveDeviceAuthorization(r.Context(), authorizationID, userID)
	if err != nil {
		writeDeviceAuthorizationError(w, r, err)
		return
	}
	s.audit(r, userID, "DEVICE_AUTH_APPROVE", "DEVICE_AUTHORIZATION", authorizationID, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "APPROVED",
		"authorizationId": authorizationID,
		"expiresAt":       expiresAt,
	})
}

func (s *Server) pollDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AuthorizationID string `json:"authorizationId"`
		PollSecret      string `json:"pollSecret"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	authorizationID := strings.TrimSpace(body.AuthorizationID)
	pollSecret := strings.TrimSpace(body.PollSecret)
	if authorizationID == "" || pollSecret == "" || len(authorizationID) > 128 || len(pollSecret) > 256 {
		writeError(w, r, http.StatusBadRequest, "DEVICE_AUTH_CREDENTIALS_REQUIRED", "authorization id and poll secret are required")
		return
	}
	refreshToken := ids.Token(32)
	refreshExpiresAt := time.Now().Add(s.cfg.RefreshTokenTTL).UnixMilli()
	user, sessionID, err := s.store.ConsumeDeviceAuthorizationAndCreateSession(
		r.Context(),
		authorizationID,
		auth.HashOpaqueToken(pollSecret),
		auth.HashOpaqueToken(refreshToken),
		refreshExpiresAt,
		s.clientIP(r),
		r.UserAgent(),
	)
	if errors.Is(err, store.ErrDeviceAuthorizationPending) {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":          "PENDING",
			"intervalSeconds": deviceAuthorizationPollInterval,
		})
		return
	}
	if err != nil {
		writeDeviceAuthorizationError(w, r, err)
		return
	}
	accessToken, accessExpiresAt, err := s.tokens.Issue(user, sessionID)
	if err != nil {
		_ = s.store.RevokeRefreshToken(r.Context(), user.ID, auth.HashOpaqueToken(refreshToken))
		writeError(w, r, http.StatusInternalServerError, "AUTH_SESSION_FAILED", "could not create device session")
		return
	}
	membership, membershipErr := s.store.Membership(r.Context(), user.ID)
	if membershipErr != nil {
		_ = s.store.RevokeRefreshToken(r.Context(), user.ID, auth.HashOpaqueToken(refreshToken))
		writeStoreError(w, r, membershipErr, "MEMBERSHIP")
		return
	}
	s.audit(r, user.ID, "DEVICE_AUTH_CONSUME", "SESSION", sessionID, map[string]any{"authorizationId": authorizationID})
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "APPROVED",
		"session": map[string]any{
			"accessToken":      accessToken,
			"refreshToken":     refreshToken,
			"accessExpiresAt":  accessExpiresAt,
			"refreshExpiresAt": refreshExpiresAt,
		},
		"account": map[string]any{
			"userId":   user.ID,
			"username": user.Username,
			"email":    user.Email,
		},
		"membership": membershipPayload(membership),
	})
}

func writeDeviceAuthorizationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrDeviceAuthorizationExpired):
		writeError(w, r, http.StatusGone, "DEVICE_AUTH_EXPIRED", "device authorization has expired")
	case errors.Is(err, store.ErrDeviceAuthorizationConsumed):
		writeError(w, r, http.StatusGone, "DEVICE_AUTH_CONSUMED", "device authorization has already been used")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, r, http.StatusUnauthorized, "DEVICE_AUTH_INVALID", "device authorization is invalid")
	case errors.Is(err, store.ErrConflict):
		writeError(w, r, http.StatusConflict, "DEVICE_AUTH_ALREADY_APPROVED", "device authorization was approved by another account")
	case errors.Is(err, store.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "DEVICE_AUTH_ACCOUNT_UNAVAILABLE", "account is unavailable")
	default:
		writeStoreError(w, r, err, "DEVICE_AUTH")
	}
}
