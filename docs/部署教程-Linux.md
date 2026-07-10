# Classing 后端 Linux 部署教程

本文覆盖 Linux `amd64` 与 `arm64` 部署。推荐使用 Docker Compose + PostgreSQL；也提供原生二进制、systemd 和 SQLite 单机方案。

正式客户端固定访问：

```text
https://api-classing.underflo.ink
```

## 1. 部署前规划

### 1.1 推荐资源

小规模自托管起步配置：

- 2 vCPU
- 2 GB RAM
- 20 GB SSD
- Linux amd64 或 arm64
- 一个可解析到服务器的域名
- TCP 80/443 对公网开放

PostgreSQL、Classing 服务与 Nginx 可以部署在同一台主机。用户量增长后，可优先将 PostgreSQL 和邮件投递工作器拆到独立节点。

### 1.2 DNS

为 `api-classing.underflo.ink` 配置：

- IPv4：`A` 记录指向服务器公网地址。
- IPv6：可选 `AAAA` 记录。

等待 DNS 生效后验证：

```bash
dig +short api-classing.underflo.ink
```

### 1.3 安装基础工具

Ubuntu / Debian：

```bash
sudo apt update
sudo apt install -y git curl ca-certificates nginx openssl
```

安装 Docker Engine 与 Compose Plugin 后确认：

```bash
docker --version
docker compose version
```

## 2. Docker Compose + PostgreSQL（推荐）

### 2.1 获取代码

```bash
sudo mkdir -p /opt/classing
sudo chown "$USER":"$USER" /opt/classing
git clone <your-repository-url> /opt/classing/backend
cd /opt/classing/backend
```

### 2.2 创建配置

```bash
cp .env.example .env
chmod 600 .env
```

生成独立随机密码：

```bash
openssl rand -hex 24
openssl rand -base64 48
openssl rand -hex 24
```

分别用于 PostgreSQL、JWT 和管理员。PostgreSQL 密码使用十六进制可避免 URL 转义问题。编辑 `.env`：

```dotenv
POSTGRES_DB=classing
POSTGRES_USER=classing
POSTGRES_PASSWORD=替换为数据库随机密码

PUBLIC_BASE_URL=https://api-classing.underflo.ink
JWT_SECRET=替换为至少32字节的随机值

BOOTSTRAP_ADMIN_USERNAME=admin
BOOTSTRAP_ADMIN_EMAIL=你的管理员邮箱
BOOTSTRAP_ADMIN_PASSWORD=替换为强管理员密码

CLASSING_PORT=8080
CORS_ALLOWED_ORIGINS=
MAX_CLOUD_DOCUMENT_BYTES=2097152
SCHEDULER_ENABLED=true

# 在管理台添加 SMTP 邮箱时使用 env:CLASSING_SMTP_PASSWORD。
CLASSING_SMTP_PASSWORD=替换为SMTP密码或应用专用密码
```

注意：

- `JWT_SECRET` 与数据库密码必须不同。
- 管理员只会在邮箱不存在时自动创建。后续修改 `.env` 中的管理员密码不会覆盖数据库密码，请从管理台“账户设置”修改。
- Compose 内 PostgreSQL 使用隔离网络，因此示例连接为 `sslmode=disable`。外部数据库必须使用 TLS。
- 管理台与 API 同源部署时 `CORS_ALLOWED_ORIGINS` 可留空。

### 2.3 启动

```bash
docker compose up -d --build
docker compose ps
```

查看日志：

```bash
docker compose logs -f classing
```

验证容器内服务：

```bash
curl -fsS http://127.0.0.1:8080/health/live
curl -fsS http://127.0.0.1:8080/health/ready
```

预期分别返回 `status=ok` 与 `status=ready`。

### 2.4 数据库迁移

服务启动时自动执行向前迁移，并把版本写入 `schema_migrations`。多个实例同时首次启动前，建议只启动一个实例完成迁移：

```bash
docker compose up -d postgres
docker compose up -d classing
```

当前迁移只创建表、索引和新增字段，不自动删除业务数据。

## 3. Nginx 反向代理

创建 `/etc/nginx/sites-available/classing`：

```nginx
server {
    listen 80;
    listen [::]:80;
    server_name api-classing.underflo.ink;

    client_max_body_size 260m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Request-ID $request_id;
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
        proxy_send_timeout 60s;
    }
}
```

启用并检查：

```bash
sudo ln -s /etc/nginx/sites-available/classing /etc/nginx/sites-enabled/classing
sudo nginx -t
sudo systemctl reload nginx
```

