# API：官方云同步与同步项目

## 1. 基本规则
- Provider 名称：`OFFICIAL`
- 固定基址：`https://api-classing.underflo.ink`
- 路径前缀：`/api/v1/cloud/official`
- 客户端不可修改域名。
- 登录后默认可用于设置同步；会员才可同步课表。
- 非会员读取官方云文档时只返回 `mobile.settings`、`wear.settings`、`cloud.config`、`app.commands` 等设置/命令域；服务端会保留既有课表域但不会向非会员下发。
- 非会员写入官方云文档时，服务端只合并设置/命令域，忽略 `timetable.lessons` 与 `timetable.exceptions`，因此无法通过官方云同步课表。

## 2. 客户端配置模型
```json
{
  "cloudProvider": "OFFICIAL",
  "cloudSyncEnabled": true,
  "officialSyncFrequency": "EVERY_30_MIN",
  "syncScopes": ["TIMETABLE", "MOBILE_SETTINGS", "WEAR_SETTINGS"]
}
```

## 3. 同步项目定义
- `TIMETABLE`
  - 课程表
  - 调课/补课
  - 例外与一次性事件
- `MOBILE_SETTINGS`
  - 周视图设置
  - 提醒设置
  - Dashboard 设置
  - 每日简报配置
- `WEAR_SETTINGS`
  - 手表展示偏好

不进入云同步：
- `accessToken`
- `refreshToken`
- 会员缓存
- WebDAV 密码
- Drive Token

## 4. 接口建议

### `GET /api/v1/cloud/official/document`
- 拉取当前云文档。
- 请求头：
  - `Authorization: Bearer <accessToken>`

### `PUT /api/v1/cloud/official/document`
- 上传合并后的云文档。
- 请求头：
  - `Authorization: Bearer <accessToken>`
  - `If-Match: <etag-or-version>`
  - `Idempotency-Key: <uuid>`

### `POST /api/v1/cloud/official/test`
- 测试账号是否具备官方云连接能力。
- 登录用户均返回成功；响应中的 `canSyncSettings=true` 表示设置同步可用，`canSyncTimetable` 仅在会员有效时为 `true`。

### `GET /api/v1/cloud/official/config`
- 可选接口，返回服务端下发的限制、限流策略、最大文档大小等。

### `GET /api/v1/cloud/official/events`
- 带 Bearer token 的 SSE 事件流，响应类型为 `text/event-stream`。
- `Last-Event-ID` 使用已知的非负整数云文档版本，与 `/document` 的 `ETag` 版本一致；它不是运行时事件表 ID。
- 事件名固定为 `cloud-document`，事件 ID 为最新云文档版本。
- `data` 仅包含 `{"version":7,"updatedAt":...}`，不包含设置或课表正文。
- 无游标时服务端发送当前版本；客户端游标落后时只通知最新版本，无需逐条重放中间版本。
- 服务端每 20 秒发送 keep-alive；客户端收到更高版本后通过 `GET /document` 拉取并执行 V2 合并。
- 该事件流登录即可使用，用于 Web 与客户端设置实时同步；客户端即便选择 Google Drive 或 WebDAV 作为课表云同步方式，也会继续使用官方云同步设置。

## 5. 幂等与并发
- 每次写入必须带 `Idempotency-Key`。
- 服务端保存最近一段时间的 key，避免客户端重试重复提交。
- 并发冲突使用：
  - `409 Conflict`
  - 或 `412 Precondition Failed`

## 6. 权限与会员规则
- `OFFICIAL_CLOUD_MEMBERSHIP_REQUIRED` 仅适用于必须同步课表的操作。
- 设置同步不要求会员，只要求有效登录态。
- 非会员提交课表域不会生效，服务端只合并允许的设置域。

## 7. Scope 合并规则
- 客户端本地 `syncScopes` 决定参与合并的 Domain。
- 关闭的 Scope：
  - 不向远端推送
  - 不用远端覆盖本地
  - 不触发删除传播

## 8. 自动同步频率
- `MANUAL_ONLY`
- `EVERY_15_MIN`
- `EVERY_30_MIN`
- `EVERY_1_HOUR`
- `EVERY_3_HOURS`

说明：
- Android 最小周期按 15 分钟对齐。
- 频率变更后客户端需重建 WorkManager 周期任务。

## 9. 限流

`PUT /api/v1/cloud/official/document` 受双维度限流保护：

| 维度 | 限制 | 错误码 |
|---|---|---|
| IP（敏感接口共享） | 60 次/分钟 | `429 IP_RATE_LIMITED` |
| 账户 | 30 次/分钟 | `429 ACCOUNT_RATE_LIMITED` |

429 响应携带 `Retry-After: 60`。正常同步频率（≤ `EVERY_15_MIN`）远低于账户限制；若因并发冲突（409/412）触发自动重试，重试次数应 ≤ 3 并带退避。

## 10. v2 文档与条件请求（2026-07-13）

- 顶层字段仅允许 `format`、`updatedAt`、`records`、`changes`、`devices`；`format` 必须是 `classing_cloud_sync_v2`，最大嵌套深度 32。
- `GET /document` 返回带引号的版本 ETag；`If-None-Match` 命中时返回 304。
- `PUT /document` 必须发送 `If-Match: "<version>"`，首次创建发送 `"0"`；缺失返回 428，非法返回 400，版本冲突返回 412。
- 同一 `Idempotency-Key` 与相同 payload 返回首次响应；同 key 不同 payload 返回 409。文档、幂等响应和审计在同一数据库事务提交。
- `/events` 的 SSE `id` 是官方云文档整数版本；客户端保存最后已应用版本并在重连时作为 `Last-Event-ID` 发送。服务端只发送 `cloud-document` 通知和 keep-alive，不在事件流中传输设置正文。正常 EOF 或 401 后客户端应刷新/重新鉴权并携带最近文档版本重连。
