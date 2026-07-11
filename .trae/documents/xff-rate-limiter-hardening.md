# XFF 信任与限流器加固

## Context

`clientIP()` 无条件取 `X-Forwarded-For` 首项，且未校验 `RemoteAddr` 是否来自可信代理。配合 Nginx 配置 `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for`（保留客户端伪造的首项），攻击者可轮换伪造 IP 绕过登录/注册验证码/密码重置的 IP 限流。限流器 `map[string]rateWindow` 无界增长（仅在 >10000 项时遍历清理过期项，窗口内伪造项不删除），可被高基数 key 制造内存与 CPU 压力。登录仅按 IP 限流、无 identifier 维度；验证码/重置令牌消费无失败次数上限，XFF 绕过后可暴力破解 6 位验证码。

本次修复目标：仅当 `RemoteAddr` 属于配置的可信代理 CIDR 时才解析代理头，并从右向左剥离可信代理；Nginx 覆盖客户端 XFF；登录按 IP + identifier 双维度限流；验证码/重置令牌按 challenge/token 计数并限制失败次数；限流器改用有界缓存 + TTL + 旧项淘汰；补充伪造 XFF、多级代理、直连源站与高基数 key 压测测试。

## 关键文件

- [internal/httpapi/helpers.go](file:///f:/data/classing_backend/internal/httpapi/helpers.go) — `clientIP()` 重写
- [internal/httpapi/middleware.go](file:///f:/data/classing_backend/internal/httpapi/middleware.go) — `rateLimiter` 有界化 + 多方法 API
- [internal/httpapi/handlers_auth.go](file:///f:/data/classing_backend/internal/httpapi/handlers_auth.go) — 登录 identifier 维度限流
- [internal/httpapi/server.go](file:///f:/data/classing_backend/internal/httpapi/server.go) — 注入 `loginFailLimiter`、可信代理配置
- [internal/config/config.go](file:///f:/data/classing_backend/internal/config/config.go) — 新增 `TrustedProxies` 配置
- [internal/store/migrations.go](file:///f:/data/classing_backend/internal/store/migrations.go) — 追加 `failed_attempts` 列迁移
- [internal/store/users.go](file:///f:/data/classing_backend/internal/store/users.go) — 三个 `Consume*` 增加失败计数与锁定
- [docs/部署教程-Linux.md](file:///f:/data/classing_backend/docs/部署教程-Linux.md) — Nginx XFF 指令改写
- [internal/httpapi/server_test.go](file:///f:/data/classing_backend/internal/httpapi/server_test.go) — 新增测试

## 实现步骤

### 1. 配置：可信代理 CIDR

在 `config.Config` 增加 `TrustedProxies []*net.IPNet` 字段，环境变量 `TRUSTED_PROXIES`（CSV CIDR），默认 `127.0.0.0/8,::1/128`（应用与 Nginx 同机的常见部署）。空值表示不信任任何代理（始终用 `RemoteAddr`，最安全）。解析失败或 CIDR 非法时在 `Load()` 返回错误。

### 2. 重写 `clientIP()`

把 `clientIP` 改为 `Server` 方法 `(s *Server) clientIP(r *http.Request) string`，逻辑：

1. 解析 `r.RemoteAddr` 的 IP（去端口）。
2. 若该 IP **不在** `s.cfg.TrustedProxies` 任意 CIDR 内 → 直接返回 `RemoteAddr` IP，忽略所有代理头（直连源站不可伪造）。
3. 若在可信代理内：
   - 取 `X-Forwarded-For`，按逗号分割并 trim。从**右向左**遍历，跳过属于可信代理 CIDR 的项，返回首个非可信 IP。
   - 若 XFF 为空，回退到 `X-Real-IP`（单值，可信代理设置）。
   - 若全部项均为可信代理或无代理头 → 返回 `RemoteAddr` IP。
4. 解析失败的 XFF 项跳过；全部无效则返回 `RemoteAddr` IP。

新增辅助 `(s *Server) isTrustedProxy(ip string) bool` 与 `parseIPList(header string) []string`。`net.ParseCIDR` 在启动时预解析一次（存 `[]*net.IPNet`），避免每请求解析。

### 3. 限流器有界化

重写 `rateLimiter`（[middleware.go:16-49](file:///f:/data/classing_backend/internal/httpapi/middleware.go#L16-L49)）：

```go
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
```

方法：
- `allow(key) bool` — 检查并自增（IP 维度，每次请求计数）。若 key 不存在/窗口过期，先确保容量：`sweepExpired` → 仍满则 `evictOldest`（按 `started` 最旧淘汰 25%，分摊成本）。返回 `count <= limit`。
- `isLimited(key) bool` — 仅检查不自增（identifier 维度，先判断是否已锁）。
- `recordFailure(key)` — 自增失败计数（不存在则创建）。
- `reset(key)` — 删除 key（成功时重置）。

容量上限：IP 限流器 `maxEntries=8192`，登录失败限流器 `maxEntries=8192`。`sweepExpired` 删除 `now.Sub(started) >= window` 的项；`evictOldest` 在 sweep 后仍超容量时淘汰最旧 25%。`newRateLimiter` 增加后台 `sweepLoop` goroutine（每 `window` 触发一次 `sweepExpired`），保证低流量后也能回收内存。Server 关闭时通过 `context` 取消（在 `Server` 增加 `sweeperCancel`，或接受 goroutine 随进程退出——单实例可接受后者，保持简单）。

### 4. 登录 identifier 维度限流

在 `server.go` 的 `Server` 增加 `loginFailLimiter *rateLimiter`（`newRateLimiter(5, 15*time.Minute)`，即 15 分钟内 5 次失败）。

修改 [handlers_auth.go login](file:///f:/data/classing_backend/internal/httpapi/handlers_auth.go#L166-L182)：

```go
identifier := strings.ToLower(strings.TrimSpace(body.Identifier))
if s.loginFailLimiter.isLimited(identifier) {
    writeError(w, r, http.StatusTooManyRequests, "AUTH_LOGIN_LOCKED", "too many failed login attempts, try again later")
    return
}
user, err := s.store.UserByIdentifier(r.Context(), identifier)
if err != nil || !auth.VerifyPassword(user.PasswordHash, body.Password) {
    s.loginFailLimiter.recordFailure(identifier)
    writeError(w, r, http.StatusUnauthorized, "AUTH_INVALID_CREDENTIALS", "identifier or password is incorrect")
    return
}
s.loginFailLimiter.reset(identifier)
```

注意：identifier 维度以提交的字符串为 key（不区分账户是否存在），避免账户枚举。失败才计数，成功重置，避免误锁正常用户。

### 5. 验证码/重置令牌失败次数上限

#### 5a. 迁移

在 [migrations.go](file:///f:/data/classing_backend/internal/store/migrations.go) `migrations` 切片末尾追加三条（位置版本号自动递增，`schema_migrations` 保证只执行一次）：

```sql
ALTER TABLE email_verification_challenges ADD COLUMN failed_attempts INTEGER NOT NULL DEFAULT 0
ALTER TABLE email_change_requests ADD COLUMN failed_attempts INTEGER NOT NULL DEFAULT 0
ALTER TABLE password_reset_tokens ADD COLUMN failed_attempts INTEGER NOT NULL DEFAULT 0
```

#### 5b. 消费函数改造

定义常量 `maxVerificationAttempts = 10`（6 位验证码 1M 空间，10 次尝试暴力概率 0.001%，对正常用户足够宽容）。

三个 `Consume*` 函数（[users.go:93](file:///f:/data/classing_backend/internal/store/users.go#L93)、[users.go:194](file:///f:/data/classing_backend/internal/store/users.go#L194)、[users.go:319](file:///f:/data/classing_backend/internal/store/users.go#L319)）统一模式：

1. SELECT 增加 `failed_attempts` 字段。
2. 早期判定：`row.UsedAt != 0 || row.ExpiresAt <= now || row.FailedAttempts >= maxVerificationAttempts` → 返回 `ErrForbidden`（已锁定/过期/已用）。
3. 若 `row.CodeHash != codeHash`（验证码/令牌不匹配）：
   - `UPDATE ... SET failed_attempts = failed_attempts + 1 WHERE id = ?`
   - 若递增后 `>= maxVerificationAttempts`，同语句 `used_at = now`（锁定，禁止再试）。
   - 提交事务，返回 `ErrForbidden`。
4. 匹配成功：原逻辑 `UPDATE SET used_at = now WHERE id = ? AND used_at = 0`，继续副作用。

对 `ConsumeResetToken`：它按 `token_hash` 查找（令牌本身是 32 字节随机，暴力不可行），失败计数仍加上以防日志噪声与 CPU 消耗，逻辑同上（不匹配时递增 `failed_attempts`，达到上限锁定该令牌）。

### 6. Nginx 配置修正

[docs/部署教程-Linux.md:167](file:///f:/data/classing_backend/docs/部署教程-Linux.md#L167) 改为：

```nginx
proxy_set_header X-Forwarded-For $remote_addr;
```

覆盖客户端 XFF（而非追加）。并加注释说明：应用层已校验可信代理，此处覆盖是纵深防御；如有多级代理链（CDN→Nginx），需把 CDN 网段加入 `TRUSTED_PROXIES` 并改回 `$proxy_add_x_forwarded_for`，应用会从右向左剥离可信代理。

### 7. 测试

在 [server_test.go](file:///f:/data/classing_backend/internal/httpapi/server_test.go) 新增（复用 `testClient` 与 `httptest.NewServer` 模式；`testClient.request` 增加可选 `xff` header 参数，或新增 `requestWithHeaders`）：

1. **`TestClientIPTrustedProxy`**：
   - `TrustedProxies=["127.0.0.0/8"]`（默认）。请求带 `X-Forwarded-For: 9.9.9.9, 1.2.3.4`（`httptest` 的 `RemoteAddr` 是 `127.0.0.1`，可信）→ 断言审计日志/限流 key 使用 `1.2.3.4`（右起首个非可信）。
   - `TrustedProxies=[]`（空）。同样 XFF → 断言使用 `127.0.0.1`（RemoteAddr，忽略 XFF）。
   - 单值 `X-Forwarded-For: 9.9.9.9` + 可信 RemoteAddr → 断言 `9.9.9.9`（Nginx `$remote_addr` 覆盖场景）。

2. **`TestForgedXFFRateLimitBypass`**：
   - `TrustedProxies=[]`（模拟直连源站或未配置可信代理）。
   - 轮换 `X-Forwarded-For` 发 25 次登录请求 → 第 21 次起 429（IP 维度仍为 `127.0.0.1`，伪造 XFF 无法绕过）。
   - 对照：`TrustedProxies=["127.0.0.0/8"]` + 轮换 XFF 单值（模拟旧 Nginx `$proxy_add_x_forwarded_for` 保留首项）→ 仍 429（右向左剥离后 key 不变）。

3. **`TestLoginIdentifierRateLimit`**：
   - 5 次错误密码登录同一 identifier（每次带不同伪造 XFF + 可信代理）→ 第 6 次 429 `AUTH_LOGIN_LOCKED`。
   - 正确密码登录 → 成功并重置；之后错误密码可再试 5 次。

4. **`TestVerificationCodeBruteForceCap`**：
   - 注册请求获得 `challengeId` + `devVerificationCode`。
   - 连续提交 10 次错误验证码 → 均返回 400 `AUTH_EMAIL_VERIFICATION_INVALID`。
   - 第 11 次提交**正确**验证码 → 仍返回 400（已锁定）。
   - 即使带不同伪造 XFF 也一致（证明限流不依赖 IP）。

5. **`TestRateLimiterBounded`**：
   - 单元测试 `newRateLimiter(1, time.Minute)` 设 `maxEntries=4`。
   - 插入 4 个不同 key → 全部允许。
   - 插入第 5 个不同 key → 触发 `evictOldest`，map 大小保持 ≤4。
   - 等待 `window` 过期后 `sweepExpired` 清空。

6. **`TestMultiLevelProxyChain`**：
   - `TrustedProxies=["127.0.0.0/8","10.0.0.0/8"]`。
   - 请求 `X-Forwarded-For: 203.0.113.5, 10.0.0.7` + `RemoteAddr=127.0.0.1` → 断言返回 `203.0.113.5`（跳过可信的 `10.0.0.7` 与 `127.0.0.1`）。

## 验证

```bash
cd f:/data/classing_backend
go test ./internal/httpapi/... -run 'TestClientIP|TestForgedXFF|TestLoginIdentifier|TestVerificationCodeBruteForce|TestRateLimiterBounded|TestMultiLevelProxy' -v
go test ./... # 确保无回归
go vet ./...
```

## 注意事项

- `clientIP` 改为 `Server` 方法后，所有调用点（`helpers.go` 内的 `audit`、`handlers_auth.go` 的 `verifyTurnstile`/`writeSession`/`refresh`/`requestPasswordReset`/`requestRegistrationEmail`、`middleware.go` 的限流）需改为 `s.clientIP(r)`。`audit` 已是 `Server` 方法；`verifyTurnstile` 已是 `Server` 方法。仅 `helpers.go` 顶层 `clientIP` 函数删除即可。
- 三个 `Consume*` 的 `SELECT` 列表需同步增加 `failed_attempts`，避免 `sql.ErrStructNotFound` / 列不匹配。
- `TrustedProxies` 默认包含 loopback，`httptest.NewServer` 的 `RemoteAddr=127.0.0.1` 落在可信范围内，测试默认走可信代理路径（与生产 Nginx 同机一致）。测试"直连源站"场景时显式置 `TrustedProxies` 为空。
- 不引入外部限流服务或新依赖（如 `golang.org/x/time/rate`），保持当前依赖面。
