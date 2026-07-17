# Changelog

## 2026-07-17 · Web 课表实时管理、审计保留和富文本问答

### Added

- Web 课表页可直接新增、编辑和删除 Mobile 使用的 `timetable.lessons`，写入沿用官方云 ETag、幂等键、V2 逻辑版本与删除墓碑，并通过 SSE 实时通知 Mobile 合并。
- 审计日志页支持分页，并可配置 `audit.retention_days`（1–3650 天）；保存后立即清理一次，后台任务持续自动清理。
- 新增 `DELETE /api/v1/admin/users/{id}`；管理员可删除未验证、已停用或普通用户，服务端脱敏身份、撤销会话并释放原邮箱和用户名，同时禁止管理员删除自己。
- Web Ask AI 的历史消息与流式回答支持安全 Markdown 富文本渲染。

### Client Impact

- Mobile Ask AI 同步支持标题、列表、引用、代码块、粗体、斜体、删除线和链接样式。
- Mobile 已有官方云 SSE 控制器会在 Web 保存课程后立即拉取最新文档并应用课表域。

## 2026-07-17 · Wear 二维码设备登录

### Added

- 新增 `POST /api/v1/auth/device/qr/start`、`/poll` 与需登录的 `/approve`。
- 二维码只携带公开授权编号；Wear 独占的轮询密钥只保存哈希。
- 授权 5 分钟过期、只能批准和兑换一次；兑换时原子创建独立 Wear 会话。
- 新增 `device_authorizations` 迁移、过期清理、审计记录和完整端到端测试。

### Client Impact

- Mobile 账号页仅在已登录时提供扫码批准，并在批准前二次确认。
- Wear 轮询间隔为 5 秒，成功后必须加密保存会话；Token 不得写入二维码、Data Layer 或云文档。
- 详细契约见 [API-Wear二维码登录.md](./API-Wear二维码登录.md)。

## 2026-07-13 · Lark SMTP 适配与邮件任务详细日志

### Added

- 管理台“邮件与任务”新增 Lark 公共邮箱预设，可一键填入 `smtp.larksuite.com`、SSL 465 或 STARTTLS 587、公共邮箱用户名和 `env:LARK_SMTP_PASSWORD`。
- 新增 `briefing_job_logs` 表，邮件 worker 会记录任务领取、邮箱选择、SMTP 连接、STARTTLS、认证、MAIL FROM、RCPT TO、DATA、服务器接受和完成状态。
- `GET /api/v1/admin/briefing-jobs` 额外返回 `jobLogs`，Web 端可在任务行中展开查看结构化详情。

### Changed

- SMTP 连接改为显式 10 秒拨号超时；465 使用隐式 TLS，其他端口使用 STARTTLS if available，并在失败时保存具体阶段和脱敏详情。
- `.env.example` 和 Linux 部署文档补充 `LARK_SMTP_PASSWORD` 配置说明。

## 2026-07-13 · 账号注销、协议确认与过期会员设置同步

### Added

- 新增 `POST /api/v1/account/delete`，要求当前密码与二次确认文本，成功后软删除账户并撤销该账户全部设备会话。
- `GET /api/v1/auth/registration/config` 新增 `legalAgreementUrls`，由 `LEGAL_PRIVACY_URL`、`LEGAL_TERMS_URL`、`LEGAL_CROSS_BORDER_URL` 配置；未配置时三项均回落到 `https://lyxyy.notion.site/classing-user-policy`。
- Web 登录、邮箱注册申请和邮箱注册确认必须携带三项协议同意；缺失或未全选返回 `400 AUTH_CONSENT_REQUIRED`。

### Changed

- 官方云 SSE 事件统一为 `cloud-document`，`Last-Event-ID` 明确定义为云文档整数版本，事件流只通知版本和更新时间，不传输设置正文。
- 会员过期或非会员账户仍可同步设置域；课表域在 GET 中被过滤、PUT 中被忽略，服务端保留既有课表数据但不继续下发或合并。

