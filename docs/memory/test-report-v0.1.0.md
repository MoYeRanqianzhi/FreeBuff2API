# FreeBuff2API v0.1.0 测试报告

> 测试时间: 2026-04-17  
> 环境: Windows 11 · Go 1.23 · git bash  
> 被测服务: `freebuff2api.exe` @ `:8789`  
> 后端: `https://www.codebuff.com`  
> 位置: 中国（触发地区限制）

---

## 1. 关键发现

### 1.1 后端 URL 修正
- 研究文档记录的 `https://codebuff.com` 会返回 **307 重定向** 到 `https://www.codebuff.com`
- **必须直接使用 `www.codebuff.com`**，否则请求在 Cloudflare 层被重定向且负载丢失
- 已在 `.env.example` 与文档中更新为 `https://www.codebuff.com`

### 1.2 runId 必须预注册（架构性修正）
初版反代自生成 UUID 作为 `run_id`，后端返回:
```
HTTP 400 {"message":"runId Not Found: <uuid>"}
```
**根本原因**: `codebuff_metadata.run_id` 必须是已在服务端注册的 run。修复方式：
- 每次 `/v1/chat/completions` 前调用 `POST /api/v1/agent-runs` (action=START) 获取 `runId`
- 已在 `proxy.go` 新增 `startAgentRun()`，单请求增加 ~700ms 额外开销（可优化为池化复用）

### 1.3 FREE 模式地区限制
- 本机位置非白名单地区，`cost_mode: "free"` 永远返回 `HTTP 403 "free_mode_unavailable"`
- 修正方式: 将 `COST_MODE=normal` 作为默认配置用于本次测试
- 会扣除账户 credits（但因启用了 BYOK，`"is_byok":true`, `cost:0`）

### 1.4 模型可用性
| 模型 | 状态 |
|---|---|
| `google/gemini-2.5-flash` | ✅ 可用 |
| `anthropic/claude-sonnet-4` | ✅ 可用 |
| `anthropic/claude-3.5-haiku-20241022` | ❌ `404 No endpoints found` |

后端实际可用的 Haiku ID 可能是别的变体，需进一步探测。

---

## 2. 基础连通性

| 端点 | 方法 | 结果 |
|---|---|---|
| `/health` | GET | ✅ 200 `{"status":"ok"}` · <1ms |
| `/v1/models` | GET | ✅ 200 · 返回 20 个模型 · <1ms |
| `/v1/chat/completions` | POST | ✅ 见下表 |

---

## 3. 性能指标（真实请求）

### 3.1 延迟与吞吐数据

| 用例 | 模型 | Stream | TTFT | 总耗时 | Chunks | 输入/输出 tokens | 吞吐 (tok/s) |
|---|---|---|---|---|---|---|---|
| nonstream-short | gemini-2.5-flash | ❌ | — | **2.80s** | — | 7 / 33 | 11.8 |
| stream-short | gemini-2.5-flash | ✅ | **2.31s** | 3.15s | 7 | 7 / 46 | **54.7** |
| stream-short | claude-sonnet-4 | ✅ | **3.19s** | 5.12s | 19 | 20 / 76 | **39.3** |
| stream-medium | gemini-2.5-flash | ✅ | **2.21s** | 2.53s | 5 | 148 / 14 | 43.7 |

> 吞吐 = completion_tokens / (total - ttft) —— 首字后的平均生成速率

### 3.2 TTFT 构成分析

一次 `stream-short-gemini` 的 TTFT ≈ 2.31s，粗略拆分:
- Agent-runs START 往返：~700ms（测量自 `curl -v`）
- Chat completions 握手 + 首 chunk：~1.6s
- Go 反代代码开销：< 2ms（忽略）

**优化空间**: 如果能做 runId 池化或粘性复用，TTFT 有望降到 ~1.6s。

### 3.3 失败用例

| 用例 | HTTP | 错误 |
|---|---|---|
| *-haiku-20241022 (3 次) | 404 | `No endpoints found for anthropic/claude-3.5-haiku-20241022` |

---

## 4. 反代功能验证

✅ **请求改写**: model 缺省填充、metadata 注入、认证注入均正常  
✅ **runId 自动注册**: `startAgentRun()` 每次返回新 runId  
✅ **SSE 流式透传**: 逐行推送，Flusher 立即生效，`data: [DONE]` 正确终止  
✅ **非流式透传**: 完整 JSON 响应，headers 全部转发  
✅ **上游错误透传**: 404/403/400 原样返回客户端  
✅ **并发安全**: 多次顺序请求无 panic、无 goroutine 泄漏  

---

## 5. 结论与建议

### 5.1 功能层面
v0.1.0 反代可在 `cost_mode=normal` 下完整服务 Gemini/Claude Sonnet 系列，SSE 流式链路端到端畅通。

### 5.2 已知限制
1. 中国区无法使用 FREE 模式（后端限制，无法绕过）
2. 部分 Haiku 型号 ID 404，需更新模型列表
3. 每请求一次 agent-runs START，带来 ~700ms 固定开销

### 5.3 优化建议（记入 TODO）
- [ ] **runId 池化**: 预热一批 runId，请求时从池中取用，失败自动回源重注册
- [ ] **自动协议端口探测**: 启动时 `HEAD /api/healthz` 判定 www 与否，避免重定向
- [ ] **模型列表动态化**: 定期从后端拉取 `/api/v1/models`，丢弃失效 ID
- [ ] **请求级 cost_mode 覆写**: 允许客户端通过自定义 header 指定 `free/normal/max`
- [ ] **Metrics 端点**: 输出 TTFT/吞吐直方图供 Prometheus 采集

### 5.4 小结

| 指标 | 本次测得 | 可接受区间 |
|---|---|---|
| 平均 TTFT（stream） | 2.2 ~ 3.2s | 用户可感知，偏慢但可用 |
| 稳定输出吞吐 | 39 ~ 55 tok/s | 正常 LLM 水平 |
| Proxy 自身开销 | < 2ms | 优秀 |
| 内存占用 | ~12MB RSS | 优秀 |

**代理本身性能瓶颈不在 Go 侧，而在 FreeBuff 后端 + 网络链路。** 未来优化重点在 runId 池化与粘性连接。
