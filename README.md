# FreeBuff2API

将 FreeBuff / Codebuff 反向代理为 OpenAI 兼容的 API 接口，方便接入 Claude Code、ChatGPT-Next-Web 等任意支持 OpenAI API 的客户端。

## 功能特性

- 兼容 OpenAI `/v1/chat/completions` 接口，支持 SSE 流式输出
- 多账号密钥池，round-robin 轮询负载均衡
- 熔断机制：连续 3 次失败自动冷却 12 小时，到期自动恢复
- 配置热加载：基于 fsnotify 监听 `config.yaml`，修改后秒级生效，无需重启
- 管理后台 `/admin/`，通过 `token.key` 文件启用，支持在线管理密钥与查看状态
- 公开众筹登录页 `/login.html`，支持 GitHub OAuth Device Code 流程
- 贡献者激励系统：donor key / 兑换码模式可选
- OpenRouter 兜底：FreeBuff 全部失败时自动回退到 OpenRouter
- 三层速率限制：全局 RPM、单账号 RPM、单客户端 RPM
- Free 模式：零成本 BYOK
- Docker 镜像约 15MB（Alpine + 静态二进制）

## 快速开始

### 前置条件

- Docker + Docker Compose
- 至少一个 FreeBuff / Codebuff 账号的 authToken 或 `credentials.json`

### 部署步骤

1. 克隆仓库：

```bash
git clone https://github.com/MoYeRanQianZhi/FreeBuff2API.git
cd FreeBuff2API
```

2. 创建配置文件：

```bash
cp config.example.yaml config.yaml
```

3. 编辑 `config.yaml`，填入你的 authToken：

```yaml
auth:
  api_keys:
    - "cb_live_xxx..."
```

或者将 `credentials.json` 文件放入 `auths/` 目录（支持热加载）。

4. （可选）启用管理后台：

```bash
echo "your-admin-token" > token.key
```

5. 启动服务：

```bash
docker compose up -d
```

服务默认监听 `127.0.0.1:28666`。

## 配置说明

所有配置集中在 `config.yaml` 一个文件中，支持热加载。主要配置项：

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `server.listen` | HTTP 监听地址 | `:8080` |
| `server.api_keys` | 客户端鉴权 Token 列表 | `[]`（不鉴权） |
| `upstream.base_url` | 上游地址 | `https://www.codebuff.com` |
| `upstream.cost_mode` | 计费模式 `free` / `normal` | `free` |
| `upstream.default_model` | 默认模型 | `anthropic/claude-sonnet-4` |
| `auth.api_keys` | 内联 authToken 列表 | `[]` |
| `auth.dir` | credentials 目录 | `auths` |
| `auth.breaker.threshold` | 熔断触发次数 | `3` |
| `auth.breaker.cooldown` | 熔断冷却时长 | `12h` |
| `limits.global_rpm` | 全局 RPM 限制 | `0`（不限） |
| `limits.account_rpm` | 单账号 RPM | `0` |
| `limits.client_rpm` | 单客户端 RPM | `0` |

完整配置参考 `config.example.yaml`。

## API 用法

### 非流式请求

```bash
curl http://127.0.0.1:28666/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-api-key" \
  -d '{
    "model": "anthropic/claude-sonnet-4",
    "messages": [
      {"role": "user", "content": "Hello"}
    ]
  }'
```

### 流式请求

```bash
curl http://127.0.0.1:28666/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-api-key" \
  -d '{
    "model": "anthropic/claude-sonnet-4",
    "messages": [
      {"role": "user", "content": "Hello"}
    ],
    "stream": true
  }'
```

### 健康检查

```bash
curl http://127.0.0.1:28666/health
```

### 密钥池状态

```bash
curl http://127.0.0.1:28666/status/keys
```

## 在 Claude Code 中使用

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:28666
export ANTHROPIC_API_KEY=sk-your-api-key
claude
```

## 许可证

本项目基于 [MIT License](LICENSE) 开源。
