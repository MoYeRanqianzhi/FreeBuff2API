# 多账号轮询（Multi-Account Round-Robin）

**引入版本**: v0.2.0
**日期**: 2026-04-17

## 目的
将多个 FreeBuff (codebuff) 账号的 API Key 聚合到单一反代入口，按请求粒度 round-robin 分配，达成：
- 分散单账号额度 / 速率限制
- 多账号负载均衡，吞吐线性扩展
- 某账号失效时不会完全中断服务（仅该账号承担的请求失败）

## 配置
环境变量 `FREEBUFF_API_KEY`（或别名 `FREEBUFF_API_KEYS`）支持以下分隔符：
- 逗号 `,`
- 分号 `;`
- 换行 `\n`

示例：
```bash
FREEBUFF_API_KEY="cb-key1,cb-key2,cb-key3"
```
或 docker-compose `.env`：
```env
FREEBUFF_API_KEY=cb-key1,cb-key2,cb-key3
```

单 key 配置与以前完全兼容（不分隔即视为单 key）。

## 实现

### 选择算法
`keypool.go` 中 `KeyPool` 使用 `atomic.AddUint64` 原子自增计数器 + 取模，lock-free，O(1)：
```go
idx := atomic.AddUint64(&p.counter, 1) - 1
return p.keys[int(idx % uint64(len(p.keys)))]
```
优点：无锁、可水平并发；同一账号的请求均匀分布。

### Key 绑定生命周期
**一个请求全程使用同一 key**。`startAgentRun()` 注册 runId 与后续 `/chat/completions` 必须属于同一账号（runId 在服务端按账号归属）。实现：
```go
upstreamKey, keyIdx := p.keys.Next()  // 入口选一次
runID := p.startAgentRun(ctx, upstreamKey)
// chat/completions 使用相同 upstreamKey
```

### 日志脱敏
每请求记录 `key[index]=prefix…suffix` 指纹（前 6 + 后 2 字符），便于排查哪个账号失败，不泄露完整 key。

## 去重 & 容错
- `parseKeys` 自动去重（同一 key 粘贴多次只保留一份）
- 空行 / 空白被忽略
- 若解析后 0 个 key，启动时 `return fmt.Errorf("FREEBUFF_API_KEY is required ...")`

## 负载均衡特性
| 项目 | 行为 |
|---|---|
| 并发请求分配 | 基于原子计数器严格轮询 |
| 单 key 故障 | 该 key 承担的请求返回 502，其他 key 正常服务 |
| runId 归属 | 绑定选中的 key，不跨账号 |
| 客户端透明 | 调用方无感知，仍是单一 `/v1/chat/completions` |

## 已知局限 & 后续工作
- [ ] 无自动熔断：连续失败的 key 仍会被选中（跳过需要加 health tracker）
- [ ] 无权重：所有 key 同权重；未来可引入 weighted RR / least-loaded
- [ ] 无每 key 的速率限制隔离：由上游自身限流兜底

若需熔断逻辑，建议在 `KeyPool` 上加 `MarkFailure(idx)` + 失败计数窗口，下一轮选择时跳过。
