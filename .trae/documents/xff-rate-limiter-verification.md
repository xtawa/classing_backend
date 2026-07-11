# XFF 伪造与限流硬化 — 验证计划

## 背景

本会话承接前一轮对话的上下文恢复。针对 `classing_backend` 仓库的 XFF 伪造绕过限流漏洞，前一轮已完成全部 7 个实现任务（代码已写入磁盘）。本计划仅覆盖**最后一步：运行测试与 `go vet` 验证无回归**。

## 当前状态确认（Phase 1 探索结果）

通过读取全部 8 个已修改文件，确认以下变更均已落盘：

| # | 文件 | 变更 | 状态 |
|---|------|------|------|
| 1 | `internal/config/config.go` | `TrustedProxies []*net.IPNet` 字段 + `parseTrustedProxies()`（默认 loopback） | ✅ 已落盘 |
| 2 | `internal/httpapi/helpers.go` | `clientIP` 改为 Server 方法；RemoteAddr 不在可信 CIDR 时忽略 XFF；可信时从右向左剥离可信代理 | ✅ 已落盘 |
| 3 | `internal/httpapi/middleware.go` | 限流器加 `maxEntries`（默认 8192）+ `ensureCapacity`/`sweepExpired`/`evictOldest`（丢弃最旧 25%）；新增 `isLimited`/`recordFailure`/`reset`/`size` | ✅ 已落盘 |
| 4 | `internal/httpapi/handlers_auth.go` + `server.go` | `loginFailLimiter`（5 次/15 分钟，按 identifier）；登录失败计数、成功重置 | ✅ 已落盘 |
| 5 | `internal/store/users.go` + `migrations.go` | 三张验证表加 `failed_attempts` 列（幂等 `ensureFailedAttemptsColumns`）；验证码/邮箱变更/重置令牌达到 10 次失败后锁定 | ✅ 已落盘 |
| 6 | `docs/部署教程-Linux.md` | Nginx `X-Forwarded-For $remote_addr` 覆盖客户端 XFF + 注释说明多级代理场景 | ✅ 已落盘 |
| 7 | `internal/httpapi/server_test.go` | 6 个新测试：`TestClientIPTrustedProxy`、`TestMultiLevelProxyChain`、`TestForgedXFFRateLimitBypass`、`TestLoginIdentifierRateLimit`、`TestVerificationCodeBruteForceCap`、`TestRateLimiterBounded` | ✅ 已落盘 |

## 待执行步骤（Task 8 — 验证）

唯一剩余工作是运行测试与静态检查，确认无编译错误、无回归。

### 步骤 1：运行新增的安全测试（详细模式）

```bash
cd f:/data/classing_backend
go test ./internal/httpapi/... -run 'TestClientIP|TestForgedXFF|TestLoginIdentifier|TestVerificationCodeBruteForce|TestRateLimiterBounded|TestMultiLevelProxy' -v
```

**预期**：6 个测试（含子测试共 ~10 个用例）全部 PASS。

### 步骤 2：运行全量测试套件（回归检查）

```bash
go test ./...
```

**预期**：所有包测试通过，包括既有测试：
- `TestAnnouncementsAndReleaseUploadDownload`
- `TestAccountMembershipAndSessionRevocation`
- `TestAccountEmailChangeSecurity`
- `TestMigrateRepairsLegacyVersionRegistry`（store 包，验证迁移幂等性）
- 其他 store/auth/config 包测试

### 步骤 3：静态检查

```bash
go vet ./...
```

**预期**：无告警。

## 失败处理预案

若任一步骤失败：
1. **编译错误**：检查对应文件的 import 与符号引用（前一轮已修复 `net`/`sort`/`fmt` import）。
2. **迁移测试失败**：`TestMigrateRepairsLegacyVersionRegistry` 已在前一轮验证通过；若再次失败，检查 `ensureFailedAttemptsColumns` 是否被正确调用。
3. **新测试失败**：根据失败输出定位——若是 `clientIP` 逻辑问题查 `helpers.go`；若是限流器边界问题查 `middleware.go`；若是登录限流问题查 `handlers_auth.go`。

## 验收标准对应

- ✅ 仅当 RemoteAddr 属于可信代理 CIDR 时解析代理头 — Task 2（已实现）
- ✅ Nginx 覆盖客户端 XFF / 应用从右向左剥离 — Task 2 + Task 6（已实现）
- ✅ 登录按 IP + identifier 双维度限流 — Task 4（已实现）
- ✅ 验证码按 challenge 计数并限制失败次数 — Task 5（已实现）
- ✅ 限流器有界缓存 + TTL/LRU — Task 3（已实现）
- ⏳ 伪造 XFF / 多级代理 / 直连源站 / 高基数 key 压测 — Task 7 测试已编写，待本计划步骤 1 运行验证
