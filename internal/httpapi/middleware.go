package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/model"
)

type rateWindow struct {
	started time.Time
	count   int
}
type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	clients map[string]rateWindow
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, clients: map[string]rateWindow{}}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	item := l.clients[key]
	if item.started.IsZero() || now.Sub(item.started) >= l.window {
		item = rateWindow{started: now}
	}
	item.count++
	l.clients[key] = item
	if len(l.clients) > 10000 {
		for client, value := range l.clients {
			if now.Sub(value.started) > l.window {
				delete(l.clients, client)
			}
		}
	}
	return item.count <= l.limit
}

func (s *Server) authRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			writeError(w, r, http.StatusTooManyRequests, "AUTH_RATE_LIMITED", "too many authentication requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) publicClientRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.publicLimiter.allow(clientIP(r) + ":" + r.URL.Path) {
			w.Header().Set("Retry-After", "60")
			writeError(w, r, http.StatusTooManyRequests, "CLIENT_RATE_LIMITED", "maximum 3 requests per minute")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get("X-Request-ID")
		if strings.TrimSpace(id) == "" {
			id = ids.New("req")
		}
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self' https://challenges.cloudflare.com; connect-src 'self' https://challenges.cloudflare.com; frame-src https://challenges.cloudflare.com; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if origin := r.Header.Get("Origin"); origin != "" && s.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-Match, Idempotency-Key, X-Request-ID, Last-Event-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("request panic", "request_id", id, "error", recovered, "stack", string(debug.Stack()))
				writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "an internal error occurred")
			}
			s.log.Info("http request", slog.String("request_id", id), slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Duration("duration", time.Since(start)))
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(origin string) bool {
	if len(s.cfg.AllowedOrigins) == 0 {
		return false
	}
	for _, allowed := range s.cfg.AllowedOrigins {
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
			writeError(w, r, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
			return
		}
		claims, err := s.tokens.Parse(strings.TrimSpace(header[7:]))
		if err != nil {
			writeError(w, r, http.StatusUnauthorized, "AUTH_ACCESS_EXPIRED", "access token is invalid or expired")
			return
		}
		user, err := s.store.UserByID(r.Context(), claims.Subject)
		if err != nil || user.Status != model.StatusActive {
			writeError(w, r, http.StatusUnauthorized, "AUTH_ACCOUNT_DISABLED", "account is unavailable")
			return
		}
		if claims.Epoch != user.AuthEpoch {
			writeError(w, r, http.StatusUnauthorized, "AUTH_SESSION_REVOKED", "session has been revoked")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, Principal{User: user})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principal(r).User.Role != model.RoleAdmin {
			writeError(w, r, http.StatusForbidden, "ADMIN_REQUIRED", "administrator permission is required")
			return
		}
		next.ServeHTTP(w, r)
	}))
}
