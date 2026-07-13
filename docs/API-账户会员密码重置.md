# API：账户、认证、密码重置

## 1. 基础约束
- Base URL：`https://api-classing.underflo.ink`
- 所有响应建议统一：
  - `code`
  - `message`
  - `data`
  - `requestId`
- 所有时间统一使用 Unix epoch milliseconds。

## 2. 注册

直注册接口 `POST /api/v1/auth/register` 已停用，服务端返回 `AUTH_EMAIL_VERIFICATION_REQUIRED`。客户端必须使用邮箱验证注册流程：

- `GET /api/v1/auth/registration/config`
- `POST /api/v1/auth/register/email/request`
- `POST /api/v1/auth/register/email/confirm`

注册申请与确认请求均必须携带 `consent`：

```json
{
  "privacyPolicy": true,
  "termsOfService": true,
  "crossBorderTransfer": true,
  "acceptedAt": 1783698400000,
  "client": "web"
}
```

约束：
- `username` 全局唯一。
- `email` 全局唯一。
- 后端必须记录邮箱验证状态字段，未验证用户不能登录或取得 token。

## 3. 登录

### `POST /api/v1/auth/login`
请求体：
```json
{
  "identifier": "alice@example.com",
  "password": "plain-or-prehashed",
  "consent": {
    "privacyPolicy": true,
    "termsOfService": true,
    "crossBorderTransfer": true,
    "acceptedAt": 1783698400000,
    "client": "web"
  }
}
```

说明：
- `identifier` 可为邮箱或用户名。
- `consent` 对登录和邮箱注册流程均必需；三项协议未全部同意时返回 `400 AUTH_CONSENT_REQUIRED`。
- 协议链接由 `GET /api/v1/auth/registration/config` 的 `legalAgreementUrls` 下发，对应 `LEGAL_PRIVACY_URL`、`LEGAL_TERMS_URL`、`LEGAL_CROSS_BORDER_URL`。

## 4. 刷新 Token

### `POST /api/v1/auth/refresh`
请求体：
```json
{
  "refreshToken": "opaque-refresh-token"
}
```

说明：
- 刷新成功后后端会一次性轮换 refresh token，客户端必须保存响应中的新 token。
- 旧 refresh token 在首次成功轮换后立即失效；为兼容同一客户端的并发请求，相同 token、IP 与 User-Agent 在 5 秒内会重放完全相同的 replacement session，不会再次轮换。
- 5 秒窗口之外再次使用旧 token 返回 `401 AUTH_REFRESH_REVOKED`。客户端仍应使用 single-flight，避免并发刷新。

## 5. 登出

### `POST /api/v1/auth/logout`
请求头：
- `Authorization: Bearer <accessToken>`

请求体：
```json
{
  "refreshToken": "opaque-refresh-token"
}
```

说明：
- 后端需撤销 refresh token。
- 若使用黑名单方案，可同时短时拉黑 access token。

## 6. 当前用户资料

### `GET /api/v1/account/me`
请求头：
- `Authorization: Bearer <accessToken>`

响应体建议：
```json
{
  "account": {
    "userId": "u_123",
    "identifier": "alice@example.com",
    "username": "alice",
    "email": "alice@example.com"
  }
}
```

## 7. 密码重置申请

### `POST /api/v1/auth/password/reset/request`
请求体：
```json
{
  "email": "alice@example.com"
}
```

规则：
- 永远返回泛化成功文案，避免枚举邮箱存在性。
- 后端生成一次性 reset token，写入重置表。
- 后端同时创建 `PASSWORD_RESET` 邮件任务，通过 SMTP 邮箱池异步发送一次性 token；生产环境响应不会暴露 token。
- token 必须带：
  - `userId`
  - `email`
  - `expiresAt`
  - `usedAt`
  - `requestIp`
  - `requestUa`

## 8. 密码重置确认

### `POST /api/v1/auth/password/reset/confirm`
请求体：
```json
{
  "token": "reset-token",
  "newPassword": "new-password"
}
```

规则：
- token 只能使用一次。
- 成功后必须：
  - 更新密码哈希
  - 标记 token 已使用
  - 撤销该用户全部 refresh token
  - 记录审计日志

## 9. 错误码建议
- `AUTH_INVALID_CREDENTIALS`
- `AUTH_ACCOUNT_DISABLED`
- `AUTH_CONSENT_REQUIRED`
- `AUTH_REFRESH_EXPIRED`
- `AUTH_REFRESH_REVOKED`
- `AUTH_RESET_TOKEN_INVALID`
- `AUTH_RESET_TOKEN_EXPIRED`
- `AUTH_RESET_TOKEN_USED`
- `AUTH_EMAIL_ALREADY_EXISTS`
- `AUTH_USERNAME_ALREADY_EXISTS`
- `IP_RATE_LIMITED` — 同一 IP 对敏感接口的请求超过 60 次/分钟（HTTP 429，携带 `Retry-After: 60`）
- `ACCOUNT_RATE_LIMITED` — 同一账户密码修改超过 10 次/分钟（HTTP 429，携带 `Retry-After: 60`）

## 10. 账号注销

### `POST /api/v1/account/delete`
请求头：
- `Authorization: Bearer <accessToken>`

请求体：
```json
{
  "currentPassword": "plain-password",
  "confirm": "DELETE"
}
```

说明：
- 前端必须二次确认，确认文本为 `DELETE` 或 `注销账号`。
- 服务端校验当前密码后软删除账户，撤销该账户所有设备会话，并清理 refresh replay 缓存。
- 成功后当前端必须立即登出并清除本地凭据；其他设备会在下一次请求或刷新时收到撤销。
- 最后一个活跃管理员账户不可注销。

## 11. 可撤销会话迁移（2026-07-13）

- access token 必须包含服务端会话 ID（JWT `sid`）；缺少 `sid` 的历史 token 不再接受，升级后所有用户需要重新登录。
- refresh token 轮换绑定同一会话；旧 token 在轮换窗口外被再次使用时，服务端撤销该会话并返回 `AUTH_REFRESH_REVOKED`。
- `POST /auth/logout` 只撤销当前 `sid`。密码重置、账户禁用、角色或状态高风险变更会撤销该用户全部 access/refresh 会话。
- 客户端遇到 `AUTH_SESSION_REVOKED` 或 `AUTH_REFRESH_REVOKED` 必须原子清理本地 access/refresh token 并回到登录页。
