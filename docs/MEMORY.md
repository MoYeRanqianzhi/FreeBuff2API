# FreeBuff2API 持久化记忆

## 项目概况
- [架构说明](memory/architecture.md) - 项目架构与核心设计
- [API 协议研究](memory/api-protocol.md) - FreeBuff 后端 API 完整协议
- [流式传输与模型配置](memory/streaming-models.md) - SSE 流式协议 + 模型 ID 映射

## 关键决策
- 2026-04-16: 选择 OpenAI 格式直通反代（非 Anthropic 转换），因 FreeBuff 后端本身就是 OpenAI Chat Completions 兼容
- 2026-04-16: 使用纯标准库 `net/http`，零外部依赖，最大化性能和可维护性
