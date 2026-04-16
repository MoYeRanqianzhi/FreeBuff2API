# FreeBuff (Codebuff) 流式传输协议 & 模型配置 研究报告

> 研究时间: 2026-04-16
> 源码版本: codebuff-clone (本地临时目录)

---

## 1. 整体架构概览

FreeBuff/Codebuff 采用 **两层通信架构**:

1. **CLI <-> Backend (WebSocket)**: CLI 客户端通过 WebSocket 与 Codebuff 后端通信，交换 `ClientAction` 和 `ServerAction` 消息
2. **Backend <-> LLM Provider (SSE/HTTP)**: 后端通过 OpenAI Chat Completions 兼容格式（SSE 流）与 OpenRouter/Anthropic/OpenAI 等 LLM 提供商通信

**关键发现**: Codebuff 后端 (`/api/v1/chat/completions`) 本质上是一个 **OpenAI Chat Completions API 兼容代理**。它接收标准 Chat Completions 请求，转发给 OpenRouter（或其他 provider），然后将 SSE 响应流回客户端。

---

## 2. 流式传输协议

### 2.1 核心协议: SSE (Server-Sent Events)

**协议格式**: 标准 OpenAI Chat Completions 流式格式

**Content-Type**: `text/event-stream`

**响应头**:
```
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
Access-Control-Allow-Origin: *
```

### 2.2 SSE 事件格式

每一行格式为:
```
data: {JSON}\n\n
```

特殊事件:
```
: connected 2026-04-16T12:00:00.000Z\n     # 初始连接确认（SSE comment）
: heartbeat 2026-04-16T12:00:30.000Z\n\n   # 心跳，每30秒
data: [DONE]\n\n                             # 流结束信号
```

### 2.3 流式 Chunk 结构 (OpenRouter/Chat Completions 格式)

```typescript
// 标准 SSE Chunk
{
  id: string,           // 响应ID（如 "chatcmpl-xxx"）
  model: string,        // 实际模型名（如 "anthropic/claude-sonnet-4"）
  provider: string,     // 提供商名
  created: number,      // Unix timestamp
  choices: [{
    index: number,
    delta: {
      role?: "assistant",          // 仅首个 chunk
      content?: string | null,     // 文本内容增量
      reasoning?: string | null,   // 推理内容增量（思考过程）
      reasoning_details?: ReasoningDetail[],  // 结构化推理
      tool_calls?: [{              // 工具调用增量
        index: number | null,
        id?: string | null,        // 工具调用ID
        type?: "function",
        function: {
          name?: string | null,    // 工具名
          arguments?: string | null // 参数（增量 JSON 字符串）
        }
      }],
      annotations?: [{
        type: "url_citation",
        url_citation: { end_index, start_index, title, url, content? }
      }]
    },
    logprobs?: { content: [{ token, logprob, top_logprobs }] } | null,
    finish_reason?: string | null  // "stop" | "length" | "tool_calls" | null
  }],
  usage?: {                        // 仅最终 chunk 包含
    prompt_tokens: number,
    prompt_tokens_details?: { cached_tokens: number },
    completion_tokens: number,
    completion_tokens_details?: { reasoning_tokens: number },
    total_tokens: number,
    cost?: number,
    cost_details?: { upstream_inference_cost?: number }
  }
}
```

### 2.4 终止信号

| finish_reason | 含义 |
|---|---|
| `"stop"` | 正常结束 |
| `"length"` | 达到 token 上限 |
| `"tool_calls"` | 模型请求工具调用 |
| `null` | 流进行中（非终止 chunk） |

流终止序列:
1. 最终 chunk 包含 `finish_reason` + `usage`
2. 接着是 `data: [DONE]\n\n`

---

## 3. SDK 内部 StreamChunk 类型

SDK 将 LLM 流解析为内部 `StreamChunk` 类型:

```typescript
type StreamChunk =
  | { type: 'text'; text: string; agentId?: string }     // 文本增量
  | { type: 'reasoning'; text: string }                   // 推理增量
  | { type: 'tool-call'; toolCallId: string; toolName: string; input: Record<string, unknown> }  // 完整工具调用
  | { type: 'error'; message: string }                    // 错误
```

**注意**: SDK 使用 Vercel AI SDK (`ai` package) 的 `streamText` 函数与 LLM 交互，通过 `fullStream` 迭代器获取 chunk。AI SDK 将 OpenAI 格式的流自动解析为内部事件类型:
- `text-delta` -> `StreamChunk { type: 'text' }`
- `reasoning-delta` -> `StreamChunk { type: 'reasoning' }`
- `tool-call` -> `StreamChunk { type: 'tool-call' }`
- `error` -> 各种错误处理逻辑

