# FreeBuff / Codebuff API 通信协议研究报告

> 基于 codebuff-clone 源码分析，更新于 2026-04-16

---

## 1. 后端 URL 基础配置

### 1.1 WEBSITE_URL (核心后端)

- **来源**: `common/src/env-schema.ts` 中的 `NEXT_PUBLIC_CODEBUFF_APP_URL` 环境变量
- **SDK 导出**: `sdk/src/constants.ts` -> `WEBSITE_URL = env.NEXT_PUBLIC_CODEBUFF_APP_URL`
- **生产环境**: 由编译时注入，通常为 `https://codebuff.com` 或类似域名
- **所有 API 请求都以此为 base URL**

### 1.2 FreeBuff 特殊配置

```typescript
// cli/src/login/constants.ts
const FREEBUFF_WEB_URL = IS_DEV ? 'http://localhost:3002' : 'https://freebuff.com'
export const LOGIN_WEBSITE_URL = IS_FREEBUFF ? FREEBUFF_WEB_URL : WEBSITE_URL
```

- FreeBuff 登录流程使用 `freebuff.com`，但 API 调用仍走 `WEBSITE_URL`

---

## 2. 认证体系

### 2.1 API Key (主认证方式)

**存储位置**: `~/.config/manicode/credentials.json`

```json
{
  "default": {
    "id": "user-uuid",
    "name": "User Name",
    "email": "user@example.com",
    "authToken": "cb_xxxxxxxxxxxx",
    "fingerprintId": "fp_xxxx",
    "fingerprintHash": "hash_xxxx",
    "credits": 500
  }
}
```

**获取优先级** (见 `cli/src/utils/auth.ts:getAuthTokenDetails`):
1. 文件系统凭据 (`credentials.json` -> `default.authToken`)
2. 环境变量 `CODEBUFF_API_KEY`

**使用方式**: HTTP Header

```
Authorization: Bearer <authToken>
```

### 2.2 Session Cookie (Legacy)

部分端点支持 Cookie 认证:

```
Cookie: next-auth.session-token=<authToken>;
```

使用场景: `includeCookie: true` 的请求 (如 referral, subscription)

### 2.3 Fingerprint (设备指纹)

用于登录流程和使用量追踪:
- `fingerprintId`: 客户端生成的设备标识
- `fingerprintHash`: 服务端返回的哈希值

---

## 3. API 端点清单

### 3.1 认证相关

#### POST `/api/auth/cli/code` - 获取登录 URL

**无需认证**

```typescript
// Request
{
  "fingerprintId": "fp_xxxxx"
}

// Response 200
{
  "loginUrl": "https://freebuff.com/auth/cli?code=xxxx",
  "fingerprintHash": "sha256_hash",
  "expiresAt": "2026-04-16T12:00:00Z"
}
```

#### GET `/api/auth/cli/status` - 轮询登录状态

**无需认证**

```
GET /api/auth/cli/status?fingerprintId=fp_xxx&fingerprintHash=hash_xxx&expiresAt=2026-04-16T12:00:00Z
```

```typescript
// Response 200 (已登录)
{
  "user": {
    "id": "user-uuid",
    "name": "User Name",
    "email": "user@example.com",
    "authToken": "cb_xxxxxxxxxxxx"
  }
}

// Response 200 (未登录) - 无 user 字段
{}

// Response 401 - 继续轮询
```

轮询配置:
- 间隔: 5000ms
- 超时: 5分钟

#### POST `/api/auth/cli/logout` - 登出

```typescript
// Request
{
  "userId": "user-uuid",
  "fingerprintId": "fp_xxx",
  "fingerprintHash": "hash_xxx"
}

// Response 200/204
```

### 3.2 用户信息

#### GET `/api/v1/me` - 获取用户信息

```
GET /api/v1/me?fields=id,email
Authorization: Bearer <authToken>
```

```typescript
// Response 200
{
  "id": "user-uuid",
  "email": "user@example.com"
}
```

可用字段: `id`, `email`, `discord_id`, `referral_code`

### 3.3 使用量

#### POST `/api/v1/usage` - 获取使用量

