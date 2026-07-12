# Rate Limiting for Authentication & Sensitive Endpoints

## Context

A security audit found that, while auth endpoints (`/api/v1/auth/*`) share a 20/min IP limiter and public announcement/release queries are capped at 3/min/path with `Retry-After`, several sensitive write endpoints have **no IP or account rate limiting at all**, and the existing limiters have **no account dimension** (only IP/path). Specifically:

- `POST /api/v1/membership/redeem` (兑换) — no limit; brute-force target for redeem codes
- `POST /api/v1/briefings/daily/test` (简报测试) — no limit; email/job spam vector
- `PUT /api/v1/cloud/official/document`, `PUT /api/v1/briefings/daily`, `DELETE /api/v1/briefings/daily` — no limit
- `PATCH /api/v1/account/me`, `PUT /api/v1/account/password`, `POST /api/v1/account/email/confirm` — no limit
- `GET /api/v1/client/releases/{id}/download` — public, no limit (bandwidth abuse)

The audit also flags "X-Forwarded-For 可伪造绕过" and "限流器为单机内存且无账户维度".

### Already resolved (no action needed)

- **XFF forgery**: [clientIP()](file:///f:/data/classing_backend/internal/httpapi/helpers.go#L89-L101) only parses `X-Forwarded-For` when `RemoteAddr` is a configured trusted proxy, and [lastUntrustedProxyIP()](file:///f:/data/classing_backend/internal/httpapi/helpers.go#L135-L152) strips trusted proxies right-to-left. Verified by passing test `TestForgedXFFRateLimitBypass` in [server_test.go](file:///f:/data/classing_backend/internal/httpapi/server_test.go#L681-L710). No change required.
- **Bounded caching**: existing [rateLimiter](file:///f:/data/classing_backend/internal/httpapi/middleware.go#L23-L29) already does TTL sweep + LRU eviction (`evictOldest` drops oldest 25% at capacity), satisfying the project memory constraint.
- **Single-machine in-memory**: noted as a deployment concern. Multi-instance deployments would need Redis/shared state — **out of scope** for this change; the bounded in-memory limiter is correct for single-instance and the existing pattern.

### Goal

Add IP + account-dimension rate limiting to the unprotected sensitive endpoints, with `Retry-After` on 429s, plus tests. Reuse the existing `rateLimiter` type and patterns — no new abstractions.

## Design

### 1. New limiters on `Server` ([server.go](file:///f:/data/classing_backend/internal/httpapi/server.go#L14-L38))

Add to `Server` struct and initialize in `New`:

| Limiter field | Limit | Window | Key dimension | Purpose |
|---|---|---|---|---|
| `sensitiveIPLimiter` | 60 | 1 min | IP | Shared broad IP cap across all sensitive endpoints |
| `redeemAccountLimiter` | 10 | 1 min | `user:<id>` | Redemption brute-force cap |
| `briefingTestAccountLimiter` | 5 | 1 min | `user:<id>` | Briefing test email/job spam cap |
| `cloudWriteAccountLimiter` | 30 | 1 min | `user:<id>` | Cloud writes + briefing config mutation |
| `accountWriteAccountLimiter` | 10 | 1 min | `user:<id>` | Password / email change |

All use `newRateLimiter(limit, window)` — bounded by `defaultMaxRateEntries` (8192) with existing TTL/LRU eviction.

### 2. New `sensitiveLimit` middleware ([middleware.go](file:///f:/data/classing_backend/internal/httpapi/middleware.go#L143-L163))

Add next to the existing `authRateLimit` / `publicClientRateLimit`:

```go
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
```

Keyed as `"user:" + p.User.ID` so account limits are independent of IP (an attacker rotating IPs via a botnet still hits the per-account cap).

### 3. Wire routes in [server.go](file:///f:/data/classing_backend/internal/httpapi/server.go#L40-L113)

Pattern: `s.requireAuth(s.sensitiveLimit(ipLim, acctLim)(http.HandlerFunc(handler)))` — `requireAuth` runs first (sets principal via context), then `sensitiveLimit` reads it.

| Route | IP limiter | Account limiter |
|---|---|---|
| `POST /api/v1/membership/redeem` | `sensitiveIPLimiter` | `redeemAccountLimiter` |
| `POST /api/v1/briefings/daily/test` | `sensitiveIPLimiter` | `briefingTestAccountLimiter` |
| `PUT /api/v1/cloud/official/document` | `sensitiveIPLimiter` | `cloudWriteAccountLimiter` |
| `PUT /api/v1/briefings/daily` | `sensitiveIPLimiter` | `cloudWriteAccountLimiter` |
| `DELETE /api/v1/briefings/daily` | `sensitiveIPLimiter` | `cloudWriteAccountLimiter` |
| `PATCH /api/v1/account/me` | `sensitiveIPLimiter` | `accountWriteAccountLimiter` |
| `PUT /api/v1/account/password` | `sensitiveIPLimiter` | `accountWriteAccountLimiter` |
| `POST /api/v1/account/email/confirm` | `sensitiveIPLimiter` | `accountWriteAccountLimiter` |
| `GET /api/v1/client/releases/{id}/download` | wrap with existing `publicClientRateLimit` | — |

`POST /api/v1/cloud/official/test` (cloud ping test) is a read-only ping — leave under `requireAuth` only, no new limiter.

### 4. Tests ([server_test.go](file:///f:/data/classing_backend/internal/httpapi/server_test.go))

Add focused tests reusing `newTestServerWithTrustedProxies` and `requestWithHeaders`:

- `TestRedeemAccountRateLimit`: 10 successful redeems from rotating IPs (XFF) for same account → 11th returns 429 `ACCOUNT_RATE_LIMITED` with `Retry-After: 60`. Confirms account dimension beats IP rotation.
- `TestBriefingTestAccountRateLimit`: 5 briefing tests → 6th returns 429.
- `TestCloudWriteAccountRateLimit`: 30 cloud doc puts → 31st returns 429.
- `TestAccountWriteRateLimit`: 10 password changes → 11th returns 429.
- `TestSensitiveIPRateLimit`: 60 requests across different accounts from same IP → 61st returns 429 `IP_RATE_LIMITED`.
- `TestPublicDownloadRateLimit`: 3 downloads of same release → 4th returns 429 `CLIENT_RATE_LIMITED`.

Each test uses rotating `X-Forwarded-For` (with `127.0.0.0/8` trusted) to isolate the account dimension from the IP dimension where needed.

## Files to modify

1. [internal/httpapi/server.go](file:///f:/data/classing_backend/internal/httpapi/server.go) — add 5 limiter fields, init in `New`, rewrap ~9 routes.
2. [internal/httpapi/middleware.go](file:///f:/data/classing_backend/internal/httpapi/middleware.go) — add `sensitiveLimit` middleware.
3. [internal/httpapi/server_test.go](file:///f:/data/classing_backend/internal/httpapi/server_test.go) — add ~6 tests.

No config changes (limits are code constants, matching the existing `loginFailureLimit` pattern in [handlers_auth.go](file:///f:/data/classing_backend/internal/httpapi/handlers_auth.go#L24-L27)). No store/schema changes. No new dependencies.

## Verification

```powershell
go build ./...
go vet ./...
go test ./internal/httpapi/...
```

Then manually confirm by running the new tests targeted at each limiter. The existing `TestForgedXFFRateLimitBypass`, `TestLoginIdentifierRateLimit`, `TestVerificationCodeBruteForceCap`, `TestRateLimiterBounded` must still pass unchanged.