### Client Impact

- 登录页和注册页必须展示并校验《隐私政策》《用户协议》《个人数据跨境传输协议》复选框，未勾选不得提交。
- 注销成功后客户端必须清空本地凭据并回到登录页；其他设备会在下一次请求或刷新时失效。

## 2026-07-13 · Code review 异常修复：可撤销会话、条件同步与可靠部署

### Changed

- JWT 现在携带服务端 `sessionId`；旧 token、已撤销会话和刷新重放会被拒绝。退出仅撤销当前会话，密码重置、账户禁用及高风险角色/状态变更撤销全部相关会话。
- 注册、登录、资料修改和管理员用户变更补齐严格输入校验、统一外部登录错误、Turnstile 必需/可选策略、邮件入队回滚、最后管理员并发保护和事务审计。
- 官方云文档严格校验 v2 schema、大小和深度；GET 支持 `If-None-Match`/304，PUT 强制带带引号的 `If-Match`，文档写入、幂等响应与审计同事务提交。
- 课表增加名称筛选、管理员 owner 筛选、时区/学期/周数/文档 schema 与 2 MiB 限制；会员授予支持延长、缩短、撤销和事务审计，兑换码查询默认脱敏。
- 每日简报按用户本地日期补偿漏跑，禁用订阅会取消待处理任务；PostgreSQL 任务领取和 SMTP 邮箱配额使用行锁/`SKIP LOCKED`，连接或认证失败可切换邮箱且错误详情脱敏。
- `/health/live` 仅报告进程存活；`/health/ready` 检查关闭状态、数据库和迁移，邮件不可用以 `checks.mail=degraded` 展示但保持 200。新增仅本机可访问的 `/metrics`。
- 发布查询严格验证平台、Stable/Beta 通道和版本号并返回 `forceUpdate`；上传增加磁盘余量、临时文件同步和目录同步，下载复核大小与 SHA-256，删除先隔离文件再提交数据库事务。
- Dockerfile 移除匿名 `/data` 卷；Compose 补齐日志轮换、Turnstile、邮件验证和可信代理配置。新增联合备份、隔离恢复校验、仓库内 Nginx 配置和先构建后切换的可重试自动部署流程。

### Client Impact

- 所有既有登录会话需重新登录。
- 官方云 PUT 必须发送 `If-Match: "<version>"`，首次写入使用 `"0"`。
- Mobile 可持久选择 Stable/Beta 通道，并统一处理错误码、`Retry-After`、Range 下载、容量和 SHA-256。

## 2026-07-12 · 请求头长度限制与安全头/CORS/超时测试补全

### Changed

- `X-Request-ID` 客户端传入值超过 128 字节时，服务端忽略该值并生成新的 `req_` 前缀 ID，防止超长值污染日志、上下文与错误响应（`internal/httpapi/middleware.go`）。128 字节以内的值仍原样回显。
- `Idempotency-Key` 客户端传入值超过 128 字节时，`PUT /api/v1/cloud/official/document` 返回 `400 IDEMPOTENCY_KEY_TOO_LONG`，防止超长 key 写入 `idempotency_keys` 表导致存储膨胀（`internal/httpapi/handlers_cloud.go`）。
- 新增 `maxHeaderIDLen = 128` 常量，统一约束两个客户端标识头长度。

### Added

- 新增 `internal/httpapi/middleware_test.go`（8 个测试）：安全头存在性（CSP/HSTS/nosniff/Referrer/Permissions-Policy）、HSTS 仅在 `X-Forwarded-Proto: https` 时下发、CORS 允许源反射与拒绝、CORS 预检 OPTIONS → 204、4 MiB JSON body 上限、X-Request-ID 长度限制、Idempotency-Key 长度限制。

### Affected Endpoints

