# Classing Backend

Classing 课程表应用的后台服务与 Material You 管理台。服务端使用 Go，生产环境推荐 PostgreSQL；内置纯 Go SQLite 模式用于单机部署和端到端验证。单个二进制同时提供 API、管理台、数据库迁移、简报调度和 SMTP 投递工作器。

## 已实现能力

- 账户注册、登录、refresh token 一次性轮换、退出与密码重置。
- 用户名/邮箱修改、当前密码校验、密码修改后撤销全部会话。
- `ADMIN` / `USER` 权限边界与管理员自锁保护。
- 课表项目 CRUD，管理员可查看与管理全部项目。
- 会员状态、唯一/活动兑换码、原子核销与管理员权益调整。
- 官方云同步、会员校验、`ETag` / `If-Match` 并发控制和可选幂等键。
- 每日简报设置、时区调度、SMTP 邮箱池、额度切换与失败重试。
- 审计日志、运行时设置、健康检查与安全响应头。
- 内嵌 Material You 管理台，支持动态色、暗色和响应式布局。

## 本地启动

```powershell
$env:APP_ENV="development"
$env:HTTP_ADDR="127.0.0.1:8080"
$env:DATABASE_DRIVER="sqlite"
$env:DATABASE_URL="file:classing.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
$env:JWT_SECRET="replace-with-at-least-32-random-bytes"
$env:BOOTSTRAP_ADMIN_EMAIL="admin@example.com"
$env:BOOTSTRAP_ADMIN_PASSWORD="replace-with-a-strong-password"
go run ./cmd/server
```

打开 `http://127.0.0.1:8080/`。移动客户端的正式基址保持为 `https://api-classing.underflo.ink`。

## 验证

```powershell
go test ./...
node --check web-v0/app.js
```

Linux、Docker Compose、Nginx、TLS、备份和升级步骤见 [docs/部署教程-Linux.md](docs/部署教程-Linux.md)。
