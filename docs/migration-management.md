# 数据库迁移管理

## 迁移机制

项目使用内嵌迁移系统（`internal/store/migrations.go`），迁移 SQL 以 append-only 字符串切片形式维护，按 1-based 序号记录在 `schema_migrations` 表中。

- 启动时 `store.Open()` 自动执行 `Migrate()`，在单个事务内运行所有未应用的迁移
- 支持 SQLite 和 PostgreSQL 两种方言，方言感知逻辑见 `tableExists` / `columnExists` / `ensure*Column` 等辅助函数
- 迁移为前向执行（forward-only），不提供 down-migration

## 新增迁移

1. 在 `migrations` 切片末尾追加 SQL 字符串（不要插入或重排已有条目）
2. 优先使用幂等写法：`CREATE TABLE IF NOT EXISTS`、`CREATE INDEX IF NOT EXISTS`
3. **ALTER TABLE ADD COLUMN** 不能直接加入切片（SQLite 不支持 `ADD COLUMN IF NOT EXISTS`）。必须仿照 `ensureFailedAttemptsColumns` 编写 `ensure*Column` 辅助函数，在 `Migrate()` 末尾调用，按方言检查列是否存在后决定是否执行 ALTER
4. 新增表或列后运行 `go test ./internal/store/...` 确认迁移测试通过

## 查看迁移状态

```bash
./classing-backend migrate-status
```

输出示例：

```
Applied migrations: 36
Latest available:   36
Pending:            0
```

- 有 pending 迁移时退出码为 1，可在部署脚本中用于前置检查
- 该命令不执行迁移，仅查询当前状态

### 部署前检查

```bash
# CI 或部署脚本中
./classing-backend migrate-status || { echo "数据库未迁移到最新版本"; exit 1; }
```

## 回滚流程

项目不提供自动 schema 回滚（无 down-migration）。回滚通过快照恢复实现：

### PostgreSQL

```bash
# 迁移前创建快照
pg_dump -Fc -U <user> -d <database> > backup_before_migration.dump

# 需要回滚时恢复
pg_restore -c -U <user> -d <database> < backup_before_migration.dump
```

### SQLite

```bash
# 迁移前备份
sqlite3 <database_path> ".backup backup_before_migration.db"

# 需要回滚时恢复
cp backup_before_migration.db <database_path>
```

### 注意事项

- 回滚到旧快照会丢失快照之后的所有业务数据
- 每次生产环境迁移前必须创建快照
- 如需在回滚后重启服务，确保服务二进制版本与数据库 schema 版本兼容