```typescript
// Request (两种方式)
// 方式1: 通过 Authorization header
{
  "fingerprintId": "cli-usage"
}

// 方式2: 通过 body 传递 authToken (legacy)
{
  "fingerprintId": "cli-usage",
  "authToken": "<authToken>"
}

// Response 200
{
  "type": "usage-response",
  "usage": 42,
  "remainingBalance": 458,
  "balanceBreakdown": {
    "free": 200,
    "paid": 258,
    "ad": 0,
    "referral": 0,
    "admin": 0
  },
  "next_quota_reset": "2026-05-01T00:00:00Z",
  "autoTopupEnabled": false
}
```

### 3.4 订阅

#### GET `/api/user/subscription` - 获取订阅信息

```
GET /api/user/subscription
Authorization: Bearer <authToken>
Cookie: next-auth.session-token=<authToken>;
```

### 3.5 Agent Runs (核心)

#### POST `/api/v1/agent-runs` - 开始/结束 Agent Run

```typescript
// START - 开始运行
// Request
{
  "action": "START",
  "agentId": "base2",
  "ancestorRunIds": ["run-uuid-1"]
}
// Response 200
{
  "runId": "run-uuid-new"
}

// FINISH - 结束运行
// Request
{
  "action": "FINISH",
  "runId": "run-uuid",
  "status": "completed",
  "totalSteps": 5,
  "directCredits": 10,
  "totalCredits": 15
}
```

#### POST `/api/v1/agent-runs/{agentRunId}/steps` - 记录 Agent 步骤

```typescript
// Request
{
  "stepNumber": 1,
  "credits": 3,
  "childRunIds": ["child-run-uuid"],
  "messageId": "msg-uuid",
  "status": "completed",
  "errorMessage": null,
  "startTime": 1713300000000
}

// Response 200
{
  "stepId": "step-uuid"
}
```

### 3.6 Agent 定义

#### GET `/api/v1/agents/{publisherId}/{agentId}/{version}` - 获取 Agent 定义

```
GET /api/v1/agents/codebuff/base2/latest
Authorization: Bearer <authToken>
```

```typescript
// Response 200
{
  "version": "1.0.0",
  "data": { /* DynamicAgentTemplate */ }
}
```

### 3.7 LLM 代理 (核心 - Chat Completions)

#### POST `/api/v1/chat/completions` - LLM 请求 (OpenAI Compatible)

这是**最核心的端点**。SDK 通过 `OpenAICompatibleChatLanguageModel` 发送请求。

```typescript
// URL 构造方式 (sdk/src/impl/model-provider.ts)
url: ({ path: endpoint }) =>
  new URL(path.join('/api/v1', endpoint), WEBSITE_URL).toString()
// 实际 URL: {WEBSITE_URL}/api/v1/chat/completions
```

**Request Headers**:
```
Authorization: Bearer <apiKey>
Content-Type: application/json
user-agent: ai-sdk/openai-compatible/{VERSION}/codebuff
x-openrouter-api-key: <byokKey>  (可选, BYOK 模式)
```

**Request Body** (OpenAI Chat Completions 格式):
```json
{
  "model": "anthropic/claude-sonnet-4",
  "messages": [
    {
      "role": "system",
      "content": "You are a helpful coding assistant..."
    },
    {
      "role": "user",
      "content": "Help me write a function"
    },
    {
      "role": "assistant",
      "content": "..."
    }
  ],
  "stream": true,
  "temperature": 0.7,
  "max_tokens": 4096,
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "read_files",
        "description": "Read file contents",
        "parameters": {
          "type": "object",
          "properties": {
            "paths": {
              "type": "array",
              "items": { "type": "string" }
            }
          }
        }
      }
    }
  ]
}
```

**Response** (SSE Stream - `stream: true`):
```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1713300000,"model":"anthropic/claude-sonnet-4","choices":[{"index":0,"delta":{"role":"assistant","content":"Here"},"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1713300000,"model":"anthropic/claude-sonnet-4","choices":[{"index":0,"delta":{"content":" is"},"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1713300000,"model":"anthropic/claude-sonnet-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"cost":0.003,"cost_details":{"upstream_inference_cost":0.002}}}

data: [DONE]
```

**Response** (Non-stream - `stream: false`):
```json
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion",
  "created": 1713300000,
  "model": "anthropic/claude-sonnet-4",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Here is the function..."
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 100,
    "completion_tokens": 50,
    "total_tokens": 150,
    "cost": 0.003,
    "cost_details": {
      "upstream_inference_cost": 0.002
    }
  }
}
```

### 3.8 健康检查

#### GET `/api/healthz` - 健康检查

