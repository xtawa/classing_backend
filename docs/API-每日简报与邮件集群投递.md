# API：每日简报与邮件集群投递

## 1. 客户端设置模型
```json
{
  "enabled": true,
  "channel": "BOTH",
  "time": "20:00"
}
```

`channel` 枚举：
- `APP_NOTIFICATION`
- `EMAIL`
- `BOTH`

规则：
- `APP_NOTIFICATION` 可离线工作。
- `EMAIL` 与 `BOTH` 要求用户已登录。
- `time` 必须是有效的 24 小时制 `HH:mm`，`timezone` 必须是有效的 IANA 时区。

## 2. 客户端接口建议

### `PUT /api/v1/briefings/daily`
请求头：
- `Authorization: Bearer <accessToken>`

请求体：
```json
{
  "enabled": true,
  "channel": "BOTH",
  "time": "20:00",
  "timezone": "Asia/Shanghai"
}
```

### `DELETE /api/v1/briefings/daily`
- 取消邮件订阅。

### `POST /api/v1/briefings/daily/test`
- 试发或预览。
- 请求体可指定测试渠道：
```json
{
  "channel": "APP_NOTIFICATION"
}
```
- `channel=APP_NOTIFICATION` 时，后端不会创建邮件任务，而是向官方云 `app.commands` 写入 `DAILY_BRIEFING_TEST` 命令；已登录客户端通过官方设置同步实时收到命令后弹出 App 通知。
- `channel=EMAIL` 时创建 `EMAIL_TEST` 邮件任务；`channel=BOTH` 同时创建邮件任务并下发 App 测试命令。
- 响应可返回：
  - 纯文本预览
  - HTML 预览
  - 本次将使用的发件邮箱标识
  - `appNotificationQueued`

## 3. 后端任务模型
- `briefing_subscription`
  - `userId`
  - `channel`
  - `time`
  - `timezone`
  - `enabled`
  - `lastScheduledFor`
- `briefing_job`
  - `jobId`
  - `userId`
  - `targetDate`
  - `channel`
  - `status`
  - `providerMailboxId`
  - `retryCount`
  - `lastError`
- `briefing_job_logs`
  - `jobId`
  - `level`
  - `event`
  - `message`
  - `details`
  - `createdAt`

## 4. SMTP 邮箱池设计
- `mailbox`
  - `mailboxId`
  - `smtpHost`
  - `smtpPort`
  - `username`
  - `passwordSecretRef`
  - `dailyQuota`

管理台可选择 Lark 公共邮箱预设：

- SMTP Host：`smtp.larksuite.com`
- SSL 端口：`465`
- STARTTLS 端口：`587`
- 用户名与发件地址：公共邮箱完整地址
- 密码引用：默认 `env:CLASSING_SMTP_PASSWORD`；如需独立隔离可改为 `env:LARK_SMTP_PASSWORD`
  - `usedToday`
  - `enabled`
- 调度规则：
  1. 从启用邮箱中选择 `usedToday < dailyQuota` 的邮箱。
  2. 优先选当天发送量最低者。
 3. 达到上限后自动切换到下一邮箱。
 4. 所有邮箱都满额时，任务转入待重试或次日补发队列。
 5. 每次投递尝试都会写入可展开任务日志；日志只记录阶段、脱敏收件人、主机端口、TLS 模式、错误分类和 SMTP 返回，不记录 SMTP 密码。

## 5. 集群投递建议
- 投递服务无状态化。
- 配置与计数放数据库/Redis。
- 同一用户同一天同一频道只能生成一个正式任务。
- 通过分布式锁或唯一索引防止重复发送。

## 6. 邮件内容建议
- 今日总课数
- 今日剩余课时
- 下一节课
- 特殊调课/例外提醒
- Dashboard 摘要链接或打开 App 深链

## 7. 错误码建议
- `BRIEFING_LOGIN_REQUIRED`
- `BRIEFING_EMAIL_CHANNEL_DISABLED`
- `BRIEFING_INVALID_TIME`
- `BRIEFING_MAILBOX_POOL_EXHAUSTED`
- `BRIEFING_TEST_SEND_FAILED`
- `IP_RATE_LIMITED` — 同一 IP 对敏感接口的请求超过 60 次/分钟（HTTP 429，携带 `Retry-After: 60`）
- `ACCOUNT_RATE_LIMITED` — 同一账户：简报测试 5 次/分钟、简报配置变更 30 次/分钟（HTTP 429，携带 `Retry-After: 60`）
