# OpenRouter 兜底 (v0.6.0)

## 背景

FreeBuff / codebuff 后端本质上是 OpenRouter 的反代（它把请求转发到 `openrouter.ai`），因此当 FreeBuff 所有上游 key 都熔断或网络故障时，直接兜底到 OpenRouter 官方 API 是最低成本的可用性兜底。同时 v0.6.0 将 `server.api_key` (string) 变成 `server.api_keys` (list)，方便团队多 token 共享一个实例。

## 配置

```yaml
server:
  api_keys:
    - "sk-team-alice"
    - "sk-or-v1-abc..."   # 也可以直接填 OpenRouter key；会走"FreeBuff 优先 + 失败兜底"

upstream:
  openrouter:
    base_url: "https://openrouter.ai/api/v1"   # 默认值
    enabled: true                               # 默认 true；false 则完全禁用兜底
```

注意：`server.api_key` (单值) 已彻底移除，不保留向后兼容。

## 路由决策矩阵（`authGuard` + `ProxyHandler`）

| 客户端 Bearer              | 是否在 `api_keys` | OpenRouter.enabled | 处理路径                                                                                                    |
|---------------------------|-------------------|--------------------|-------------------------------------------------------------------------------------------------------------|
| 任意 / 空                  | 空列表            | false              | 放行，走 FreeBuff（无下游鉴权）                                                                             |
| 空                         | 非空              | 任意               | 401 Missing API key                                                                                         |
| 匹配 `sk-or-[20+]`         | ✗                 | true               | **force fallback**：直接转发 OpenRouter，用客户端 token 做 Bearer                                            |
| 匹配 `sk-or-*`             | ✗                 | false              | 401 Invalid API key                                                                                         |
| 在列表中（任意格式）       | ✓                 | —                  | 走 FreeBuff。若 token 本身是 `sk-or-*` 且 OR 启用：FreeBuff 失败时（pool 空 / startAgentRun 错 / 网络错 / 502/503/504）兜底到 OpenRouter |
| 不在列表且不匹配 `sk-or-` | ✗                 | —                  | 401                                                                                                         |

## 关键实现点

- `openrouter.go` 独立文件：`openRouterKeyPattern = ^sk-or-[a-zA-Z0-9_\-]{20,}$`（故意宽松，容忍未来 sk-or-v2 等形态）。
- `forwardToOpenRouter(w, r, body, client, cfg, token)` 复用预读的 body；透传 `HTTP-Referer` / `X-Title`（OpenRouter 归因头）；使用 `http.Flusher` 手工刷盘以支持 SSE 流式。
- middleware 通过 context 把 `downstream_token` 和 `force_openrouter` 传给 proxy，避免 proxy 重新解析 Authorization 头。
- proxy fallback 触发条件：`keys.Next()` 返回空 / `startAgentRun` 失败 / HTTP do 失败 / 上游 502/503/504。上游 401/403/429 等认证类错误**不**兜底（这些是 key 本身的问题，不是整体不可用）。
- `OpenRouterConfig.Enabled *bool`：使用指针区分"未设置（默认 true）"与"显式 false"；`IsEnabled()` 辅助函数封装。

## 测试覆盖

- `config_test.go` — `TestLoadConfigAPIKeysList`、`TestLoadConfigOpenRouterDefaults`、`TestLoadConfigOpenRouterDisable`、`TestLoadConfigOpenRouterInvalidURL`
- `middleware_test.go` — 全部 6 个 auth 路径分支 + `IsOpenRouterKey` 正则边界
- `openrouter_test.go` — 用 httptest 验证 force fallback、FreeBuff 失败兜底、非 sk-or 不兜底、SSE 流透传

## 已知限制

- `/v1/models` 已移除 —— 与上游 OpenRouter 一致（OpenRouter 本身就不返回静态模型列表，静态白名单在新模型发布时会过期）。`model` 字段由客户端决定，请求直接透传。
- 上游 401/403/429 不触发兜底 —— 这是语义选择（那是上游 key 的问题，不是 FreeBuff 整体故障）。若未来需要更激进兜底，在 proxy.go 里把状态码列表加宽即可。
