# 多账号轮询 + 熔断 + 热加载

**引入版本**: v0.3.0（轮询） / v0.4.0（熔断 + 热加载）
**日期**: 2026-04-17

## 功能概览

| 能力 | 说明 |
|---|---|
| 多账号来源 | 环境变量 + `auths/*.json`（任一或同时） |
| 分配策略 | 请求级 round-robin（原子计数器） |
| 熔断 | 连续 `DefaultBreakerThreshold=3` 次失败即跳过，`DefaultBreakerCooldown=12h` 后自动恢复 |
| 热加载 | 默认 15s 扫描一次 `auths/`，文件新增 / 删除 / 修改自动 reload |
| runId 归属 | 每请求绑定单一 key（startAgentRun + chat/completions 同 key） |
| 日志脱敏 | 仅打印前 6 + 后 2 字符指纹 |
| 状态端点 | `GET /status/keys` 返回各 key 健康状况 |

## Key 来源

### 1. 环境变量（兼容老部署）
```bash
FREEBUFF_API_KEY="cb_live_a,cb_live_b,cb_live_c"
```
支持 `,`、`;`、换行分隔，自动去重。

### 2. `auths/` 目录 ⭐（推荐）
每账号一个 `.json`，字段对齐 Codebuff CLI `credentials.json`（来自 `common/src/util/credentials.ts` 的 `userSchema`）：
```json
{
  "id": "...",
  "email": "...",
  "name": null,
  "authToken": "cb_live_xxx",
  "fingerprintId": "...",
  "fingerprintHash": "..."
}
```
只有 `authToken` 用于反代，其他字段保留原格式。

两种来源可共存：env 优先，再读 `auths/`，token 相同则去重。

## 熔断器

### 触发条件
任一请求上游返回以下情况时 `MarkFailure(idx)`：
- 网络层错误（DNS、连接）
- `startAgentRun` 失败
- HTTP 401/403/402/429/5xx

连续失败次数 ≥ `DefaultBreakerThreshold`(3) → 熔断 `DefaultBreakerUntil = now + 12h`。

### 恢复路径
- `Next()` 读到 `BrokenUntil` 已过期 → 原地清零 `Fails / Broken`
- 任何 2xx 响应 → `MarkSuccess(idx)` 立即清零（避免正常 key 因历史累计失败误熔断）

### 退化保障
全部 key 都处于熔断期时，`Next()` **不会返回空**，而是选 `BrokenUntil` 最早的那个作为兜底，避免服务完全瘫痪。

## 热加载实现

`AuthsWatcher`：
- 计算 `env + auths/*.json` 的签名（名字 + 大小 + mtime）
- 每 `AUTHS_WATCH_INTERVAL`（默认 15s）对比签名
- 变化时调用 `LoadKeySources` + `pool.Reload(keys, labels)`

`Reload` 保留熔断状态：
- 同 token（survivor）→ 原 `KeyEntry` 复用，`Fails/Broken/BrokenUntil` 不丢
- 新 token → 新 Entry，干净状态
- 消失的 token → drop 掉

## 配置

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `FREEBUFF_API_KEY` | *（可空）* | 多 key 分隔符：`,`/`;`/`\n` |
| `AUTHS_DIR` | `auths` | 凭据目录，容器内默认 `/app/auths` |
| `AUTHS_WATCH_INTERVAL` | `15s` | 监视间隔（支持 Go duration 或纯秒数） |

至少一种来源要提供 key，否则启动 `log.Fatal`。

## 观测

### 启动日志
```
Upstream API keys: 3 (round-robin, breaker=3 fails/12h0m0s cooldown)
  cb_liv…7x  auths/alice.json
  cb_liv…2a  auths/bob.json
  cb-xxx…k1  env
```

### 每请求日志
```
→ upstream key[1]=cb_liv…2a
upstream status 429 on key[1]=cb_liv…2a — marked failure
```

### `/status/keys`
```json
{
  "total": 3,
  "healthy": 2,
  "keys": [
    {"index":0,"fingerprint":"cb_liv…7x","label":"auths/alice.json","fails":0,"broken":false},
    {"index":1,"fingerprint":"cb_liv…2a","label":"auths/bob.json","fails":3,"broken":true,"broken_until":"2026-04-18T11:12:00+08:00"},
    {"index":2,"fingerprint":"cb-xxx…k1","label":"env","fails":0,"broken":false}
  ]
}
```

## 已知局限 & 后续工作

- [ ] 失败时不会自动在同一请求内切换到下一 key 重试（需要单独实现 retry chain）
- [ ] 无权重 / least-loaded 策略，暂 assume 所有账号等额配额
- [ ] 无 per-key metrics（req/s、latency），后续可接 Prometheus
