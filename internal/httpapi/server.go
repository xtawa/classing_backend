package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/xtawa/classing-backend/internal/auth"
	"github.com/xtawa/classing-backend/internal/config"
	"github.com/xtawa/classing-backend/internal/store"
)

type Server struct {
	cfg     config.Config
	store   *store.Store
	tokens  *auth.Manager
	web     fs.FS
	log     *slog.Logger
	limiter *rateLimiter
}

func New(cfg config.Config, data *store.Store, web fs.FS, logger *slog.Logger) *Server {
	return &Server{cfg: cfg, store: data, tokens: auth.NewManager(cfg.JWTSecret, cfg.AccessTokenTTL), web: web, log: logger, limiter: newRateLimiter(20, time.Minute)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)

	mux.Handle("POST /api/v1/auth/register", s.authRateLimit(http.HandlerFunc(s.register)))
	mux.Handle("POST /api/v1/auth/login", s.authRateLimit(http.HandlerFunc(s.login)))
	mux.Handle("POST /api/v1/auth/refresh", s.authRateLimit(http.HandlerFunc(s.refresh)))
	mux.Handle("POST /api/v1/auth/password/reset/request", s.authRateLimit(http.HandlerFunc(s.requestPasswordReset)))
	mux.Handle("POST /api/v1/auth/password/reset/confirm", s.authRateLimit(http.HandlerFunc(s.confirmPasswordReset)))

	mux.Handle("POST /api/v1/auth/logout", s.requireAuth(http.HandlerFunc(s.logout)))
	mux.Handle("GET /api/v1/account/me", s.requireAuth(http.HandlerFunc(s.accountMe)))
	mux.Handle("PATCH /api/v1/account/me", s.requireAuth(http.HandlerFunc(s.updateAccount)))
	mux.Handle("PUT /api/v1/account/password", s.requireAuth(http.HandlerFunc(s.changePassword)))

	mux.Handle("GET /api/v1/membership/status", s.requireAuth(http.HandlerFunc(s.membershipStatus)))
	mux.Handle("POST /api/v1/membership/redeem", s.requireAuth(http.HandlerFunc(s.redeemMembership)))

	mux.Handle("GET /api/v1/timetables", s.requireAuth(http.HandlerFunc(s.listTimetables)))
	mux.Handle("POST /api/v1/timetables", s.requireAuth(http.HandlerFunc(s.createTimetable)))
	mux.Handle("GET /api/v1/timetables/{id}", s.requireAuth(http.HandlerFunc(s.getTimetable)))
	mux.Handle("PUT /api/v1/timetables/{id}", s.requireAuth(http.HandlerFunc(s.updateTimetable)))
	mux.Handle("DELETE /api/v1/timetables/{id}", s.requireAuth(http.HandlerFunc(s.deleteTimetable)))

	mux.Handle("GET /api/v1/cloud/official/ping", s.requireAuth(http.HandlerFunc(s.cloudPing)))
	mux.Handle("POST /api/v1/cloud/official/test", s.requireAuth(http.HandlerFunc(s.cloudPing)))
	mux.Handle("GET /api/v1/cloud/official/config", s.requireAuth(http.HandlerFunc(s.cloudConfig)))
	mux.Handle("GET /api/v1/cloud/official/document", s.requireAuth(http.HandlerFunc(s.getCloudDocument)))
	mux.Handle("PUT /api/v1/cloud/official/document", s.requireAuth(http.HandlerFunc(s.putCloudDocument)))

	mux.Handle("GET /api/v1/briefings/daily", s.requireAuth(http.HandlerFunc(s.getBriefing)))
	mux.Handle("PUT /api/v1/briefings/daily", s.requireAuth(http.HandlerFunc(s.putBriefing)))
	mux.Handle("DELETE /api/v1/briefings/daily", s.requireAuth(http.HandlerFunc(s.deleteBriefing)))
	mux.Handle("POST /api/v1/briefings/daily/test", s.requireAuth(http.HandlerFunc(s.testBriefing)))

	mux.Handle("GET /api/v1/admin/dashboard", s.requireAdmin(http.HandlerFunc(s.adminDashboard)))
	mux.Handle("GET /api/v1/admin/users", s.requireAdmin(http.HandlerFunc(s.adminListUsers)))
	mux.Handle("PATCH /api/v1/admin/users/{id}", s.requireAdmin(http.HandlerFunc(s.adminUpdateUser)))
	mux.Handle("POST /api/v1/admin/redeem-codes/generate", s.requireAdmin(http.HandlerFunc(s.adminGenerateRedeemCodes)))
	mux.Handle("GET /api/v1/admin/redeem-codes/query", s.requireAdmin(http.HandlerFunc(s.adminListRedeemCodes)))
	mux.Handle("POST /api/v1/admin/redeem-codes/revoke", s.requireAdmin(http.HandlerFunc(s.adminRevokeRedeemCode)))
	mux.Handle("POST /api/v1/admin/membership/grant", s.requireAdmin(http.HandlerFunc(s.adminGrantMembership)))
	mux.Handle("POST /api/v1/admin/membership/revoke", s.requireAdmin(http.HandlerFunc(s.adminRevokeMembership)))
	mux.Handle("GET /api/v1/admin/mailboxes", s.requireAdmin(http.HandlerFunc(s.adminListMailboxes)))
	mux.Handle("POST /api/v1/admin/mailboxes", s.requireAdmin(http.HandlerFunc(s.adminCreateMailbox)))
	mux.Handle("DELETE /api/v1/admin/mailboxes/{id}", s.requireAdmin(http.HandlerFunc(s.adminDeleteMailbox)))
	mux.Handle("GET /api/v1/admin/briefing-jobs", s.requireAdmin(http.HandlerFunc(s.adminListJobs)))
	mux.Handle("POST /api/v1/admin/briefing-jobs/{id}/retry", s.requireAdmin(http.HandlerFunc(s.adminRetryJob)))
	mux.Handle("GET /api/v1/admin/audit-logs", s.requireAdmin(http.HandlerFunc(s.adminListAudit)))
	mux.Handle("GET /api/v1/admin/settings", s.requireAdmin(http.HandlerFunc(s.adminListSettings)))
	mux.Handle("PUT /api/v1/admin/settings", s.requireAdmin(http.HandlerFunc(s.adminSetSettings)))

	mux.Handle("/", s.spaHandler())
	return s.middleware(mux)
}

func (s *Server) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "classing-backend"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "SERVICE_NOT_READY", "database is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}