| 路由 | 变更 | 新增错误码 |
|---|---|---|
| 所有路由 | `X-Request-ID` > 128 字节时静默替换为生成值 | — |
| `PUT /api/v1/cloud/official/document` | `Idempotency-Key` > 128 字节时拒绝 | `400 IDEMPOTENCY_KEY_TOO_LONG` |

### Client Impact

- **影响端**：Android Mobile、Android Wear、Web Admin。
- 使用 UUID（36 字符）作为 `Idempotency-Key` 的客户端不受影响；128 字节上限覆盖 UUID、nanoid 等所有常见标识格式。
- `X-Request-ID` 变更对客户端透明：无论客户端是否发送、发送多长，响应始终携带有效的 `X-Request-ID`。
- 客户端适配详情见 [WearOS CHANGELOG](../../WearOS_ClassingTimeTable/docs/CHANGELOG.md)。

### Verified

- `go test ./internal/httpapi/` 全绿（含 8 个新增测试与既有回归测试）。
- `go vet ./internal/httpapi/` 无警告。

## 2026-07-12 · 敏感接口限流：账户维度与 IP 维度

### Changed

- 兑换、简报测试、官方云写入、每日简报配置变更、密码修改与邮箱变更确认等敏感写接口新增双维度限流：IP 维度（60 次/分钟/IP，敏感接口共享）与账户维度（5–30 次/分钟/账户，按接口风险分级）。超限分别返回 `429 IP_RATE_LIMITED` 或 `429 ACCOUNT_RATE_LIMITED`，并携带 `Retry-After: 60`。
- 公开下载接口 `GET /api/v1/client/releases/{id}/download` 纳入公共客户端限流（3 次/分钟/IP+路径），与公告查询、最新版本查询共享限流策略与 `CLIENT_RATE_LIMITED` 错误码。
- 新增 `sensitiveLimit` 中间件（`internal/httpapi/middleware.go`），在 `requireAuth` 之后执行，同时校验 IP 与账户两个维度；账户键为 `user:<userId>`，IP 轮换无法绕过账户维度限流。

### Added

- 新增 5 个限流器（`internal/httpapi/server.go`）：`sensitiveIPLimiter`（60/min/IP）、`redeemAccountLimiter`（10/min/account）、`briefingTestAccountLimiter`（5/min/account）、`cloudWriteAccountLimiter`（30/min/account）、`accountWriteAccountLimiter`（10/min/account）。均复用 `rateLimiter` 类型，受 8192 上限与 TTL/LRU 驱逐约束。
- 新增 7 个限流回归测试（`internal/httpapi/server_test.go`）：中间件单元测试（IP+账户双维度）、兑换/简报测试/云写入/账户写入账户维度测试、敏感接口 IP 维度测试、公共下载限流测试。

### Affected Endpoints

| 路由 | IP 限制 | 账户限制 | 新增错误码 |
|---|---|---|---|
| `POST /api/v1/membership/redeem` | 60/min | 10/min | `IP_RATE_LIMITED` / `ACCOUNT_RATE_LIMITED` |
| `POST /api/v1/briefings/daily/test` | 60/min | 5/min | `IP_RATE_LIMITED` / `ACCOUNT_RATE_LIMITED` |
| `PUT /api/v1/cloud/official/document` | 60/min | 30/min | `IP_RATE_LIMITED` / `ACCOUNT_RATE_LIMITED` |
| `PUT /api/v1/briefings/daily` | 60/min | 30/min | `IP_RATE_LIMITED` / `ACCOUNT_RATE_LIMITED` |
| `DELETE /api/v1/briefings/daily` | 60/min | 30/min | `IP_RATE_LIMITED` / `ACCOUNT_RATE_LIMITED` |
| `PATCH /api/v1/account/me` | 60/min | 10/min | `IP_RATE_LIMITED` / `ACCOUNT_RATE_LIMITED` |
| `PUT /api/v1/account/password` | 60/min | 10/min | `IP_RATE_LIMITED` / `ACCOUNT_RATE_LIMITED` |
| `POST /api/v1/account/email/confirm` | 60/min | 10/min | `IP_RATE_LIMITED` / `ACCOUNT_RATE_LIMITED` |
| `GET /api/v1/client/releases/{id}/download` | 3/min/IP+路径 | — | `CLIENT_RATE_LIMITED` |