```typescript
// Response 200
{
  "status": "ok"
}
```

### 3.9 其他端点

#### POST `/api/referrals` - 邀请码

```typescript
// Request
{
  "referralCode": "ABCDEF"
}
// Response 200
{
  "credits_redeemed": 500
}
```

#### POST `/api/agents/publish` - 发布 Agent

```typescript
// Request
{
  "data": [{ /* agent definitions */ }],
  "allLocalAgentIds": ["my-agent"]
}
```

#### POST `/api/v1/feedback` - 反馈

```typescript
// Request (FeedbackRequest schema)
{
  "type": "bug",
  "message": "Something went wrong",
  "metadata": {}
}
```

---

## 4. 模型路由架构

### 4.1 三层路由

SDK 中的模型路由 (`sdk/src/impl/model-provider.ts`) 有三层:

1. **Claude OAuth Direct** (已禁用 `CLAUDE_OAUTH_ENABLED = false`)
   - 直接调用 `api.anthropic.com`
   - 使用用户 OAuth Bearer token

2. **ChatGPT OAuth Direct**
   - 直接调用 `https://chatgpt.com/codex/responses`
   - 用于 FREE 模式下的 OpenAI 模型

3. **Codebuff Backend** (默认路径)
   - 所有请求走 `{WEBSITE_URL}/api/v1/chat/completions`
   - 后端转发到 OpenRouter
   - 这是我们反代需要关注的核心路径

### 4.2 Agent 模式与 Agent ID 映射

```typescript
// cli/src/utils/constants.ts
const AGENT_MODE_TO_ID = {
  DEFAULT: 'base2',        // 默认模式
  FREE: 'base2-free',      // 免费模式
  MAX: 'base2-max',        // 最强模式
  PLAN: 'base2-plan',      // 规划模式
}

const AGENT_MODE_TO_COST_MODE = {
  DEFAULT: 'normal',
  FREE: 'free',
  MAX: 'max',
  PLAN: 'normal',
}
```

### 4.3 模型选择 (按 costMode)

```typescript
// common/src/constants/model-config.ts
// agent 操作
free:         'google/gemini-2.5-flash'
normal:       'anthropic/claude-sonnet-4'
max:          'anthropic/claude-sonnet-4'
experimental: 'google/gemini-2.5-pro'

// file-requests 操作
free:   'anthropic/claude-3.5-haiku'
normal: 'anthropic/claude-3.5-haiku'
max:    'anthropic/claude-sonnet-4'
```

### 4.4 FREE 模式特殊模型

```typescript
// common/src/constants/free-agents.ts
'base2-free':        ['minimax/minimax-m2.7', 'z-ai/glm-5.1']
'file-picker':       ['google/gemini-2.5-flash-lite']
'editor-lite':       ['minimax/minimax-m2.7', 'z-ai/glm-5.1']
// 等等
```

---

## 5. SDK Client 架构

### 5.1 CodebuffClient 初始化

```typescript
// sdk/src/client.ts
const client = new CodebuffClient({
  apiKey: authToken,           // 必须
  cwd: projectRoot,            // 项目根目录
  agentDefinitions: [],        // 本地 agent 定义
  logger: logger,              // 日志器
  overrideTools: { ... },      // 工具覆写
})
```

### 5.2 client.run() 调用

```typescript
// sdk/src/run.ts
const runConfig = {
  agent: 'base2',              // agent ID 或 AgentDefinition
  prompt: 'user message',      // 用户输入
  content: messageContent,     // 图片等多媒体内容
  previousRun: runState,       // 上次运行状态 (用于续聊)
  agentDefinitions: [],        // 自定义 agent
  maxAgentSteps: 200,          // 最大步骤数
  handleStreamChunk: fn,       // 流式 chunk 回调
  handleEvent: fn,             // 事件回调
  signal: abortController.signal,
  costMode: 'normal',          // 计费模式
}

const runState = await client.run(runConfig)
```

### 5.3 RunState 结构

```typescript
// RunState 是续聊的关键 - 包含完整对话历史
interface RunState {
  output: {
    type: 'success' | 'error'
    message?: string
  }
  // 内部包含完整的消息历史和会话状态
}
```

---

## 6. HTTP 客户端配置

### 6.1 CodebuffApiClient (REST API)

