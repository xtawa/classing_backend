# DB 迁移测试与事务一致性修复计划

## Context（背景）

当前 classing_backend 的数据库层存在三类已确认缺口：

1. **PostgreSQL 迁移/并发测试缺失** — `migrations.go` 中 `tableExists`、`columnExists`、`ensureBriefingJobsPayload`、`ensureFailedAttemptsColumns` 均有 pgx + sqlite 双分支，但 pgx 分支零测试覆盖。唯一的迁移测试 `TestMigrateRepairsLegacyVersionRegistry` 仅跑 SQLite。无 DB 级并发测试（现有 `refresh_replay_test.go` 只测内存缓存 single-flight）。
2. **跨资源事务一致性缺失** — `SetMembership`（membership.go L160-175）的 membership 更新 + event 插入是两个独立 `ExecContext`，event 错误被 `_, _ =` 吞掉，存在 TOCTOU 竞态。`DeleteRelease`（releases.go L141-154）的 DB 删除与 handler 层 audit 写入不在同一事务，FS 文件删除错误被 `_ =` 吞掉。`s.audit()`（handlers_auth.go L317-325）在所有业务操作后以 best-effort 方式独立调用，与业务写入不原子。
3. **无自动 schema 回滚** — 迁移为 append-only 前向执行，无 `migrate status` 命令查看已应用版本，无文档说明回滚流程。

目标：补齐 PostgreSQL 迁移与并发回归测试；将会员调整、发布删除的跨资源写入包裹在事务内；新增 `migrate status` CLI 与迁移管理文档。

## Work Area 1 — PostgreSQL 测试 + 并发测试

### 1.1 测试 harness（新文件 `internal/store/testdb_test.go`）

```
func testDialects(t *testing.T) []testDialect
```

- 始终返回 `{name: "sqlite", open: newSQLiteTestStore}`
- 当 `os.Getenv("TEST_POSTGRES_DSN") != ""` 时追加 `{name: "pgx", open: newPostgresTestStore}`
- `newSQLiteTestStore(t)` — 用 `t.TempDir()` + `file:...?_pragma=foreign_keys(1)` 打开，`SetMaxOpenConns(1)`，调用 `Migrate`，`t.Cleanup` 关闭
- `newPostgresTestStore(t)` — 从 `TEST_POSTGRES_DSN` 打开 pgx 连接，`DROP SCHEMA IF EXISTS public CASCADE` + `CREATE SCHEMA public` 获得干净状态，调用 `Migrate`，`t.Cleanup` 关闭。不复用连接池的 search_path（直接重建 public schema 最可靠）
- 不使用 `t.Parallel()`（public schema 重建不兼容并行），在文件顶部注释说明

### 1.2 迁移测试（扩展 `internal/store/migrations_test.go`）

将现有 `TestMigrateRepairsLegacyVersionRegistry` 改造为表驱动双方言测试，新增：

| 测试名 | 验证点 |
|--------|--------|
| `TestMigrateEmptyDB` | 空库 Migrate 后所有表存在（users, refresh_tokens, memberships, audit_logs, app_releases 等），`schema_migrations` 行数 == `len(migrations)` |
| `TestMigrateIdempotent` | 连续调用 `Migrate()` 两次，第二次无错误、无重复记录 |
| `TestMigrateRepairsLegacyVersionRegistry` | 已有逻辑，改为跑 sqlite + pgx 两个子测试 |

每个测试用 `for _, d := range testDialects(t) { t.Run(d.name, ...) }` 模式自动覆盖双方言。

### 1.3 并发测试（新文件 `internal/store/concurrency_test.go`）