### Client Impact

- **影响端**：Android Mobile、Android Wear、Web Admin。
- 上述接口超限时返回 429 并携带 `Retry-After: 60`，客户端应按该值退避，不应立即重试。
- 下载接口 3 次/分钟限制与公告/版本查询共享预算，客户端下载失败重试应指数退避。
- 客户端适配详情见 [客户端影响-敏感接口限流.md](../../WearOS_ClassingTimeTable/docs/客户端影响-敏感接口限流.md)。

### Verified

- `go build ./...` 与 `go vet ./...` 通过。
- `go test ./internal/httpapi/...` 全绿（含 7 个新增测试与既有回归测试）；`TestForgedXFFRateLimitBypass`、`TestLoginIdentifierRateLimit`、`TestVerificationCodeBruteForceCap`、`TestRateLimiterBounded` 等既有测试未受影响。

## 2026-07-12 · 版本发布流程缺口复核

### Reviewed
- 版本发布流程已具备 E2E 覆盖：草稿/发布/删除、`LatestRelease` 选择、`versionCode` 比较、`ETag`、`ServeContent` Range/长度与哈希元数据（`internal/httpapi/server_test.go::TestAnnouncementsAndReleaseUploadDownload`，含 `Range: bytes=0-7` → 206 + `Content-Length: 8` 断言）。

### Gaps
- **channel 未限制**：`internal/store/releases.go` 的 `normalizeChannel` / `CreateRelease` 未校验渠道白名单，任意字符串均可入库。
- **minSupported 可为负**：`CreateRelease` 未校验 `MinSupportedVersionCode` 下界，负值可入库。
- **发布不复核文件/hash**：`internal/httpapi/handlers_releases.go` 的 `adminPublishRelease` 仅翻转 `status`，未重新校验磁盘文件存在性 / 大小 / SHA-256 与记录一致。
- **删除产生孤儿**：`adminDeleteRelease` 先删数据库（事务）再 best-effort 删除文件，文件清理失败会留下孤儿文件（存储泄漏）。
- **下载不验证磁盘文件**：`publicDownloadRelease` 按记录名直接 `ServeContent`，未校验实际文件大小 / 哈希与记录一致；`ETag` 取自记录值而非实际文件。

### Client Impact
- 结论：**无需客户端适配**。客户端已具备完整下载完整性防线与失败容错，五个缺口均在客户端被兜底或不可达。
- 依据（WearOS 客户端 `mobile/src/main/java/com/classing/mobile/timetable/update/UpdateApiClient.kt`）：
  - **下载完整性**：`downloadRelease` 写盘同时流式计算 SHA-256，完成后比对 `release.sha256` 与 `artifactSize`，不匹配则丢弃 `.part` 且不唤起安装器（L160-166）。客户端不信任服务端 `ETag` →「下载不验证磁盘文件」缺口被客户端兜底。
  - **下载失败容错**：HTTP 非 2xx（含 404）即 `Result.failure`，UI 显示「下载失败」（L132-135, MobileSettingsAbout.kt L2337）→「发布不复核」「删除竞态」导致的 404 已被容错。
  - **channel**：`checkLatest` 硬编码 `channel=STABLE`（L100），不会发送未知渠道 →「channel 未限制」对客户端不可达。
  - **minSupportedVersionCode**：仅解析入 `AppUpdateRelease` 字段（L202），`checkLatest` 与 UI 均未用于强制升级门禁 → 负值被忽略。