---

## 4. CLI <-> Backend 通信 (WebSocket Actions)

### 4.1 Client -> Server (ClientAction)

```typescript
type ClientAction =
  | { type: 'init'; fingerprintId: string; authToken?: string; fileContext; repoUrl? }
  | { type: 'prompt'; promptId: string; prompt?: string; content?: (TextPart|ImagePart)[];
      fingerprintId: string; authToken?: string; costMode?: string;
      sessionState: SessionState; toolResults: ToolMessage[]; model?: string; repoUrl?; agentId? }
  | { type: 'read-files-response'; files: Record<string, string|null>; requestId? }
  | { type: 'tool-call-response'; requestId: string; output: ToolResultOutput[] }
  | { type: 'cancel-user-input'; authToken: string; promptId: string }
  | { type: 'mcp-tool-data'; requestId: string; tools: [...] }
```

### 4.2 Server -> Client (ServerAction)

```typescript
type ServerAction =
  | { type: 'response-chunk'; userInputId: string; chunk: string | PrintModeEvent }
  | { type: 'subagent-response-chunk'; userInputId: string; agentId: string; agentType: string; chunk: string; prompt?; forwardToPrompt? }
  | { type: 'handlesteps-log-chunk'; userInputId: string; agentId: string; level: 'debug'|'info'|'warn'|'error'; data: any; message? }
  | { type: 'prompt-response'; promptId: string; sessionState: SessionState; toolCalls?; toolResults?; output?: AgentOutput }
  | { type: 'read-files'; filePaths: string[]; requestId: string }
  | { type: 'tool-call-request'; userInputId: string; requestId: string; toolName: string; input; timeout?; mcpConfig? }
  | { type: 'init-response'; message?; agentNames?; usage; remainingBalance; ... }
  | { type: 'usage-response'; usage; remainingBalance; balanceBreakdown?; next_quota_reset; ... }
  | { type: 'message-cost-response'; promptId: string; credits: number; agentId? }
  | { type: 'action-error'; message: string; error?; remainingBalance? }
  | { type: 'prompt-error'; userInputId: string; message: string; error?; remainingBalance? }
  | { type: 'request-reconnect' }
  | { type: 'request-mcp-tool-data'; requestId: string; mcpConfig; toolNames? }
```

### 4.3 PrintModeEvent 类型 (response-chunk 中的结构化事件)

```typescript
type PrintModeEvent =
  | { type: 'start'; agentId?; messageHistoryLength }
  | { type: 'error'; message }
  | { type: 'download'; version; status: 'complete'|'failed' }
  | { type: 'tool_call'; toolCallId; toolName; input; agentId?; parentAgentId?; includeToolCall? }
  | { type: 'tool_result'; toolCallId; toolName; output: ToolResultOutput[]; parentAgentId? }
  | { type: 'text'; text; agentId? }
  | { type: 'finish'; agentId?; totalCost }
  | { type: 'subagent_start'; agentId; agentType; displayName; onlyChild; parentAgentId?; params?; prompt? }
  | { type: 'subagent_finish'; agentId; agentType; displayName; onlyChild; parentAgentId?; params?; prompt? }
  | { type: 'reasoning_delta'; text; ancestorRunIds; runId }
```

---

## 5. 消息类型

### 5.1 Message 类型 (Codebuff 内部格式)

```typescript
type Message =
  | { role: 'system'; content: TextPart[] }
  | { role: 'user'; content: (TextPart | ImagePart | FilePart)[] }
  | { role: 'assistant'; content: (TextPart | ReasoningPart | ToolCallPart)[] }
  | { role: 'tool'; toolCallId: string; toolName: string; content: ToolResultOutput[] }

type TextPart = { type: 'text'; text: string; providerOptions? }
type ImagePart = { type: 'image'; image: Buffer|URL; mediaType?; providerOptions? }
type FilePart = { type: 'file'; data: Buffer|URL; filename?; mediaType: string; providerOptions? }
type ReasoningPart = { type: 'reasoning'; text: string; providerOptions? }
type ToolCallPart = { type: 'tool-call'; toolCallId: string; toolName: string; input: Record<string,unknown>; providerOptions?; providerExecuted? }
type ToolResultOutput = { type: 'json'; value: any } | { type: 'media'; data: string; mediaType: string }
```

### 5.2 AgentOutput 类型

```typescript
type AgentOutput =
  | { type: 'structuredOutput'; value: Record<string,any> | null }
  | { type: 'lastMessage'; value: any[] }
  | { type: 'allMessages'; value: any[] }
  | { type: 'error'; message: string; statusCode?; error? }
```

