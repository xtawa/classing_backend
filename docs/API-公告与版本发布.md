# API：公告与版本发布

本组接口为 Classing Android 客户端提供公告、更新检测、APK 下载能力，并为管理台提供公告管理、安装包上传与版本发布能力。

## 1. 公共客户端接口

公共接口不要求登录，但只返回已经启用或发布的内容。

### 1.1 查询公告

```http
GET /api/v1/client/announcements?platform=ANDROID_MOBILE
```

`platform` 支持：

- `ANDROID_MOBILE`
- `ANDROID_WEAR`

响应：

```json
{
  "announcements": [
    {
      "announcementId": "ann_...",
      "title": "维护通知",
      "content": "今晚 23:00 进行短时维护。",
      "platform": "ANDROID_MOBILE",
      "priority": 10,
      "active": true,
      "publishAt": 1783600000000,
      "expiresAt": 0,
      "createdAt": 1783600000000,
      "updatedAt": 1783600000000
    }
  ]
}
```

返回条件：`active = true`、已到 `publishAt`、尚未到 `expiresAt`。`platform` 为空的公告对全部客户端可见。结果按优先级、发布时间降序排列。

### 1.2 检测最新版本

```http
GET /api/v1/client/releases/latest?platform=ANDROID_MOBILE&channel=STABLE&versionCode=104
```

响应：

```json
{
  "updateAvailable": true,
  "release": {
    "releaseId": "rel_...",
    "platform": "ANDROID_MOBILE",
    "channel": "STABLE",
    "versionCode": 105,
    "versionName": "1.0.5",
    "minSupportedVersionCode": 100,
    "title": "Classing 1.0.5",
    "changelog": "修复同步与更新流程。",
    "mandatory": false,
    "status": "PUBLISHED",
    "artifactFileName": "classing-1.0.5.apk",
    "artifactSize": 23856169,
    "sha256": "...",
    "artifactMimeType": "application/vnd.android.package-archive",
    "downloadUrl": "/api/v1/client/releases/rel_.../download",
    "publishedAt": 1783600000000
  }
}
```

客户端必须使用整数 `versionCode` 比较版本，不得使用字符串形式的 `versionName` 比较大小。

### 1.3 下载安装包

```http
GET /api/v1/client/releases/{releaseId}/download
```

特性：

- 仅允许下载 `PUBLISHED` 版本。
- 返回准确的 `Content-Length`、APK MIME、`ETag`（SHA-256）。
- 支持标准 HTTP `Range`，便于下载器恢复或分段读取。
- 客户端下载完成后必须同时验证 `artifactSize` 与 `sha256`。

## 2. 管理员接口

所有管理接口要求管理员 Bearer token。

### 2.1 公告管理

- `GET /api/v1/admin/announcements?limit=100`
- `POST /api/v1/admin/announcements`
- `PATCH /api/v1/admin/announcements/{id}`
- `DELETE /api/v1/admin/announcements/{id}`

创建或更新请求：

```json
{
  "title": "维护通知",
  "content": "今晚 23:00 进行短时维护。",
  "platform": "ANDROID_MOBILE",
  "priority": 10,
  "active": true,
  "publishAt": 1783600000000,
  "expiresAt": 0
}
```

`publishAt = 0` 表示立即发布，`expiresAt = 0` 表示不过期。

### 2.2 上传 APK

```http
POST /api/v1/admin/releases
Content-Type: multipart/form-data
```

表单字段：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `platform` | 是 | `ANDROID_MOBILE` 或 `ANDROID_WEAR` |
| `channel` | 是 | 默认 `STABLE`，也可使用 `BETA` |
| `versionCode` | 是 | 正整数，同平台同渠道唯一 |
| `versionName` | 是 | 展示版本，例如 `1.0.5` |
| `minSupportedVersionCode` | 否 | 最低支持版本代码 |
| `title` | 是 | 更新标题 |
| `changelog` | 否 | 更新说明 |
| `mandatory` | 否 | 是否标记为必须更新 |
| `publish` | 否 | `true` 表示上传后立即发布，否则为草稿 |
| `artifact` | 是 | `.apk` 文件 |

服务端会：

1. 限制请求总大小；
2. 要求 `.apk` 扩展名；
3. 检查 ZIP 中存在 `AndroidManifest.xml`；
4. 流式计算 SHA-256；
5. 先写临时文件，再原子改名；
6. 数据库写入失败时删除已经保存的文件。

### 2.3 发布、查询和删除

- `GET /api/v1/admin/releases?limit=100`
- `POST /api/v1/admin/releases/{id}/publish`
- `DELETE /api/v1/admin/releases/{id}`

删除版本会同时删除数据库记录和服务端 APK 文件。发布、上传、删除和公告变更都会进入审计日志。

## 3. 存储与限制

环境变量：

```dotenv
RELEASE_STORAGE_DIR=/data/releases
MAX_RELEASE_ARTIFACT_BYTES=262144000
```

生产环境必须把 `RELEASE_STORAGE_DIR` 放在持久卷中，并把 APK 文件与数据库一起备份。Compose 默认使用 `classing-releases` 卷。

## 4. 错误码

- `ANNOUNCEMENT_INVALID`
- `RELEASE_NOT_FOUND`
- `RELEASE_STORAGE_DISABLED`
- `RELEASE_UPLOAD_INVALID`
- `RELEASE_ARTIFACT_REQUIRED`
- `RELEASE_ARTIFACT_INVALID`
- `RELEASE_VERSION_INVALID`
- `RELEASE_CONFLICT`
- `RELEASE_STORAGE_FAILED`