## 4. HTTPS 证书

使用 Certbot：

```bash
sudo apt install -y certbot python3-certbot-nginx
sudo certbot --nginx -d api-classing.underflo.ink
sudo certbot renew --dry-run
```

验证：

```bash
curl -fsS https://api-classing.underflo.ink/health/ready
```

管理台地址与 API 域名相同：

```text
https://api-classing.underflo.ink/
```

## 5. 防火墙

使用 UFW：

```bash
sudo ufw allow OpenSSH
sudo ufw allow 'Nginx Full'
sudo ufw enable
sudo ufw status
```

Compose 默认只把后端绑定到 `127.0.0.1`，不要把 PostgreSQL 的 5432 端口暴露到公网。

## 6. amd64 / arm64 多架构镜像

创建 buildx builder：

```bash
docker buildx create --name classing-builder --use
docker buildx inspect --bootstrap
```

构建并推送两种架构：

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t your-registry.example.com/classing/backend:latest \
  --push .
```

单架构本地加载：

```bash
docker buildx build --platform linux/amd64 -t classing-backend:local --load .
```

Dockerfile 使用 `CGO_ENABLED=0`，SQLite 驱动也是纯 Go，因此 amd64 与 arm64 不依赖宿主机 C 库。

## 7. 原生二进制部署

### 7.1 构建

amd64：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" \
  -o dist/classing-backend-linux-amd64 ./cmd/server
```

arm64：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags="-s -w" \
  -o dist/classing-backend-linux-arm64 ./cmd/server
```

### 7.2 安装用户与目录

```bash
sudo useradd --system --home /var/lib/classing --shell /usr/sbin/nologin classing
sudo install -d -o classing -g classing /var/lib/classing /etc/classing
sudo install -m 0755 dist/classing-backend-linux-amd64 /usr/local/bin/classing-backend
```

arm64 主机将文件名替换为 `classing-backend-linux-arm64`。

### 7.3 环境文件

创建 `/etc/classing/classing.env`：

```dotenv
APP_ENV=production
HTTP_ADDR=127.0.0.1:8080
PUBLIC_BASE_URL=https://api-classing.underflo.ink
DATABASE_DRIVER=pgx
DATABASE_URL=postgres://classing:数据库密码@127.0.0.1:5432/classing?sslmode=require
JWT_SECRET=至少32字节随机值
BOOTSTRAP_ADMIN_USERNAME=admin
BOOTSTRAP_ADMIN_EMAIL=管理员邮箱
BOOTSTRAP_ADMIN_PASSWORD=首次启动管理员密码
EXPOSE_RESET_TOKEN=false
SCHEDULER_ENABLED=true
```

保护文件：

```bash
sudo chown root:classing /etc/classing/classing.env
sudo chmod 640 /etc/classing/classing.env
```

### 7.4 systemd

创建 `/etc/systemd/system/classing.service`：

```ini
[Unit]
Description=Classing Backend
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=classing
Group=classing
EnvironmentFile=/etc/classing/classing.env
ExecStart=/usr/local/bin/classing-backend
WorkingDirectory=/var/lib/classing
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/classing
CapabilityBoundingSet=
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now classing
sudo systemctl status classing
sudo journalctl -u classing -f
```

## 8. SQLite 单机模式

SQLite 适合个人部署、演示和较低并发环境。使用 Compose：

```bash
docker compose -f compose.sqlite.yaml up -d --build
```

原生配置：

```dotenv
DATABASE_DRIVER=sqlite
DATABASE_URL=file:/var/lib/classing/classing.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)
```

高并发、多实例或需要数据库高可用时必须使用 PostgreSQL。

## 9. SMTP 邮箱池

1. 在 `.env` 或 systemd 环境文件加入 SMTP 密码，例如：

   ```dotenv
   CLASSING_SMTP_PASSWORD=应用专用密码
   ```

2. 管理员登录管理台，进入“邮件与任务”。
3. 添加邮箱，Secret 引用填写：

   ```text
   env:CLASSING_SMTP_PASSWORD
   ```

4. 按服务端口选择：
   - 587：使用 STARTTLS（服务器支持时自动协商）。
   - 465：使用隐式 TLS。
5. 从“每日简报”发送测试任务，在“邮件与任务”观察状态。

服务不会把 SMTP 明文密码保存进数据库。Secret 引用只能读取服务进程环境变量。

## 10. 备份与恢复

### 10.1 PostgreSQL 备份

```bash
mkdir -p /opt/classing/backups
docker compose exec -T postgres pg_dump \
  -U classing -d classing -Fc \
  > /opt/classing/backups/classing-$(date +%F-%H%M).dump
