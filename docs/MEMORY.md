# FreeBuff2API 持久化记忆

## 项目概况
- [架构说明](memory/architecture.md) - 项目架构与核心设计
- [API 协议研究](memory/api-protocol.md) - FreeBuff 后端 API 完整协议
- [流式传输与模型配置](memory/streaming-models.md) - SSE 流式协议 + 模型 ID 映射
- [v0.1.0 测试报告](memory/test-report-v0.1.0.md) - 真实请求性能数据 (CN/normal)
- [US 节点部署报告](memory/deployment-us-v0.1.1.md) - **FREE 模式 US 节点可用性 + 性能**

## 生产部署
- 主机: `remote3` (Los Angeles, US, 38.55.179.54)
- 路径: `~/m/freebuff2api/`
- 内网端口: `28666`（绑定 127.0.0.1 + 172.17.0.1，外部阻断）
- Docker 网络: `freebuff-net`
- FREE 模式已打通，`cost=0, is_byok=true`

## 关键决策
- 2026-04-16: 选择 OpenAI 格式直通反代（非 Anthropic 转换），因 FreeBuff 后端本身就是 OpenAI Chat Completions 兼容
- 2026-04-16: 使用纯标准库 `net/http`，零外部依赖
- 2026-04-17: **架构性修正** —— `codebuff_metadata.run_id` 必须是服务端已注册的 run，不能自生 UUID。已实现 `startAgentRun()` 每次请求前调用 `/api/v1/agent-runs` START
- 2026-04-17: 后端 URL 修正为 `https://www.codebuff.com`（apex 会 307 重定向）
- 2026-04-17: `COST_MODE` 默认改为 `normal`，避免中国区 FREE 模式被拦截（需要 BYOK 或 credits）
- 2026-04-17: **模型列表补全** —— codebuff backend 对 `model` 字段透传 OpenRouter，不做白名单校验；原 `models.go` 静态列表只到 4.1 世代，缺 `claude-opus-4.6/4.5`、`sonnet-4.6/4.5`、`haiku-4.5`、`gpt-5.3`、`gemini-3.x`、qwen3、kimi-k2.5、glm-4.7 等。已按上游 `agent-definition.ts` 的 `ModelName` union + `claude-oauth.ts` 的 OAuth 映射全量补齐

## 实测性能（v0.1.0）
- TTFT: 2.2 ~ 3.2s（含 runId 注册 ~700ms）
- 吞吐: 39 ~ 55 tok/s
- Proxy 自身开销 < 2ms, 内存 ~12MB