---

## 6. 模型配置

### 6.1 CostMode (费用模式)

```typescript
type CostMode = 'free' | 'normal' | 'max' | 'experimental' | 'ask'
```

### 6.2 模型 ID 映射

#### OpenRouter 模型（通过 OpenRouter API 路由）
| 键名 | 模型 ID |
|---|---|
| openrouter_claude_sonnet_4_5 | `anthropic/claude-sonnet-4.5` |
| openrouter_claude_sonnet_4 | `anthropic/claude-4-sonnet-20250522` |
| openrouter_claude_opus_4 | `anthropic/claude-opus-4.1` |
| openrouter_claude_3_5_haiku | `anthropic/claude-3.5-haiku-20241022` |
| openrouter_claude_3_5_sonnet | `anthropic/claude-3.5-sonnet-20240620` |
| openrouter_gpt4o | `openai/gpt-4o-2024-11-20` |
| openrouter_gpt5 | `openai/gpt-5.1` |
| openrouter_gpt5_chat | `openai/gpt-5.1-chat` |
| openrouter_gpt4o_mini | `openai/gpt-4o-mini-2024-07-18` |
| openrouter_gpt4_1_nano | `openai/gpt-4.1-nano` |
| openrouter_o3_mini | `openai/o3-mini-2025-01-31` |
| openrouter_gemini2_5_pro_preview | `google/gemini-2.5-pro` |
| openrouter_gemini2_5_flash | `google/gemini-2.5-flash` |
| openrouter_gemini2_5_flash_thinking | `google/gemini-2.5-flash-preview:thinking` |
| openrouter_grok_4 | `x-ai/grok-4-07-09` |

#### OpenAI 直连模型
| 键名 | 模型 ID |
|---|---|
| gpt4_1 | `gpt-4.1-2025-04-14` |
| gpt4o | `gpt-4o-2024-11-20` |
| gpt4omini | `gpt-4o-mini-2024-07-18` |
| o3mini | `o3-mini-2025-01-31` |
| o3 | `o3-2025-04-16` |
| o3pro | `o3-pro-2025-06-10` |
| o4mini | `o4-mini-2025-04-16` |

#### DeepSeek 模型
| 键名 | 模型 ID |
|---|---|
| deepseekChat | `deepseek-chat` |
| deepseekReasoner | `deepseek-reasoner` |

#### 短名称映射
| 短名 | 完整 ID |
|---|---|
| `gemini-2.5-pro` | `google/gemini-2.5-pro` |
| `flash-2.5` | `google/gemini-2.5-flash` |
| `opus-4` | `anthropic/claude-opus-4.1` |
| `sonnet-4.5` | `anthropic/claude-sonnet-4.5` |
| `sonnet-4` | `anthropic/claude-4-sonnet-20250522` |
| `gpt-4.1` | `gpt-4.1-2025-04-14` |
| `o3-mini` | `o3-mini-2025-01-31` |
| `o3` | `o3-2025-04-16` |
| `o4-mini` | `o4-mini-2025-04-16` |
| `o3-pro` | `o3-pro-2025-06-10` |

### 6.3 Cost Mode -> Agent 模型映射

```typescript
getModelForMode(costMode, 'agent'):
  free:          google/gemini-2.5-flash
  normal:        anthropic/claude-4-sonnet-20250522
  max:           anthropic/claude-4-sonnet-20250522
  experimental:  google/gemini-2.5-pro
  ask:           google/gemini-2.5-pro

getModelForMode(costMode, 'file-requests'):
  free:          anthropic/claude-3.5-haiku-20241022
  normal:        anthropic/claude-3.5-haiku-20241022
  max:           anthropic/claude-4-sonnet-20250522
  experimental:  anthropic/claude-4-sonnet-20250522
  ask:           anthropic/claude-3.5-haiku-20241022
```

### 6.4 FREE 模式 Agent-Model 白名单

```typescript
FREE_MODE_AGENT_MODELS = {
  'base2-free':                 ['minimax/minimax-m2.7', 'z-ai/glm-5.1']
  'file-picker':                ['google/gemini-2.5-flash-lite']
  'file-picker-max':            ['google/gemini-3.1-flash-lite-preview']
  'file-lister':                ['google/gemini-3.1-flash-lite-preview']
  'researcher-web':             ['google/gemini-3.1-flash-lite-preview']
  'researcher-docs':            ['google/gemini-3.1-flash-lite-preview']
  'basher':                     ['google/gemini-3.1-flash-lite-preview']
  'editor-lite':                ['minimax/minimax-m2.7', 'z-ai/glm-5.1']
  'code-reviewer-lite':         ['minimax/minimax-m2.7', 'z-ai/glm-5.1']
  'thinker-with-files-gemini':  ['google/gemini-3.1-pro-preview']
}
```

