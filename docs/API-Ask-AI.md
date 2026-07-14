# API: Ask AI

## 使用边界

- 所有接口均要求 `Authorization: Bearer <accessToken>`。
- `POST /api/v1/ai/chat` 仅允许有效会员调用；未登录由认证中间件返回 `401`，非会员返回 `403 AI_MEMBERSHIP_REQUIRED`。
- 新建会话必须随首问提供 `timetableSnapshot`，服务端持久化快照并在后续追问中复用，客户端不得重新发送或替换该快照。
- 对话、消息和用量按用户隔离。删除会话会级联删除其中的消息与请求记录。

## 聊天流

### `POST /api/v1/ai/chat`

请求：

```json
{
  "clientRequestId": "uuid",
  "message": "我周三下午有什么课？",
  "sourceProjectId": "optional-project-id",
  "timetableSnapshot": { "lessons": [{ "title": "数学", "dayOfWeek": "WEDNESDAY" }] }
}
```

追问时传入 `conversationId`，且省略 `timetableSnapshot`：

```json
{ "conversationId": "aic_xxx", "clientRequestId": "uuid", "message": "地点在哪里？" }
```

响应为 `text/event-stream`，事件依次可能为 `conversation`、`usage`、多个 `delta`、`done` 或 `error`。`clientRequestId` 在同一用户内幂等；完成的重复请求会重放已保存回答，不重复计量。

## 用户接口

- `GET /api/v1/ai/usage/me`：返回当月 `limit`、`used`、`reserved`、`resetAt`。
- `GET /api/v1/ai/conversations?limit=&offset=`：返回当前用户的会话列表。
- `GET /api/v1/ai/conversations/{id}/messages`：返回当前用户的已完成消息。
- `DELETE /api/v1/ai/conversations/{id}`：删除当前用户会话。

## 管理员接口（仅 Web）

- `GET` / `PUT /api/v1/admin/ai/config`：配置 OpenAI-compatible 提供商、模型、提示词、超时、历史上限和默认月限额。`secretRef` 只能引用以 `AI_PROVIDER_KEY_` 开头的环境变量，响应不会返回密钥。
- `POST /api/v1/admin/ai/config/test`：使用当前配置向提供商发起无业务数据的连通性测试。
- `GET /api/v1/admin/ai/usage?limit=&offset=`：查看用户月度用量与限额模式。
- `PUT /api/v1/admin/ai/quotas`：批量设置 `INHERIT`、`LIMITED`、`UNLIMITED` 或 `BLOCKED` 用户限额。
- `PUT /api/v1/admin/ai/quotas/default`：更新继承用户的默认月限额。

生产环境使用 HTTPS 提供商地址；服务端拒绝 localhost、私网及链路本地字面量地址。AI 默认关闭，部署后需由管理员配置环境变量与提供商后才可启用。
