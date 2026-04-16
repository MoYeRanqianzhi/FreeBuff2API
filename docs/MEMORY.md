# FreeBuff2API 持久化记忆

## 项目概况
- [架构说明](memory/architecture.md) - 项目架构与核心设计
- [API 协议研究](memory/api-protocol.md) - FreeBuff 后端 API 完整协议
- [流式传输与模型配置](memory/streaming-models.md) - SSE 流式协议 + 模型 ID 映射
- [v0.1.0 测试报告](memory/test-report-v0.1.0.md) - 真实请求性能数据

## 关键决策
- 2026-04-16: 选择 OpenAI 格式直通反代（非 Anthropic 转换），因 FreeBuff 后端本身就是 OpenAI Chat Completions 兼容
- 2026-04-16: 使用纯标准库 `net/http`，零外部依赖
- 2026-04-17: **架构性修正** —— `codebuff_metadata.run_id` 必须是服务端已注册的 run，不能自生 UUID。已实现 `startAgentRun()` 每次请求前调用 `/api/v1/agent-runs` START
- 2026-04-17: 后端 URL 修正为 `https://www.codebuff.com`（apex 会 307 重定向）
- 2026-04-17: `COST_MODE` 默认改为 `normal`，避免中国区 FREE 模式被拦截（需要 BYOK 或 credits）

## 实测性能（v0.1.0）
- TTFT: 2.2 ~ 3.2s（含 runId 注册 ~700ms）
- 吞吐: 39 ~ 55 tok/s
- Proxy 自身开销 < 2ms, 内存 ~12MB