---

## 7. 模型提供商路由

### 7.1 路由决策链

SDK `getModelForRequest()` 按如下优先级选择模型 provider:

1. **Claude OAuth** (直连 Anthropic API): 如果用户有 Claude OAuth token 且模型是 Claude 系列
2. **ChatGPT OAuth** (直连 ChatGPT Backend API): 如果用户有 ChatGPT OAuth token 且模型在白名单
3. **Codebuff Backend** (默认): 通过 `{WEBSITE_URL}/api/v1/chat/completions` 转发到 OpenRouter

### 7.2 Codebuff Backend API 端点

```
POST {WEBSITE_URL}/api/v1/chat/completions
Authorization: Bearer {apiKey}
Content-Type: application/json
```

请求体为标准 Chat Completions 格式，额外包含:
```json
{
  "model": "anthropic/claude-sonnet-4",
  "messages": [...],
  "stream": true,
  "codebuff_metadata": {
    "run_id": "...",
    "client_id": "...",
    "cost_mode": "free",
    "n": 1
  },
  "provider": {
    "order": ["Google", "Anthropic", "Amazon Bedrock"],
    "allow_fallbacks": true
  },
  "usage": { "include": true }
}
```

### 7.3 Provider 路由偏好

```typescript
providerOrder = {
  'anthropic/claude-sonnet-4':   ['Google', 'Anthropic', 'Amazon Bedrock'],
  'anthropic/claude-sonnet-4.5': ['Google', 'Anthropic', 'Amazon Bedrock'],
  'anthropic/claude-opus-4.1':   ['Google', 'Anthropic'],
}
```

### 7.4 OpenRouter 请求格式

后端直接转发到 `https://openrouter.ai/api/v1/chat/completions`:

```
POST https://openrouter.ai/api/v1/chat/completions
Authorization: Bearer {OPEN_ROUTER_API_KEY}
HTTP-Referer: https://codebuff.com
X-Title: Codebuff
Content-Type: application/json
```

---

## 8. ChatGPT OAuth 特殊处理 (Responses API 桥接)

当使用 ChatGPT OAuth 时，SDK 使用自定义 fetch 在两种格式间转换:

### 8.1 Request 转换: Chat Completions -> Responses API

```
输入:  POST /api/v1/chat/completions (Chat Completions 格式)
输出:  POST chatgpt.com/backend-api/codex/responses (Responses API 格式)
```

转换规则:
- `messages` -> `input` (角色映射: user->user, assistant->assistant, tool->function_call_output)
- `system` messages -> `instructions` 字段
- `tools[].function` -> 直接展开
- 添加 `reasoning: { effort: 'high', summary: 'auto' }`
- 添加 `include: ['reasoning.encrypted_content']`

### 8.2 Response 转换: Responses API SSE -> Chat Completions SSE

| Responses API Event | Chat Completions Event |
|---|---|
| `response.created` | role: 'assistant' delta |
| `response.output_text.delta` | content delta |
| `response.reasoning_summary_text.delta` | reasoning_content delta |
| `response.output_item.added` (function_call) | tool_calls delta (name) |
| `response.function_call_arguments.delta` | tool_calls delta (arguments) |
| `response.completed` / `response.done` | finish_reason + usage + [DONE] |
| `response.failed` | error + [DONE] |

---

## 9. 流状态管理

### 9.1 StreamController

```typescript
type StreamState = {
  rootStreamBuffer: string              // 主流文本缓冲
  agentStreamAccumulators: Map<string, string>  // 子agent流累加器
  rootStreamSeen: boolean               // 是否收到过根流数据
  planExtracted: boolean                // 是否已提取计划
  wasAbortedByUser: boolean             // 用户是否中止
  spawnAgentsMap: Map<string, SpawnAgentInfo>   // 已生成的子agent映射
}
```

### 9.2 StreamStatus

```typescript
type StreamStatus = 'idle' | 'waiting' | 'streaming'
```

### 9.3 消息队列

客户端维护消息队列(`useMessageQueue`)，支持:
- 顺序处理消息
- 用户暂停/恢复
- 看门狗超时（60秒自动恢复）
- 队列阻塞条件: 链进行中、活跃子agent流、正在处理中

---

## 10. Agent 体系

### 10.1 Agent 类型

