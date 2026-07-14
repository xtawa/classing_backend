package httpapi

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/xtawa/classing-backend/internal/auth"
	"github.com/xtawa/classing-backend/internal/config"
	"github.com/xtawa/classing-backend/internal/store"
)

type Server struct {
	cfg                        config.Config
	store                      *store.Store
	tokens                     *auth.Manager
	web                        fs.FS
	log                        *slog.Logger
	limiter                    *rateLimiter
	publicLimiter              *rateLimiter
	loginFailLimiter           *rateLimiter
	sensitiveIPLimiter         *rateLimiter
	redeemAccountLimiter       *rateLimiter
	briefingTestAccountLimiter *rateLimiter
	cloudWriteAccountLimiter   *rateLimiter
	accountWriteAccountLimiter *rateLimiter
	refreshReplays             *refreshReplayCache
	shuttingDown               atomic.Bool
	startedAt                  time.Time
}

func New(cfg config.Config, data *store.Store, web fs.FS, logger *slog.Logger) *Server {
	return &Server{
		cfg:                        cfg,
		store:                      data,
		tokens:                     auth.NewManager(cfg.JWTSecret, cfg.AccessTokenTTL),
		web:                        web,
		log:                        logger,
		limiter:                    newRateLimiter(20, time.Minute),
		publicLimiter:              newRateLimiter(3, time.Minute),
		loginFailLimiter:           newRateLimiter(loginFailureLimit, loginFailureWindow),
		sensitiveIPLimiter:         newRateLimiter(sensitiveIPLimit, time.Minute),
		redeemAccountLimiter:       newRateLimiter(redeemAccountLimit, time.Minute),
		briefingTestAccountLimiter: newRateLimiter(briefingTestAccountLimit, time.Minute),
		cloudWriteAccountLimiter:   newRateLimiter(cloudWriteAccountLimit, time.Minute),
		accountWriteAccountLimiter: newRateLimiter(accountWriteAccountLimit, time.Minute),
		refreshReplays:             newRefreshReplayCache(5 * time.Second),
		startedAt:                  time.Now(),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.Handle("GET /api/v1/client/announcements", s.publicClientRateLimit(http.HandlerFunc(s.publicAnnouncements)))
	mux.Handle("GET /api/v1/client/releases/latest", s.publicClientRateLimit(http.HandlerFunc(s.publicLatestRelease)))
	mux.Handle("GET /api/v1/client/releases/{id}/download", s.publicClientRateLimit(http.HandlerFunc(s.publicDownloadRelease)))

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
	mux.Handle("PATCH /api/v1/account/me", s.requireAuth(s.sensitiveLimit(s.sensitiveIPLimiter, s.accountWriteAccountLimiter)(http.HandlerFunc(s.updateAccount))))
	mux.Handle("POST /api/v1/account/email/confirm", s.requireAuth(s.sensitiveLimit(s.sensitiveIPLimiter, s.accountWriteAccountLimiter)(http.HandlerFunc(s.confirmEmailChange))))
	mux.Handle("PUT /api/v1/account/password", s.requireAuth(s.sensitiveLimit(s.sensitiveIPLimiter, s.accountWriteAccountLimiter)(http.HandlerFunc(s.changePassword))))
	mux.Handle("POST /api/v1/account/delete", s.requireAuth(s.sensitiveLimit(s.sensitiveIPLimiter, s.accountWriteAccountLimiter)(http.HandlerFunc(s.deleteAccount))))

	mux.Handle("GET /api/v1/membership/status", s.requireAuth(http.HandlerFunc(s.membershipStatus)))
	mux.Handle("POST /api/v1/membership/redeem", s.requireAuth(s.sensitiveLimit(s.sensitiveIPLimiter, s.redeemAccountLimiter)(http.HandlerFunc(s.redeemMembership))))

	mux.Handle("POST /api/v1/ai/chat", s.requireAuth(http.HandlerFunc(s.aiChat)))
	mux.Handle("GET /api/v1/ai/usage/me", s.requireAuth(http.HandlerFunc(s.aiUsage)))
	mux.Handle("GET /api/v1/ai/conversations", s.requireAuth(http.HandlerFunc(s.aiListConversations)))
	mux.Handle("GET /api/v1/ai/conversations/{id}/messages", s.requireAuth(http.HandlerFunc(s.aiMessages)))
	mux.Handle("DELETE /api/v1/ai/conversations/{id}", s.requireAuth(http.HandlerFunc(s.aiDeleteConversation)))

	mux.Handle("GET /api/v1/timetables", s.requireAuth(http.HandlerFunc(s.listTimetables)))
	mux.Handle("POST /api/v1/timetables", s.requireAuth(http.HandlerFunc(s.createTimetable)))
	mux.Handle("GET /api/v1/timetables/{id}", s.requireAuth(http.HandlerFunc(s.getTimetable)))
	mux.Handle("PUT /api/v1/timetables/{id}", s.requireAuth(http.HandlerFunc(s.updateTimetable)))
	mux.Handle("DELETE /api/v1/timetables/{id}", s.requireAuth(http.HandlerFunc(s.deleteTimetable)))

	mux.Handle("GET /api/v1/cloud/official/ping", s.requireAuth(http.HandlerFunc(s.cloudPing)))
	mux.Handle("POST /api/v1/cloud/official/test", s.requireAuth(http.HandlerFunc(s.cloudPing)))
	mux.Handle("GET /api/v1/cloud/official/config", s.requireAuth(http.HandlerFunc(s.cloudConfig)))
	mux.Handle("GET /api/v1/cloud/official/document", s.requireAuth(http.HandlerFunc(s.getCloudDocument)))
	mux.Handle("PUT /api/v1/cloud/official/document", s.requireAuth(s.sensitiveLimit(s.sensitiveIPLimiter, s.cloudWriteAccountLimiter)(http.HandlerFunc(s.putCloudDocument))))
	mux.Handle("GET /api/v1/cloud/official/events", s.requireAuth(http.HandlerFunc(s.cloudEvents)))

	mux.Handle("GET /api/v1/briefings/daily", s.requireAuth(http.HandlerFunc(s.getBriefing)))
	mux.Handle("PUT /api/v1/briefings/daily", s.requireAuth(s.sensitiveLimit(s.sensitiveIPLimiter, s.cloudWriteAccountLimiter)(http.HandlerFunc(s.putBriefing))))
	mux.Handle("DELETE /api/v1/briefings/daily", s.requireAuth(s.sensitiveLimit(s.sensitiveIPLimiter, s.cloudWriteAccountLimiter)(http.HandlerFunc(s.deleteBriefing))))
	mux.Handle("POST /api/v1/briefings/daily/test", s.requireAuth(s.sensitiveLimit(s.sensitiveIPLimiter, s.briefingTestAccountLimiter)(http.HandlerFunc(s.testBriefingV2))))

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
	mux.Handle("GET /api/v1/admin/ai/config", s.requireAdmin(http.HandlerFunc(s.adminAIConfig)))
	mux.Handle("PUT /api/v1/admin/ai/config", s.requireAdmin(http.HandlerFunc(s.adminSetAIConfig)))
	mux.Handle("POST /api/v1/admin/ai/config/test", s.requireAdmin(http.HandlerFunc(s.adminTestAIConfig)))
	mux.Handle("GET /api/v1/admin/ai/usage", s.requireAdmin(http.HandlerFunc(s.adminAIUsage)))
	mux.Handle("PUT /api/v1/admin/ai/quotas/default", s.requireAdmin(http.HandlerFunc(s.adminSetAIDefaultQuota)))
	mux.Handle("PUT /api/v1/admin/ai/quotas", s.requireAdmin(http.HandlerFunc(s.adminSetAIQuota)))

	mux.Handle("/", s.spaHandler())
	return s.middleware(mux)
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	// Network access is restricted by the loopback-only Compose port binding and
	// the Nginx allow/deny rule. A host request reaches the container through the
	// Docker bridge, so checking RemoteAddr for loopback here would reject the
	// legitimate local proxy as well.
	databaseUp := 1
	if err := s.store.Ping(r.Context()); err != nil {
		databaseUp = 0
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = fmt.Fprintf(w, "# HELP classing_up Process availability.\n# TYPE classing_up gauge\nclassing_up 1\n# HELP classing_database_up Database availability.\n# TYPE classing_database_up gauge\nclassing_database_up %d\n# HELP classing_process_uptime_seconds Process uptime.\n# TYPE classing_process_uptime_seconds gauge\nclassing_process_uptime_seconds %.0f\n", databaseUp, time.Since(s.startedAt).Seconds())
}

func (s *Server) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "classing-backend"})
}

func (s *Server) MarkShuttingDown() { s.shuttingDown.Store(true) }

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	checks := map[string]any{}
	if s.shuttingDown.Load() {
		checks["shutdown"] = "stopping"
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "checks": checks})
		return
	}
	checks["shutdown"] = "ok"
	if err := s.store.Ping(r.Context()); err != nil {
		checks["database"] = "unavailable"
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "checks": checks, "code": "SERVICE_NOT_READY"})
		return
	}
	checks["database"] = "ok"
	migrations, err := s.store.MigrationStatus(r.Context())
	if err != nil || migrations.Pending != 0 {
		checks["migrations"] = map[string]any{"status": "pending", "pending": migrations.Pending}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "checks": checks, "code": "SERVICE_MIGRATIONS_PENDING"})
		return
	}
	checks["migrations"] = "ok"
	mailReady, err := s.store.MailReady(r.Context())
	if err != nil || !mailReady {
		checks["mail"] = "degraded"
	} else {
		checks["mail"] = "ok"
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "checks": checks})
}