```

恢复到空数据库：

```bash
cat /opt/classing/backups/classing-YYYY-MM-DD-HHMM.dump | \
  docker compose exec -T postgres pg_restore \
    -U classing -d classing --clean --if-exists
```

生产环境建议将备份复制到另一台主机或对象存储，并定期执行恢复演练。

### 10.2 SQLite 备份

最安全方式是短暂停止写入后复制数据库：

```bash
docker compose -f compose.sqlite.yaml stop classing
docker run --rm -v classing-data:/data -v "$PWD/backups:/backup" alpine \
  cp /data/classing.db /backup/classing-$(date +%F-%H%M).db
docker compose -f compose.sqlite.yaml start classing
```

### 10.3 安装包备份

版本元数据位于数据库，APK 文件位于 `RELEASE_STORAGE_DIR`。两者必须在同一备份批次中保存。Compose 部署可导出版本卷：

```bash
docker run --rm -v classing-releases:/data -v "$PWD/backups:/backup" alpine \
  tar -C /data -czf /backup/classing-releases-$(date +%F-%H%M).tar.gz .
```

SQLite Compose 的 APK 位于 `classing-data` 卷中的 `/data/releases`，备份数据库卷时一并包含。

## 11. 升级与回滚

升级前：

```bash
git pull --ff-only
# 先执行数据库备份
docker compose build --pull classing
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:8080/health/ready
```

回滚应用镜像时使用明确版本标签，不要只依赖 `latest`：

```bash
CLASSING_IMAGE=your-registry.example.com/classing/backend:previous docker compose up -d classing
```

数据库迁移为向前迁移。若未来版本包含不可逆结构变化，回滚应用前必须同时按该版本发布说明恢复数据库备份。

## 12. 日志与健康检查

Docker：

```bash
docker compose logs --since=30m classing
docker compose ps
```

systemd：

```bash
journalctl -u classing --since "30 minutes ago"
```

探针：

- `/health/live`：进程存活。
- `/health/ready`：数据库连接可用。

服务日志为 JSON，包含 `request_id`、HTTP 方法、路径与耗时。客户端也会收到 `X-Request-ID`，可用于关联问题。

## 13. 常见故障

### 13.1 Nginx 502

```bash
curl -v http://127.0.0.1:8080/health/ready
docker compose logs classing
sudo nginx -t
```

确认 Compose 端口只绑定本机且 Nginx 指向同一个端口。

### 13.2 数据库不可用

```bash
docker compose ps postgres
docker compose logs postgres
docker compose exec postgres pg_isready -U classing -d classing
```

检查 `.env` 中的 PostgreSQL 用户、数据库与密码是否一致。

### 13.3 管理员无法登录

- Bootstrap 只创建不存在的管理员，不会覆盖已有密码。
- 使用设置页修改密码后，全部 access/refresh 会话都会立即失效。
- 若完全丢失管理员密码，应通过受控数据库维护流程恢复，不要开启 `EXPOSE_RESET_TOKEN` 到生产环境。

### 13.4 官方云同步返回 403

官方云仅会员可用。先检查：

```text
GET /api/v1/membership/status
```

管理员可在“用户与权限/会员”流程授予权益，用户也可核销兑换码。

### 13.5 云同步返回 412

客户端提交了旧版本。重新 `GET /api/v1/cloud/official/document`，读取新 `ETag`，完成本地合并后使用新的 `If-Match` 重试。

### 13.6 邮件反复重试

- Secret 引用必须为 `env:变量名`。
- 确认变量实际存在于容器或 systemd 服务环境。
- 检查 SMTP 主机、端口、应用专用密码和服务商授权策略。
- 检查邮箱每日额度是否耗尽。

## 14. 上线安全清单

- [ ] `APP_ENV=production`。
- [ ] `JWT_SECRET` 至少 32 个随机字节，且未提交到 Git。
- [ ] `.env` 或环境文件权限为 600/640。
- [ ] `EXPOSE_RESET_TOKEN=false`。
- [ ] PostgreSQL 5432 未暴露公网。
- [ ] 仅通过 HTTPS 对外提供服务。
- [ ] 管理员密码已从 bootstrap 初始值修改。
- [ ] SMTP 使用应用专用密码或独立凭据。
- [ ] PostgreSQL 与 SQLite 备份已实际恢复演练。
- [ ] `/health/ready` 已纳入监控。
- [ ] Nginx 与服务端日志均保留 request ID。
