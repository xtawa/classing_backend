# API：注册验证与 Web 设置实时同步

## 1. 注册安全流程

注册统一采用三段式流程：Cloudflare Turnstile、人机校验通过后发送 SMTP 验证码、验证码确认后激活账户并签发会话。未验证用户状态为 `PENDING_VERIFICATION`，不能登录或取得 token。

### 1.1 查询注册配置

`GET /api/v1/auth/registration/config`

```json
{
  "turnstileRequired": true,
  "turnstileSiteKey": "0x4...",
  "emailVerificationRequired": true
}
```

### 1.2 申请邮件验证码

`POST /api/v1/auth/register/email/request`

```json
{
  "username": "alice",
  "email": "alice@example.com",
  "password": "UserPass123!",
  "turnstileToken": "turnstile-response"
}
```

成功返回 `202 Accepted`：

```json
{
  "challenge": {
    "challengeId": "evc_...",
    "expiresAt": 1783699000000,
    "resendAfterSeconds": 60
  }
}
```

后端创建或刷新待验证账户，将一次性 6 位验证码以 `EMAIL_VERIFICATION` 邮件任务写入 SMTP 队列。验证码默认 10 分钟过期，仅能使用一次。

### 1.3 确认验证码

`POST /api/v1/auth/register/email/confirm`

```json
{
  "challengeId": "evc_...",
  "verificationCode": "123456"
}
```

成功后账户变为 `ACTIVE`、`email_verified = 1`，响应与登录接口一致，包含 access/refresh token 及毫秒时间戳。

### 1.4 环境变量

- `TURNSTILE_SITE_KEY`
- `TURNSTILE_SECRET`
- `EMAIL_VERIFICATION_TTL=10m`
- `EXPOSE_VERIFICATION_CODE=false`：仅限非生产联调，生产环境即使设为 true 也不会回传验证码。

未配置 `TURNSTILE_SECRET` 时后端允许跳过 Turnstile，适用于本地和自动化测试；生产部署应同时配置 site key 与 secret。

## 2. Web 与客户端设置同步

Web 端直接读写官方云 `classing_cloud_sync_v2` 文档的 `mobile.settings` Domain，因此与 Android 使用同一套 Lamport 版本、ETag/`If-Match` 并发控制和冲突合并语义。

当前 Web 可编辑：

- `showWeekend`
- `weekNumberMode`
- `semesterWeekStartDate`
- `reminderEnabled`
- `reminderMinutes`
- `keepAliveLevel`
- `dailyBriefingEnabled`
- `dailyBriefingTime`

`devModeEnabled` 是设备本地开关，不进入云同步。无障碍保活字段已废弃，Web 不得再创建该记录。

### 2.1 文档接口

- `GET /api/v1/cloud/official/document`
- `PUT /api/v1/cloud/official/document`
  - 必须携带上一响应的 `ETag` 作为 `If-Match`。
  - 冲突返回 `412 OFFICIAL_CLOUD_VERSION_CONFLICT`，调用方需重新拉取、合并、重试。

### 2.2 实时事件流

`GET /api/v1/cloud/official/events`

- 认证：`Authorization: Bearer <accessToken>`。
- 响应：`text/event-stream`。
- 客户端可在 `Last-Event-ID` 传入已知文档版本。
- 事件名：`settings`；数据包含 `version` 与 `updatedAt`。
- 浏览器管理台使用带 Authorization 的 Fetch 流读取事件，收到事件后重新拉取官方云文档。

## 3. 公告与版本检测限流

以下公开接口按“来源 IP + 路径”各自限制为每分钟 3 次：

- `GET /api/v1/client/announcements`
- `GET /api/v1/client/releases/latest`

超限返回 `429 CLIENT_RATE_LIMITED` 和 `Retry-After: 60`。Android 客户端同时执行本地 3 次/分钟保护，减少误触和重复请求。

## 4. 管理台能力

- 用户目录可调用 `POST /api/v1/admin/membership/revoke` 吊销会员。
- SMTP 邮箱支持创建、编辑和删除：
  - `POST /api/v1/admin/mailboxes`
  - `PUT /api/v1/admin/mailboxes/{id}`
  - `DELETE /api/v1/admin/mailboxes/{id}`
- SMTP 密码仍只保存 `env:VARIABLE_NAME` 引用，管理台和 API 不接收明文密码。