```typescript
// cli/src/utils/codebuff-api.ts
const client = createCodebuffApiClient({
  baseUrl: WEBSITE_URL,           // 默认
  authToken: 'cb_xxx',            // Bearer token
  defaultTimeoutMs: 30000,        // 30秒超时
  retry: {
    maxRetries: 3,
    initialDelayMs: 1000,
    maxDelayMs: 10000,
    retryableStatusCodes: [408, 429, 500, 502, 503, 504],
  }
})
```

### 6.2 SDK fetchWithRetry

```typescript
// sdk/src/impl/database.ts
MAX_RETRIES_PER_MESSAGE = 3
RETRY_BACKOFF_BASE_DELAY_MS = 1000
RETRY_BACKOFF_MAX_DELAY_MS = 8000
RETRYABLE_STATUS_CODES = [408, 429, 500, 502, 503, 504]
```

---

## 7. 错误处理

### 7.1 HTTP 错误码

| 状态码 | 含义 | 处理 |
|--------|------|------|
| 401 | 认证失败 | 不重试，提示重新登录 |
| 402 | 余额不足 | 显示充值提示 |
| 403 | 权限不足 | 不重试 |
| 408 | 超时 | 自动重试 |
| 429 | 限流 | 自动重试 |
| 500-504 | 服务器错误 | 自动重试 |

### 7.2 Out of Credits 错误

```typescript
// 当后端返回信用不足时
// output.type === 'error' && isOutOfCreditsError(output)
// 显示 OUT_OF_CREDITS_MESSAGE 并切换到 'outOfCredits' 输入模式
```

---

## 8. 对反代项目的关键洞察

### 8.1 核心要点

1. **LLM 代理端点**: `POST /api/v1/chat/completions` 是唯一需要反代的 LLM 端点
2. **格式**: 完全遵循 OpenAI Chat Completions API 格式 (通过 `OpenAICompatibleChatLanguageModel`)
3. **流式传输**: 标准 SSE (Server-Sent Events)，格式为 `data: {...}\n\n`
4. **认证**: 所有请求都是 `Authorization: Bearer <apiKey>`

### 8.2 反代架构建议

```
Anthropic Claude API (入口)
    |
    v
Go Reverse Proxy
    |--- 转换 Anthropic Messages API -> OpenAI Chat Completions
    |--- 添加 FreeBuff/Codebuff 认证 headers
    |--- 转发到 {WEBSITE_URL}/api/v1/chat/completions
    |--- 转换 OpenAI Chat Completions -> Anthropic Messages API (响应)
    |
    v
FreeBuff Backend -> OpenRouter -> LLM
```

### 8.3 关键转换点

**Anthropic -> Codebuff (请求转换)**:
- `model` 映射: Anthropic 模型 ID -> OpenRouter 格式 (如 `claude-sonnet-4-20250514` -> `anthropic/claude-sonnet-4`)
- `messages` 格式: Anthropic 格式 -> OpenAI 格式
- `system` prompt: Anthropic 单独的 system 字段 -> OpenAI messages 中的 system role
- `max_tokens` -> `max_tokens`
- `stream` -> `stream`
- `tools` 格式转换

**Codebuff -> Anthropic (响应转换)**:
- OpenAI chunk 格式 -> Anthropic event 格式
- `usage` 字段映射
- `finish_reason` -> `stop_reason` 映射
- tool_calls 格式转换

### 8.4 环境变量需求

Go 反代需要配置:
- `CODEBUFF_API_KEY` 或 `FREEBUFF_AUTH_TOKEN`: FreeBuff 认证 token
- `CODEBUFF_APP_URL`: FreeBuff 后端 URL (默认 `https://freebuff.com` 或对应环境)
- `LISTEN_ADDR`: 本地监听地址 (如 `:8080`)

### 8.5 模型 ID 映射表 (Anthropic <-> OpenRouter)

```
claude-3-5-haiku-20241022        <-> anthropic/claude-3.5-haiku-20241022
claude-3-5-sonnet-20241022       <-> anthropic/claude-3.5-sonnet
claude-sonnet-4-20250514         <-> anthropic/claude-sonnet-4
claude-sonnet-4-5-20250929       <-> anthropic/claude-sonnet-4.5
claude-opus-4-1-20250805         <-> anthropic/claude-opus-4.1
claude-opus-4-6                  <-> anthropic/claude-opus-4.6
```

完整映射见 `common/src/constants/claude-oauth.ts` 中的 `OPENROUTER_TO_ANTHROPIC_MODEL_MAP`。