| 测试名 | 验证点 | 并发模型 |
|--------|--------|----------|
| `TestRedeemConcurrent` | N=20 个不同用户并发兑换同一 CAMPAIGN 码（max_redemptions=5），恰好 5 个成功，DB `current_redemptions`=5 | `sync.WaitGroup` + `chan struct{}` 同时触发 |
| `TestPutCloudDocumentConcurrent` | N=10 goroutine 以相同 `expectedVersion=1` 并发写入，恰好 1 个成功，最终 version=2 | 同上 |
| `TestRotateRefreshTokenConcurrent` | N=2 goroutine 用同一 oldHash 并发 rotate，恰好 1 个成功，另一返回 ErrForbidden | 同上 |

- 所有并发测试双方言运行（sqlite 串行化验证逻辑正确性，pgx 验证真实隔离级别下的条件更新）
- 使用 `go test -race` 验证无数据竞争

## Work Area 2 — 事务一致性修复

### 2.1 `SetMembership` 事务化（`internal/store/membership.go` L160-175）

当前：读 old → 独立 `ExecContext` 更新 membership → 独立 `ExecContext` 插入 event（错误吞掉）。

改为：
```
BeginTxx → SELECT old memberships FOR UPDATE (pgx) / SELECT old (sqlite) 
→ INSERT...ON CONFLICT memberships 
→ INSERT membership_events 
→ Commit
```
- event 插入错误不再吞掉，回滚整个事务
- 在事务内读 old，消除 TOCTOU
- 调用方 `handlers_admin.go` L119/L135 签名不变，无需改动

### 2.2 `DeleteRelease` 事务 + audit 合并（`internal/store/releases.go` L141-154）

新增 `AuditContext` 类型（放在 `internal/store/admin.go`）：
```go
type AuditContext struct {
    ActorID   string
    Action    string
    TargetType string
    TargetID  string
    RequestID string
    IPAddress string
    UserAgent string
    Metadata  map[string]any
}
```

`DeleteRelease` 签名改为：
```go
func (s *Store) DeleteRelease(ctx context.Context, id string, audit AuditContext) (model.AppRelease, error)
```

实现：
```
BeginTxx → SELECT item → DELETE WHERE id = ? → INSERT audit_logs → Commit
```
- audit_logs 与 DB 删除在同一事务提交，保证原子性
- 新增 `(s *Store) auditInTx(ctx, tx, audit AuditContext)` 私有方法

### 2.3 Handler 层适配（`internal/httpapi/handlers_releases.go` L254-263）

`adminDeleteRelease` 改为：
1. 构造 `store.AuditContext`（从 `r` 提取 requestID/IP/UA，actorID 从 `principal(r)`）
2. 调用 `s.store.DeleteRelease(ctx, id, auditCtx)` — audit 已在事务内完成
3. **事务提交后** best-effort 删除 FS 文件，失败时 `s.log.Warn` 记录（孤儿文件可运维清理，数据完整性已由 DB 事务保证）
4. 不再调用 `s.audit(...)`（已移入事务）

新增 handler 辅助方法：
```go
func (s *Server) auditContext(r *http.Request, actorID, action, targetType, targetID string, metadata map[string]any) store.AuditContext
```

### 2.4 不改动的部分

- `Redeem`（membership.go L105-158）和 `RotateRefreshToken`（users.go L313-346）已在事务内使用条件更新，不改动
- 其他 `s.audit()` 调用保持 best-effort 不变（用户选择 domain-level tx 范围）
- `PutCloudDocument`（cloud.go L32-59）使用单语句条件 UPDATE，无需事务包裹

## Work Area 3 — Schema 回滚（status CLI + 文档）

### 3.1 `MigrationStatus` 方法（`internal/store/migrations.go`）

```go
type MigrationStatus struct {
    Applied   int
    Available int
    Pending   int
}

func (s *Store) MigrationStatus(ctx context.Context) (MigrationStatus, error)
```
- 查询 `SELECT COUNT(*) FROM schema_migrations` 得 Applied
- `len(migrations)` 得 Available
- Pending = Available - Applied

### 3.2 `migrate-status` 子命令（`cmd/server/main.go`）