### Backend Hardening (Applied)
- `CreateRelease`（`internal/store/releases.go`）：新增 `validChannel` 白名单（`STABLE` / `BETA`），未知渠道返回 `ErrInvalid`；`MinSupportedVersionCode < 0` 返回 `ErrInvalid`。
- `adminPublishRelease`（`internal/httpapi/handlers_releases.go`）：发布前调用新增 `verifyReleaseArtifact` 重新 stat + 大小 + SHA-256 复核磁盘文件，与记录不一致返回 `409 RELEASE_ARTIFACT_MISMATCH` 且不翻转状态。
- `publicDownloadRelease`：提供前校验实际文件大小 == 记录 `ArtifactSize`，不一致返回 `409 RELEASE_ARTIFACT_MISMATCH`（每请求一次 stat，廉价防线；完整哈希复核仍由客户端 SHA-256 兜底，不在下载路径重算）。
- `adminDeleteRelease`：**保持 DB-first 顺序不变**（允许在途下载完成，避免 broken release 暴露给客户端）；孤儿文件清理失败为已知存储泄漏，建议后续引入 reaper / 管理端清理工具，而非重排为 file-first（会中断在途下载、损害客户端体验）。
- `docs/API-公告与版本发布.md`：补充 `channel` 白名单、`minSupportedVersionCode` 非负、发布复核与下载大小校验的契约说明，新增 `RELEASE_ARTIFACT_MISMATCH` 错误码。
- `go build ./...` 与 `go vet ./...` 通过。

## 2026-07-12 · 数据库迁移测试与事务一致性修复

### Changed

- `SetMembership` 管理员会员调整现已在单个数据库事务内完成会员表更新与 `membership_events` 审计事件插入；事件插入失败不再被吞掉，整个操作回滚。同时在事务内读取旧值，消除读-写 TOCTOU 竞态。
- `DeleteRelease` 管理员删除版本现已在单个事务内完成 `app_releases` 行删除与 `audit_logs` 审计写入，保证审计记录与业务删除原子提交。文件系统清理改为事务提交后 best-effort 执行，失败时记录告警日志而非静默忽略。
- 新增 `AuditContext` 类型与 `auditInTx` 方法，支持将 HTTP 级审计元数据（requestID / IP / UA）传入 store 层事务内写入。

### Added

- 新增 `migrate-status` CLI 子命令，输出已应用 / 可用 / 待处理迁移数量；有 pending 迁移时退出码为 1，可用于部署前检查。该命令不执行迁移，仅查询当前状态。
- 新增双方言测试 harness（`internal/store/testdb_test.go`），通过 `TEST_POSTGRES_DSN` 环境变量门控：未设置时仅跑 SQLite，设置后自动追加 PostgreSQL 子测试（重建 public schema 获得干净状态）。
- 新增 3 个 DB 级并发回归测试：并发兑换同一 CAMPAIGN 码（20 goroutine / 恰好 max_redemptions 成功）、并发写入同一云文档版本（10 / 1 成功）、并发轮换同一 refresh token（2 / 1 成功）。
- 新增 2 个事务一致性测试：`SetMembership` 验证会员与事件同时写入、`DeleteRelease` 验证删除与审计同时写入。
- 迁移测试改造为表驱动双方言：`TestMigrateEmptyDB`（空库初始化全表校验）、`TestMigrateIdempotent`（连续迁移幂等性）、`TestMigrateRepairsLegacyVersionRegistry`（旧版本号错位修复）均自动覆盖 SQLite + PostgreSQL。
- 新增 [docs/migration-management.md](migration-management.md)：迁移机制说明、新增迁移幂等规范、`migrate-status` 用法、PostgreSQL / SQLite 快照回滚流程。
- Makefile 新增 `test-race`（`go test -race ./...`）与 `test-pg`（传递 `TEST_POSTGRES_DSN`）target。

### Verified

