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
	cfg            config.Config
	store          *store.Store
	tokens         *auth.Manager
	web            fs.FS
	log            *slog.Logger
	limiter        *rateLimiter
	publicLimiter  *rateLimiter
	refreshReplays *refreshReplayCache
}

func New(cfg config.Config, data *store.Store, web fs.FS, logger *slog.Logger) *Server {
	return &Server{
		cfg:            cfg,
		store:          data,
		tokens:         auth.NewManager(cfg.JWTSecret, cfg.AccessTokenTTL),
		web:            web,
		log:            logger,
		limiter:        newRateLimiter(20, time.Minute),
		publicLimiter:  newRateLimiter(3, time.Minute),
		refreshReplays: newRefreshReplayCache(5 * time.Second),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.Handle("GET /api/v1/client/announcements", s.publicClientRateLimit(http.HandlerFunc(s.publicAnnouncements)))
	mux.Handle("GET /api/v1/client/releases/latest", s.publicClientRateLimit(http.HandlerFunc(s.publicLatestRelease)))
	mux.HandleFunc("GET /api/v1/client/releases/{id}/download", s.publicDownloadRelease)

	mux.Handle("POST /api/v1/auth/register", s.authRateLimit(http.HandlerFunc(s.register)))
	mux.HandleFunc("GET /api/v1/auth/registration/config", s.registrationConfig)
	mux.Handle("POST /api/v1/auth/register/email/request", s.authRateLimit(http.HandlerFunc(s.requestRegistrationEmail)))
	mux.Handle("POST /api/v1/auth/register/email/confirm", s.authRateLimit(http.HandlerFunc(s.confirmRegistrationEmail)))
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
	mux.Handle("GET /api/v1/cloud/official/events", s.requireAuth(http.HandlerFunc(s.cloudEvents)))

	mux.Handle("GET /api/v1/briefings/daily", s.requireAuth(http.HandlerFunc(s.getBriefing)))
	mux.Handle("PUT /api/v1/briefings/daily", s.requireAuth(http.HandlerFunc(s.putBriefing)))
	mux.Handle("DELETE /api/v1/briefings/daily", s.requireAuth(http.HandlerFunc(s.deleteBriefing)))
	mux.Handle("POST /api/v1/briefings/daily/test", s.requireAuth(http.HandlerFunc(s.testBriefingV2)))

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
	mux.Handle("PUT /api/v1/admin/mailboxes/{id}", s.requireAdmin(http.HandlerFunc(s.adminUpdateMailbox)))
	mux.Handle("DELETE /api/v1/admin/mailboxes/{id}", s.requireAdmin(http.HandlerFunc(s.adminDeleteMailbox)))
	mux.Handle("GET /api/v1/admin/briefing-jobs", s.requireAdmin(http.HandlerFunc(s.adminListJobs)))
	mux.Handle("POST /api/v1/admin/briefing-jobs/{id}/retry", s.requireAdmin(http.HandlerFunc(s.adminRetryJob)))
	mux.Handle("GET /api/v1/admin/audit-logs", s.requireAdmin(http.HandlerFunc(s.adminListAudit)))
	mux.Handle("GET /api/v1/admin/settings", s.requireAdmin(http.HandlerFunc(s.adminListSettings)))
	mux.Handle("PUT /api/v1/admin/settings", s.requireAdmin(http.HandlerFunc(s.adminSetSettings)))
	mux.Handle("GET /api/v1/admin/announcements", s.requireAdmin(http.HandlerFunc(s.adminListAnnouncements)))
	mux.Handle("POST /api/v1/admin/announcements", s.requireAdmin(http.HandlerFunc(s.adminCreateAnnouncement)))
	mux.Handle("PATCH /api/v1/admin/announcements/{id}", s.requireAdmin(http.HandlerFunc(s.adminUpdateAnnouncement)))
	mux.Handle("DELETE /api/v1/admin/announcements/{id}", s.requireAdmin(http.HandlerFunc(s.adminDeleteAnnouncement)))
	mux.Handle("GET /api/v1/admin/releases", s.requireAdmin(http.HandlerFunc(s.adminListReleases)))
	mux.Handle("POST /api/v1/admin/releases", s.requireAdmin(http.HandlerFunc(s.adminUploadRelease)))
	mux.Handle("POST /api/v1/admin/releases/{id}/publish", s.requireAdmin(http.HandlerFunc(s.adminPublishRelease)))
	mux.Handle("DELETE /api/v1/admin/releases/{id}", s.requireAdmin(http.HandlerFunc(s.adminDeleteRelease)))

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