```typescript
AgentTemplateTypes = [
  'base', 'base-free', 'base-max', 'base-experimental',
  'claude4-gemini-thinking', 'superagent', 'base-agent-builder',
  'ask', 'planner', 'dry-run', 'thinker',
  'file-picker', 'file-explorer', 'researcher',
  'reviewer', 'agent-builder', 'example-programmatic'
]
```

### 10.2 Agent 角色

| Agent | 显示名 | 用途 |
|---|---|---|
| base | Buffy the Base Agent | 主编排agent |
| ask | Ask Mode Agent | 问答模式 |
| thinker | Theo the Theorizer | 深度思考 |
| file-explorer | Dora The File Explorer | 代码库探索 |
| file-picker | Fletcher the File Fetcher | 文件查找 |
| researcher | Reid Searcher | 研究/搜索 |
| reviewer | Nit Pick Nick | 代码审查 |
| agent-builder | Bob the Agent Builder | 创建新agent模板 |

### 10.3 Agent 状态

```typescript
type AgentState = {
  agentId: string
  agentType: string | null
  agentContext: Record<string, Subgoal>
  ancestorRunIds: string[]
  runId?: string
  subagents: AgentState[]
  childRunIds: string[]
  messageHistory: Message[]
  stepsRemaining: number          // 默认 200
  creditsUsed: number
  directCreditsUsed: number
  output?: Record<string, any>
  parentId?: string
  systemPrompt: string
  toolDefinitions: Record<string, { description?: string; inputSchema: {} }>
  contextTokenCount: number
}
```

---

## 11. 缓存控制

支持 cache_control 的模型:
- 所有 `anthropic/` 前缀模型
- 所有 `openai/` 前缀模型
- 特定列表: `anthropic/claude-opus-4.1`, `anthropic/claude-sonnet-4`, `anthropic/claude-3.5-haiku`, `z-ai/glm-4.5`, `qwen/qwen3-coder`

不支持的模型: `x-ai/grok-4-07-09`

`shouldCacheModels` 列表用于决定是否在消息上添加 `cache_control: { type: 'ephemeral' }` 提示。

---

## 12. 对 Go SSE Proxy 的关键设计影响

### 12.1 需要代理的核心流

**场景**: 用户通过 Claude Code 发送请求 -> Go Proxy -> Codebuff Backend -> OpenRouter -> LLM

Go Proxy 需要:
1. 接收 Anthropic Claude Messages API 格式的请求
2. 转换为 OpenAI Chat Completions 格式
3. 转发到 Codebuff 后端 `/api/v1/chat/completions`
4. 将返回的 SSE 流（OpenAI 格式）转换回 Anthropic 格式
5. 以 SSE 方式流回给 Claude Code

### 12.2 认证

- Codebuff API Key: `Authorization: Bearer {apiKey}`
- 可选 BYOK OpenRouter Key: `x-openrouter-api-key` header

### 12.3 关键请求字段

必须在请求体中包含:
- `codebuff_metadata.run_id` (必填，后端验证)
- `codebuff_metadata.client_id`
- `codebuff_metadata.cost_mode` (free 模式标识)
- `stream: true` (流式请求)
- `usage.include: true` (获取 token 使用量)

### 12.4 错误处理

| HTTP Status | 含义 |
|---|---|
| 401 | API key 无效 |
| 402 | 余额不足 |
| 403 | 账户被封/地区限制 |
| 429 | 速率限制 |
| 500 | 内部错误 |

### 12.5 FREE 模式特殊规则

- 仅限美国、加拿大、英国、澳洲、新西兰及部分欧洲国家
- 有独立的速率限制 (checkFreeModeRateLimit)
- 使用特定的 agent-model 白名单组合
- ChatGPT OAuth 在 free 模式下不回退到后端

### 12.6 数据流转换要点

**OpenAI -> Anthropic 格式转换**:

| OpenAI SSE 字段 | Anthropic SSE 事件 |
|---|---|
| `delta.role: "assistant"` | `message_start` event |
| `delta.content: "..."` | `content_block_delta` (text_delta) |
| `delta.reasoning: "..."` | `content_block_delta` (thinking_delta) |
| `delta.tool_calls[].function.name` | `content_block_start` (tool_use) |
| `delta.tool_calls[].function.arguments` | `content_block_delta` (input_json_delta) |
| `finish_reason: "stop"` | `message_delta` (stop_reason: "end_turn") |
| `finish_reason: "tool_calls"` | `message_delta` (stop_reason: "tool_use") |
| `finish_reason: "length"` | `message_delta` (stop_reason: "max_tokens") |
| `usage` | `message_delta` (usage) |
| `data: [DONE]` | 关闭 SSE 连接 |