- `go test ./...` 全绿（auth / httpapi / store）；`go vet ./...` 无警告。
- `migrate-status` 在空库输出 `Applied: 0, Available: 31, Pending: 31`，退出码 1。
- 并发测试在 SQLite（串行化）下验证逻辑正确性；PostgreSQL 路径在设置 `TEST_POSTGRES_DSN` 后自动激活。
- `-race` 检测需 CGO + gcc，当前 Windows 环境不可用，Makefile target 已就绪供 Linux CI 使用。

## 2026-07-11 · 官方云会话刷新竞态修复

### Fixed

- 修复 Android 同一时刻触发多次官方云同步时，多个请求并发轮换同一个一次性 refresh token，导致一个请求成功而另一个请求返回 `401 AUTH_REFRESH_REVOKED`、进而误报官方云连接失败的问题。
- 后端对相同 refresh token、客户端 IP 和 User-Agent 增加 5 秒 single-flight 与成功响应重放窗口；窗口内返回完全相同的新 access token、refresh token 与过期时间，不再重复旋转令牌。失败结果不会缓存。
- 密码修改、密码重置、管理员更新账户状态或角色以及登出现在会立即清理该用户的短时 refresh replay；账户撤销后，即使仍在 5 秒窗口内，最初的旧 token 也不会再收到缓存的 200 响应。
- 修复非会员设置同步在 GET 后原样 PUT 时重复追加同一批 `changes` 的问题；服务端现在按 change ID 稳定合并，并在缺少 ID 时按规范化内容去重，避免云文档持续膨胀直至触发大小限制。

### Verified

- 新增接口回归测试，验证同一旧 refresh token 紧接请求两次均返回 200 且 replacement session 完全一致，并保留密码变更后旧 access token 和 refresh token 失效的断言。
- 新增并发 single-flight 与错误不缓存单元测试。
- 新增撤销一致性回归测试，验证首次 refresh 后立即修改密码，再次提交最初旧 refresh token 必须返回 401。
- 新增非会员官方云重复 PUT 回归测试，连续写回同一文档时 `changes` 数量保持不变。

## 2026-07-11 · 云同步连接与设置实时同步修复

### Changed

- 官方云接口拆分为登录级设置同步与会员级课表同步：登录用户默认可同步设置，非会员读取/写入时只处理设置与 App 命令域，不再同步课表域。
- Web 与客户端设置同步不再依赖客户端当前选择的云同步方式；即便客户端选择 Google Drive 或 WebDAV，也会在登录后通过官方云保持设置实时同步。
- 每日简报 Web 测试新增 App 通知通道，后端通过官方云 `app.commands` 下发 `DAILY_BRIEFING_TEST`，客户端收到后弹出每日简报测试提醒。
- Google Drive 客户端授权状态按 14 天保存，短期 access token 到刷新时间或遇到 401 时自动静默刷新并重试；Drive 文档版本改用文件 metadata `version`，不再依赖媒体下载响应的 ETag。

### Verified

- 新增后端测试覆盖非会员官方云设置同步、课表域过滤以及 App 测试简报命令下发。

## 2026-07-11 · Turnstile 生产配置

### Fixed

- Docker Compose 现在会把 `TURNSTILE_SITE_KEY`、`TURNSTILE_SECRET` 和 `EMAIL_VERIFICATION_TTL` 从服务器 `.env` 映射到后端容器，确保生产注册真正启用 Turnstile 与 SMTP 验证策略。

### Verified

- 部署后通过注册配置接口确认 Turnstile 已强制启用，并验证缺少 Turnstile token 的注册申请会被拒绝。

## 2026-07-10 · 注册验证、实时设置同步与管理台维护

### Added

