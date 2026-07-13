package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xtawa/classing-backend/internal/ids"
	"github.com/xtawa/classing-backend/internal/model"
)

const defaultMaxRateEntries = 8192

// Sensitive endpoint rate limits. IP dimension is shared across all sensitive
// endpoints; account dimension is per-category to match legitimate usage.
const (
	sensitiveIPLimit         = 60 // per IP per minute across all sensitive endpoints
	redeemAccountLimit       = 10 // per account per minute for redemption
	briefingTestAccountLimit = 5  // per account per minute for briefing test dispatch
	cloudWriteAccountLimit   = 30 // per account per minute for cloud writes & briefing config
	accountWriteAccountLimit = 10 // per account per minute for password/email change
)

// maxHeaderIDLen caps client-supplied identifier headers (X-Request-ID,
// Idempotency-Key) to prevent memory/log/DB bloat from oversized values.
const maxHeaderIDLen = 128

type rateWindow struct {
	started time.Time
	count   int
}
type rateLimiter struct {
	mu         sync.Mutex
	limit      int
	window     time.Duration
	maxEntries int
	clients    map[string]rateWindow
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return newRateLimiterWithCap(limit, window, defaultMaxRateEntries)
}

func newRateLimiterWithCap(limit int, window time.Duration, maxEntries int) *rateLimiter {
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &rateLimiter{limit: limit, window: window, maxEntries: maxEntries, clients: map[string]rateWindow{}}
}

// allow checks and increments the counter for key (IP-dimension: every request
// counts). Returns false when the key has exceeded the limit within the window.
func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	item, ok := l.clients[key]
	if !ok || now.Sub(item.started) >= l.window {
		l.ensureCapacity(now)
		item = rateWindow{started: now}
	}
	item.count++
	l.clients[key] = item
	return item.count <= l.limit
}

// isLimited reports whether key has reached the failure limit without
// incrementing. Used for identifier-dimension gating before credential check.
func (l *rateLimiter) isLimited(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	item, ok := l.clients[key]
	if !ok || time.Since(item.started) >= l.window {
		return false
	}
	return item.count >= l.limit
}

// recordFailure increments the failure counter for key (identifier-dimension:
// only failures count). Creates the entry if absent.
func (l *rateLimiter) recordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	item, ok := l.clients[key]
	if !ok || now.Sub(item.started) >= l.window {
		l.ensureCapacity(now)
		item = rateWindow{started: now}
	}
	item.count++
	l.clients[key] = item
}

// reset clears the counter for key. Called on success so a legit user is not
// locked out by prior typos.
func (l *rateLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.clients, key)
}

// size returns the current number of tracked keys (for tests).
func (l *rateLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.clients)
}

func (l *rateLimiter) ensureCapacity(now time.Time) {
	if len(l.clients) < l.maxEntries {
		return
	}
	l.sweepExpired(now)
	if len(l.clients) >= l.maxEntries {
		l.evictOldest()
	}
}

func (l *rateLimiter) sweepExpired(now time.Time) {
	for key, value := range l.clients {
		if now.Sub(value.started) >= l.window {
			delete(l.clients, key)
		}
	}
}

// evictOldest drops the oldest 25% of entries (by started time) so the map
// stays bounded under high-cardinality key injection. Single O(n log n) pass.
func (l *rateLimiter) evictOldest() {
	target := l.maxEntries * 3 / 4
	if target < 1 {
		target = 1
	}
	if len(l.clients) <= target {
		return
	}
	type entry struct {
		key     string
		started time.Time
	}
	entries := make([]entry, 0, len(l.clients))
	for key, value := range l.clients {
		entries = append(entries, entry{key: key, started: value.started})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].started.Before(entries[j].started) })
	toEvict := len(entries) - target
	for i := 0; i < toEvict; i++ {
		delete(l.clients, entries[i].key)
	}
}

func (s *Server) authRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allow(s.clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			writeError(w, r, http.StatusTooManyRequests, "AUTH_RATE_LIMITED", "too many authentication requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) publicClientRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.publicLimiter.allow(s.clientIP(r) + ":" + r.URL.Path) {
			w.Header().Set("Retry-After", "60")
			writeError(w, r, http.StatusTooManyRequests, "CLIENT_RATE_LIMITED", "maximum 3 requests per minute")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sensitiveLimit enforces IP + account rate limits on authenticated sensitive
// endpoints. Must be wrapped by requireAuth so the principal is available.
// Either limiter may be nil to skip that dimension.
func (s *Server) sensitiveLimit(ipLimiter, accountLimiter *rateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ipLimiter != nil && !ipLimiter.allow(s.clientIP(r)) {
				w.Header().Set("Retry-After", "60")
				writeError(w, r, http.StatusTooManyRequests, "IP_RATE_LIMITED", "too many requests from this IP")
				return
			}
			if accountLimiter != nil {
				if p := principal(r); p.User.ID != "" {
					if !accountLimiter.allow("user:" + p.User.ID) {
						w.Header().Set("Retry-After", "60")
						writeError(w, r, http.StatusTooManyRequests, "ACCOUNT_RATE_LIMITED", "too many requests from this account")
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if id == "" || len(id) > maxHeaderIDLen {
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
		active, err := s.store.SessionActive(r.Context(), user.ID, claims.Session)
		if err != nil || !active {
			writeError(w, r, http.StatusUnauthorized, "AUTH_SESSION_REVOKED", "session has been revoked")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, Principal{User: user, SessionID: claims.Session})
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
