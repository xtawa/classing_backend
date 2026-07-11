package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/xtawa/classing-backend/internal/model"
	"github.com/xtawa/classing-backend/internal/store"
)

type principalKey struct{}
type requestIDKey struct{}

type Principal struct{ User model.User }

func principal(r *http.Request) Principal {
	value, _ := r.Context().Value(principalKey{}).(Principal)
	return value
}

func requestID(r *http.Request) string {
	value, _ := r.Context().Value(requestIDKey{}).(string)
	return value
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, r, http.StatusBadRequest, "REQUEST_INVALID_JSON", "request body is invalid")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "REQUEST_INVALID_JSON", "request body must contain one JSON object")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, map[string]any{"code": code, "message": message, "requestId": requestID(r)})
}

func writeStoreError(w http.ResponseWriter, r *http.Request, err error, fallbackCode string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, r, http.StatusNotFound, fallbackCode+"_NOT_FOUND", "resource was not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, r, http.StatusConflict, fallbackCode+"_CONFLICT", "resource state conflicts with this request")
	case errors.Is(err, store.ErrForbidden):
		writeError(w, r, http.StatusForbidden, fallbackCode+"_FORBIDDEN", "operation is not allowed")
	case errors.Is(err, store.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, fallbackCode+"_INVALID", "request parameters are invalid")
	case errors.Is(err, store.ErrUnavailable):
		writeError(w, r, http.StatusTooManyRequests, fallbackCode+"_UNAVAILABLE", "resource is temporarily unavailable")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "an internal error occurred")
	}
}

func pageParams(r *http.Request) (limit, offset int) {
	limit = 50
	if value, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && value > 0 && value <= 200 {
		limit = value
	}
	if value, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && value >= 0 {
		offset = value
	}
	return
}

func (s *Server) clientIP(r *http.Request) string {
	remoteIP := remoteAddrIP(r.RemoteAddr)
	if !s.isTrustedProxy(remoteIP) {
		return remoteIP
	}
	if forwarded := lastUntrustedProxyIP(r.Header.Get("X-Forwarded-For"), s.cfg.TrustedProxies); forwarded != "" {
		return forwarded
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" && net.ParseIP(realIP) != nil {
		return realIP
	}
	return remoteIP
}

func remoteAddrIP(remoteAddr string) string {
	value := strings.TrimSpace(remoteAddr)
	if value == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return strings.Trim(value, "[]")
	}
	return host
}

func (s *Server) isTrustedProxy(ip string) bool {
	if ip == "" || len(s.cfg.TrustedProxies) == 0 {
		return false
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, cidr := range s.cfg.TrustedProxies {
		if cidr.Contains(parsed) {
			return true
		}
	}
	return false
}

// lastUntrustedProxyIP parses an X-Forwarded-For header and returns the
// rightmost IP that is not a trusted proxy. Trusted proxies are stripped from
// the right so a forged leftmost entry cannot override the real client IP
// appended by the proxy chain.
func lastUntrustedProxyIP(header string, trusted []*net.IPNet) string {
	entries := strings.Split(header, ",")
	for i := len(entries) - 1; i >= 0; i-- {
		ip := strings.TrimSpace(entries[i])
		if ip == "" {
			continue
		}
		parsed := net.ParseIP(ip)
		if parsed == nil {
			continue
		}
		if isTrusted(parsed, trusted) {
			continue
		}
		return ip
	}
	return ""
}

func isTrusted(ip net.IP, trusted []*net.IPNet) bool {
	for _, cidr := range trusted {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) spaHandler() http.Handler {
	fileServer := http.FileServer(http.FS(s.web))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, r, http.StatusNotFound, "ROUTE_NOT_FOUND", "route was not found")
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "." || name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(s.web, name); err == nil {
			if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
				w.Header().Set("Content-Type", contentType)
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		index, err := fs.ReadFile(s.web, "index.html")
		if err != nil {
			http.Error(w, "web console unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}

func accountPayload(user model.User) map[string]any {
	return map[string]any{"userId": user.ID, "identifier": user.Email, "username": user.Username, "email": user.Email, "role": user.Role, "status": user.Status, "emailVerified": user.EmailVerified != 0, "createdAt": user.CreatedAt, "updatedAt": user.UpdatedAt}
}

func membershipPayload(item model.Membership) map[string]any {
	return map[string]any{"isMember": item.ExpiresAt > nowMillisHTTP(), "tier": item.Tier, "expiresAt": item.ExpiresAt, "lastCheckedAt": nowMillisHTTP(), "source": item.Source}
}

func nowMillisHTTP() int64 { return timeNow().UnixMilli() }

var timeNow = time.Now

func parseVersion(value string) int64 {
	value = strings.Trim(strings.TrimSpace(value), `"`)
	version, _ := strconv.ParseInt(value, 10, 64)
	return version
}

func requireString(value, field string, min, max int) error {
	length := len(strings.TrimSpace(value))
	if length < min || length > max {
		return fmt.Errorf("%s is invalid", field)
	}
	return nil
}