- 注册新增 Cloudflare Turnstile 配置接口、SMTP 验证码申请/确认接口及待验证账户状态。
- 新增 `EMAIL_VERIFICATION` 邮件任务、一次性验证码表和 10 分钟默认有效期。
- Web 设置页可读写官方云 v2 的 `mobile.settings` Domain，并通过带认证的 SSE 事件流实时感知版本变化。
- 管理台用户目录新增会员吊销；SMTP 邮箱新增编辑和删除操作及 `PUT /api/v1/admin/mailboxes/{id}`。
- Android 注册页新增内嵌 Turnstile 与 SMTP 验证码两阶段交互。
- Android 设置新增 Dev Mode、Web 入口、新学期切换；设置分组整合为课程显示、提醒和关于。
- 课表导入的 ICS、JSON、手动录入改为互斥折叠菜单。

### Changed

- Google Drive 与官方云连接测试不再被“同步总开关”或本地会员缓存提前拦截；官方云 access token 到期时自动使用 refresh token 轮换。
- 已是会员的账户页隐藏兑换入口。
- 完整移除 mobile 与 Wear 的无障碍保活服务、manifest 声明、设置项和同步字段。

### Security

- 公告和最新版本接口服务端按 IP/路径限制为每分钟 3 次，Android 端同时执行本地 3 次/分钟保护。
- Turnstile secret 只读取环境变量；SMTP 继续只保存 `env:` Secret 引用，不落库明文密码。

### Docs

- 新增《API-注册验证与Web设置实时同步》，同步更新官方云和管理台接口清单。

## 2026-07-10 · 公告与版本发布

### Added

- 新增公开公告接口和管理员公告管理接口，支持平台、优先级、定时发布和过期时间。
- 新增 APK 版本存储、草稿、发布、查询、删除及公共下载接口。
- 管理台新增“公告与版本”页面，可直接发布公告、上传 APK、发布草稿和删除版本。
- 客户端公告与检测更新统一接入 `https://api-classing.underflo.ink`。
- 客户端新增一键下载更新、实时进度、文件大小和 SHA-256 校验，以及系统安装器唤起。
- 新增 FileProvider 和安装未知应用权限接入。

### Security

- APK 上传限制大小，只接受 `.apk`，并验证压缩包内存在 `AndroidManifest.xml`。
- 安装包采用临时文件写入和原子改名；数据库写入失败时自动清理文件。
- 公开下载仅允许 `PUBLISHED` 版本，并提供 SHA-256 ETag 与 Range 支持。

### Fixed

- 修复从旧数据库升级时公告表和简报任务 payload 字段可能因迁移版本错位而缺失的问题。
- 修复 Docker 新建 APK 持久卷默认归属 root、导致应用用户无法写入的问题。

### Verified

- 新增公告发布、APK 上传、最新版本查询、Range 下载和删除清理的端到端后端测试。
- 客户端下载器在安装前验证文件大小及 SHA-256。

## 2026-07-10

### Fixed

- 修复 Android 账户 API 在界面线程执行导致登录、注册和账户刷新失败的问题。
- 将账户入口重构为登录主页，并把注册和找回密码拆分为二级页面。
- 补充 access token 到期时间持久化、refresh token 轮换及官方云自动续期逻辑。
- 修复 Android 12+ 未授权精确闹钟时启用每日简报可能崩溃的问题，自动降级为非精确调度。
- 后端现在会为密码重置申请创建 `PASSWORD_RESET` 邮件任务；邮件发送成功后清除任务中的敏感 payload。
- 后端严格校验每日简报的 `HH:mm` 和 IANA 时区，拒绝 `99:99` 等无效设置。
- 补齐 mobile 与 Wear 的简体中文资源，并补全相关繁体中文资源；账户、每日简报、官方云、Dashboard、保活和 Wear 账户摘要不再回退到英文硬编码。

### Changed

- 客户端根据后端错误码显示本地化提示，不再直接展示英文服务端错误。
- 密码输入框使用密码遮罩，登录按钮在必填项为空时不可用。
- 每日简报页面单独维护保存状态，避免复用账户页的过期提示。

### Verified

- 后端增加密码重置完整流程测试，以及无效简报时间/时区测试。
- Android 增加账户二级页面返回路径测试。
