# Changelog

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