在 `main()` 开头插入子命令拦截：
```go
if len(os.Args) > 1 && os.Args[1] == "migrate-status" {
    runMigrateStatus()
    return
}
```
- `runMigrateStatus()` 加载 config，用 `sqlx.Open` 直接打开 DB（**不走 `store.Open` 避免自动 Migrate**），构造 `Store{db, dialect}`，调用 `MigrationStatus`
- 输出示例：
  ```
  Applied migrations: 36
  Latest available:   36
  Pending:            0
  ```
- 有 pending 时退出码 1（可用于部署前检查）

### 3.3 幂等性审计

审查 `migrations` 切片中所有 `ALTER TABLE ... ADD COLUMN` 语句（L36 `users.auth_epoch`、L151 `briefing_jobs.payload`），确认已被 `ensureBriefingJobsPayload` / `ensureFailedAttemptsColumns` 幂等保护。在 `migrations.go` 顶部注释中补充规范：**未来新增 ALTER TABLE ADD COLUMN 迁移必须通过 `ensure*Column` 模式实现幂等**（SQLite 不支持 `ADD COLUMN IF NOT EXISTS`，pgx 支持但 dialect 不统一）。

### 3.4 文档（`docs/migration-management.md`）

内容包括：
- 如何新增迁移（append 到 `migrations` 切片，遵循幂等规范）
- `./classing-backend migrate-status` 用途与输出含义
- 回滚流程：通过 `pg_dump`/`sqlite3 .backup` 在迁移前创建快照，需要回滚时恢复快照（无 down-migration，前向 only）
- 部署前检查：CI/部署脚本中先跑 `migrate-status` 确认 pending=0

## Work Area 4 — Makefile

更新 `Makefile`：
```makefile
test:
	go test ./...

test-race:
	go test -race ./...

test-pg:
	TEST_POSTGRES_DSN=$(TEST_POSTGRES_DSN) go test ./...
```

## 文件变更清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `internal/store/testdb_test.go` | 新增 | 双方言测试 harness |
| `internal/store/migrations_test.go` | 修改 | 表驱动双方言 + 新增空库/幂等测试 |
| `internal/store/concurrency_test.go` | 新增 | 并发兑换/云写入/refresh rotate 测试 |
| `internal/store/membership.go` | 修改 | `SetMembership` 事务化 |
| `internal/store/releases.go` | 修改 | `DeleteRelease` 接受 AuditContext，事务内删+audit |
| `internal/store/admin.go` | 修改 | 新增 `AuditContext` 类型 + `auditInTx` 方法 |
| `internal/store/migrations.go` | 修改 | 新增 `MigrationStatus` 方法 + 幂等规范注释 |
| `internal/httpapi/handlers_releases.go` | 修改 | `adminDeleteRelease` 适配新签名 + best-effort FS 清理 |
| `internal/httpapi/handlers_auth.go` | 修改 | 新增 `auditContext()` 辅助方法 |
| `cmd/server/main.go` | 修改 | 新增 `migrate-status` 子命令 |
| `docs/migration-management.md` | 新增 | 迁移管理文档 |
| `Makefile` | 修改 | 新增 test-race / test-pg target |

## 验证步骤

1. **SQLite 全量回归**：`go test ./...` — 确认现有测试 + 新增 sqlite 子测试全绿
2. **Race 检测**：`go test -race ./internal/store/...` — 确认并发测试无数据竞争
3. **PostgreSQL 测试**（需本地 Postgres）：
   ```
   TEST_POSTGRES_DSN="postgres://user:pass@localhost:5432/testdb?sslmode=disable" go test ./internal/store/... -v
   ```
   确认 pgx 子测试全绿，迁移双分支覆盖
4. **事务一致性验证**：
   - `TestSetMembershipAtomicity`（新增）：mock event 插入失败，确认 membership 更新回滚
   - `TestDeleteReleaseAuditAtomic`（新增）：mock audit 插入失败，确认 DB 删除回滚
5. **CLI 验证**：`go run ./cmd/server migrate-status` 输出正确的 applied/available/pending
6. **`go vet ./...`** 无警告
