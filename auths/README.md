# `auths/` — 多账号凭据目录

本目录用于投放多份 FreeBuff/Codebuff 账号凭据，FreeBuff2API 启动时会自动加载并做 round-robin 负载均衡 + 12 小时熔断恢复。

## 文件格式

每个账号一个 `.json` 文件，格式对齐 Codebuff CLI 的 `credentials.json`：

```json
{
  "id": "user_xxx",
  "email": "alice@example.com",
  "name": null,
  "authToken": "cb_live_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
  "fingerprintId": "fp_xxx",
  "fingerprintHash": "hash_xxx"
}
```

字段说明：
- **`authToken`** *（必需）*：真正用于反代的 Bearer token
- 其他字段：保留原始格式以便日后追踪来源，程序不使用

## 文件命名

- 必须以 `.json` 结尾
- 建议用可辨识名称（如 `alice.json`、`bob.json`、`team-us-01.json`），日志 / `/status/keys` 会以 `auths/<filename>` 作为 label
- 解析失败的文件会被跳过并打印 warning，不影响整体启动

## 热加载

默认每 `AUTHS_WATCH_INTERVAL`（默认 15 秒）扫描一次目录，检测到以下变化会自动重载：
- 新增 `.json` 文件
- 删除 `.json` 文件
- 修改已有 `.json` 文件（size 或 mtime 变化）

**已发生的熔断状态会在 reload 中保留**：同一个 `authToken` 如果在本次扫描前已被熔断，reload 后依旧处于熔断期；冷却结束会按正常流程恢复。

## 与环境变量共存

- 环境变量 `FREEBUFF_API_KEY` 与 `auths/*.json` 可以**同时**使用
- 顺序：env 先，auths/ 后（按文件名字典序）
- 相同 token 自动去重
- 至少要有一个来源提供 key，否则启动失败

## 安全建议

- 权限建议 `chmod 600 auths/*.json`
- 生产环境建议将本目录加入 `.gitignore`，避免凭据误入仓库
- docker-compose 已挂载为只读（`:ro`）

## 状态查看

运行时可 curl 以下端点查看每个 key 当前的健康状况：

```bash
curl http://127.0.0.1:28666/status/keys
```

返回示例：
```json
{
  "total": 3,
  "healthy": 2,
  "keys": [
    {"index":0,"fingerprint":"cb_liv…7x","label":"auths/alice.json","fails":0,"broken":false},
    {"index":1,"fingerprint":"cb_liv…2a","label":"auths/bob.json","fails":3,"broken":true,"broken_until":"2026-04-17T23:12:00+08:00"},
    {"index":2,"fingerprint":"cb-xxx…k1","label":"env","fails":0,"broken":false}
  ]
}
```
