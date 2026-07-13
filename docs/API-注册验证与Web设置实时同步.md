# API：注册验证与 Web 设置实时同步

## 1. 注册安全流程

注册统一采用三段式流程：Cloudflare Turnstile、人机校验通过后发送 SMTP 验证码、验证码确认后激活账户并签发会话。未验证用户状态为 `PENDING_VERIFICATION`，不能登录或取得 token。

### 1.1 查询注册配置

`GET /api/v1/auth/registration/config`

```json
{
  "turnstileRequired": true,
  "turnstileSiteKey": "0x4...",
  "emailVerificationRequired": true,
  "legalAgreementUrls": {
    "privacyPolicy": "https://lyxyy.notion.site/classing-user-policy",
    "termsOfService": "https://lyxyy.notion.site/classing-user-policy",
    "crossBorderTransfer": "https://lyxyy.notion.site/classing-user-policy"
  }
}
```

`legalAgreementUrls` 由服务端环境变量下发，客户端必须拿到完整 URL 后再允许登录或注册下一步操作。

### 1.2 申请邮件验证码

`POST /api/v1/auth/register/email/request`

```json
{
  "username": "alice",
  "email": "alice@example.com",
  "password": "UserPass123!",
  "turnstileToken": "turnstile-response",
  "consent": {
    "privacyPolicy": true,
    "termsOfService": true,
    "crossBorderTransfer": true,
    "acceptedAt": 1783698400000,
    "client": "web"
  }
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
  "verificationCode": "123456",
  "consent": {
    "privacyPolicy": true,
    "termsOfService": true,
    "crossBorderTransfer": true,
    "acceptedAt": 1783698700000,
    "client": "web"
  }
}
```

成功后账户变为 `ACTIVE`、`email_verified = 1`，响应与登录接口一致，包含 access/refresh token 及毫秒时间戳。

### 1.4 环境变量

- `TURNSTILE_SITE_KEY`
- `TURNSTILE_SECRET`
- `EMAIL_VERIFICATION_TTL=10m`
- `EXPOSE_VERIFICATION_CODE=false`：仅限非生产联调，生产环境即使设为 true 也不会回传验证码。
- `LEGAL_PRIVACY_URL`
- `LEGAL_TERMS_URL`
- `LEGAL_CROSS_BORDER_URL`

未配置 `TURNSTILE_SECRET` 时后端允许跳过 Turnstile，适用于本地和自动化测试；生产部署应同时配置 site key 与 secret。

### 1.5 登录协议确认

`POST /api/v1/auth/login` 也必须携带同结构的 `consent`。三项布尔值未全部为 `true` 时返回 `400 AUTH_CONSENT_REQUIRED`。前端必须展示“您已阅读并同意《隐私政策》《用户协议》和《个人数据跨境传输协议》”复选框，未勾选不得提交。

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
- 客户端可在 `Last-Event-ID` 传入已知的非负整数文档版本；该值与文档 `ETag` 中的版本一致。
- 未提供游标时服务端立即发送当前版本；游标落后时只发送最新版本，无需重放中间写入。
- 事件名：`cloud-document`；事件 ID 为文档版本，数据仅包含 `version` 与 `updatedAt`，不包含设置正文。
- 服务端每 20 秒发送 keep-alive。客户端断线后携带最后收到的事件 ID 重连，收到事件后重新拉取并合并官方云文档。
- 浏览器与 Android 前台使用带 Authorization 的 Fetch/HTTP 流；Android 后台停止长连接，恢复前台时主动补拉并由周期任务兜底。

## 3. 公告与版本检测限流

以下公开接口按“来源 IP + 路径”各自限制为每分钟 3 次：

- `GET /api/v1/client/announcements`
- `GET /api/v1/client/releases/latest`

超限返回 `429 CLIENT_RATE_LIMITED` 和 `Retry-After: 60`。Android 客户端同时执行本地 3 次/分钟保护，减少误触和重复请求。

## 4. 账号注销

`POST /api/v1/account/delete`

认证：`Authorization: Bearer <accessToken>`。

```json
{
  "currentPassword": "UserPass123!",
  "confirm": "DELETE"
}
```

规则：

- 前端必须二次确认，确认文本为 `DELETE` 或 `注销账号`。
- 服务端校验当前密码后软删除账户、清空密码哈希、撤销该账户所有 access/refresh 会话，并提升 `auth_epoch`。
- 成功后客户端必须清理本地凭据并回到登录页；其他设备会在下一次请求或刷新时收到会话撤销。
- 最后一个活跃管理员账户不可注销。

## 5. 管理台能力

- 用户目录可调用 `POST /api/v1/admin/membership/revoke` 吊销会员。
- SMTP 邮箱支持创建、编辑和删除：
  - `POST /api/v1/admin/mailboxes`
  - `PUT /api/v1/admin/mailboxes/{id}`
  - `DELETE /api/v1/admin/mailboxes/{id}`
- SMTP 密码仍只保存 `env:VARIABLE_NAME` 引用，管理台和 API 不接收明文密码。
