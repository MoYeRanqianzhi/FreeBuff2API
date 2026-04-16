# FreeBuff2API 架构说明

## 核心架构

```
Client (OpenAI SDK / curl / any OpenAI-compatible client)
    |  POST /v1/chat/completions (OpenAI 格式)
    v
Go Reverse Proxy (:8080)
    |  1. 可选: 校验客户端 API_KEY
    |  2. 注入 Authorization: Bearer <FREEBUFF_API_KEY>
    |  3. 补全 codebuff_metadata (run_id, client_id, cost_mode)
    |  4. POST /api/v1/chat/completions
    v
FreeBuff Backend (codebuff.com / freebuff.com)
    |
    v
OpenRouter -> LLM (Claude/GPT/Gemini/GLM...)
```

## 文件结构

| 文件 | 职责 |
|------|------|
| `main.go` | 入口，HTTP server，优雅关闭 |
| `config.go` | 环境变量配置加载 |
| `proxy.go` | 核心反代：请求改写 + SSE 流式透传 |
| `middleware.go` | CORS, 日志, 认证, panic 恢复 |
| `models.go` | `/v1/models` 模型列表端点 |
| `uid/uid.go` | UUID v4 生成（零依赖） |

## 关键设计

1. **直通代理**: FreeBuff 后端已是 OpenAI Chat Completions 兼容格式，无需格式转换
2. **零外部依赖**: 仅使用 Go 标准库
3. **SSE 透传**: 逐行读取上游 SSE 流，通过 `http.Flusher` 实时推送给客户端
4. **元数据注入**: 每个请求自动补全 `codebuff_metadata` 字段（run_id, cost_mode 等）

## 认证流程

1. 客户端 -> 代理: 可选（如配置了 `API_KEY`）
2. 代理 -> FreeBuff: 使用 `FREEBUFF_API_KEY` 作为 Bearer token
