# API：管理台与扩展接口

所有需要登录的接口使用：

```http
Authorization: Bearer <accessToken>
```

## 账户设置

- `GET /api/v1/account/me`
- `PATCH /api/v1/account/me`
  - `username`
  - `email`
- `PUT /api/v1/account/password`
  - `currentPassword`
  - `newPassword`

密码修改成功后，当前 access token 和全部 refresh token 均失效。

## 课表项目

- `GET /api/v1/timetables`
- `POST /api/v1/timetables`
- `GET /api/v1/timetables/{id}`
- `PUT /api/v1/timetables/{id}`
- `DELETE /api/v1/timetables/{id}`

普通用户只能访问自己的项目。管理员的列表接口会返回全部项目。

## 管理员

- `GET /api/v1/admin/dashboard`
- `GET /api/v1/admin/users`
- `PATCH /api/v1/admin/users/{id}`
- `DELETE /api/v1/admin/users/{id}`：脱敏删除用户、撤销全部会话并释放原邮箱/用户名；禁止管理员删除自己。
- `POST /api/v1/admin/redeem-codes/generate`
- `GET /api/v1/admin/redeem-codes/query`
- `POST /api/v1/admin/redeem-codes/revoke`
- `POST /api/v1/admin/membership/grant`
- `POST /api/v1/admin/membership/revoke`
- `GET /api/v1/admin/mailboxes`
- `POST /api/v1/admin/mailboxes`
- `PUT /api/v1/admin/mailboxes/{id}`
- `DELETE /api/v1/admin/mailboxes/{id}`
- `GET /api/v1/admin/briefing-jobs`
- `POST /api/v1/admin/briefing-jobs/{id}/retry`
- `GET /api/v1/admin/audit-logs?limit=&offset=`：分页读取审计日志，响应包含 `auditLogs` 与 `total`。
- `GET /api/v1/admin/settings`
- `PUT /api/v1/admin/settings`：`audit.retention_days` 可设置为 1–3650；未配置时不清理，保存后立即清理一次，后台任务继续自动清理。

## 健康检查

- `GET /health/live`：进程存活。
- `GET /health/ready`：数据库连接正常。

所有响应带 `X-Request-ID`。错误响应包含：

```json
{
  "code": "ERROR_CODE",
  "message": "human-readable message",
  "requestId": "req_..."
}
```
