# Admin UI + REST API (v0.7.0)

**引入版本**: v0.7.0 · 2026-04-17

## 目的

提供浏览器可达的运维后台，无需 SSH 就能：
- 查看 key 池健康度（total / healthy / broken + 每 key 倒计时）
- 手动熔断 / 恢复某个 key
- 添加 / 删除 `auths/*.json`（方便新增账号）
- 编辑 `config.yaml` 并立即生效

所有操作复用现有 Reloader 机制 —— 写文件后 fsnotify 自动捕获并热加载，无重启。

## 鉴权模型

**独立文件 `token.key`**：
- 启动时 `os.ReadFile("token.key")`，内容 trim 后作为期待 token
- **缺失或空内容** → 整个 `/admin/*` 树返回 404（不暴露后台存在）
- 与 `config.yaml` 一样支持 fsnotify 热加载

**为什么不放在 config.yaml**：
- `token.key` 语义独立（运维 token ≠ 业务 downstream api_keys）
- 单独轮换更方便（改 token.key 不触碰 config.yaml 的 api_keys）
- .gitignore 粒度更清晰（token.key + config.yaml 都忽略，各自保留 `.example`）

**header 双通道**：`Authorization: Bearer <t>` 或 `X-Admin-Token: <t>`，前者兼容 curl，后者前端用（避免和 /v1/* 的 Bearer 冲突）。

## 路由分工

| 前缀 | 鉴权 | 说明 |
|---|---|---|
| `/v1/*` | `server.api_keys` 匹配 / sk-or-* | OpenAI 兼容上游代理 |
| `/admin/` | `adminGuard`（token.key） | 静态 UI（login 页免鉴权） |
| `/admin/api/*` | `adminGuard` 强制 | JSON REST |
| `/health`, `/status/keys` | 无 | 原有保留 |

`authGuard` 增加 `/v1/` 前缀检查，其他路径直接放行；`adminGuard` 单独挂在 `/admin/` 树。

## REST 端点

全部 `{"ok": bool, "data": any, "error": string}` 结构：

| Method | Path | 行为 |
|---|---|---|
| GET | `/admin/api/status` | key 池快照（复用 `writeKeyStatus` shape） |
| GET | `/admin/api/config` | 返回 config.yaml 原文，`server.api_keys` / `auth.api_keys` 替换为 fingerprint |
| PUT | `/admin/api/config` | body `{"yaml":"..."}`，YAML 解析 + Validate 校验通过后原子写入；fingerprint 占位会自动 merge 回 live 值 |
| POST | `/admin/api/keys` | body `{"label","token"}` → 写 `auths/<label>.json`（codebuff credentials 格式） |
| DELETE | `/admin/api/keys/{label}` | 删除 `auths/<label>.json` |
| POST | `/admin/api/keys/{idx}/trip` | 手动熔断第 idx 个 key（`pool.TripBreaker`） |
| POST | `/admin/api/keys/{idx}/reset` | 手动恢复（`pool.MarkSuccess`） |
| POST | `/admin/api/reload` | 强制 `reloader.Reload("admin-manual")` |

## 关键安全措施

- **Label 白名单**：`^[a-zA-Z0-9_-]{1,64}$`，防路径遍历（`../evil` 被拒）
- **原子写入**：`os.CreateTemp` + `os.Rename`，避免半写入导致 fsnotify 读到坏文件
- **YAML redaction**：`redactYAMLKeys` 用 `yaml.Node` 遍历精确替换；`mergeRedactedKeys` 允许客户端提交带 fingerprint 的部分 yaml（回写 live 值），不强迫完整复制 api_keys
- **Token 常量时比对**：目前用 `==`。未来可考虑 `subtle.ConstantTimeCompare`
- **404 by default**：缺 token.key 时不暴露 `/admin/*` 路径的存在

## 前端

单文件 `static/index.html`（~26KB），纯 HTML + 原生 JS/CSS，零依赖。Go `//go:embed all:static` 打进二进制。

- 风格：淡蓝 / 青绿 glassmorphism，动态渐变背景，stagger 入场动效
- 两个 tab：Dashboard（stat 卡 + key 表 + 添加/删除/熔断/恢复）、Config（textarea + 行号 + Ctrl+S 保存）
- localStorage 存 token；401 自动回登录
- Dashboard tab 激活时 5s 轮询 `/admin/api/status`，broken key 倒计时每秒更新

## KeyPool 新增方法

`TripBreaker(idx)` — 手动把某 key 推入熔断。实现对称 `MarkFailure`：
- 设 Fails = threshold（方便 reset）
- Broken = true
- BrokenUntil = now + cooldown

越界 idx 无操作（不 panic）。

## 测试覆盖（`admin_test.go`）

- `TestAdminGuardNoToken` — 缺 token.key → 404
- `TestAdminGuardRejectsBadToken` — 错 token → 401
- `TestAdminStatusOK` — 正常查询
- `TestAdminPutConfigValidates` — 非法 cost_mode 被拒，合法 YAML 落盘并触发 reload
- `TestAdminPostKeyWritesAuthsFile` — 检查文件内容包含 token
- `TestAdminKeyLabelSanitized` — `../evil`/空/含斜杠的 label 全部 400
- `TestAdminTripAndReset` — pool 状态正确
- `TestAdminConfigRedactsKeys` — 返回的 config yaml 不含明文 api_key

## Docker

`docker-compose.yml` 改动：
- 挂载 `./token.key:/app/token.key:ro` — 容器只读访问 admin token
- `./auths` 从 `:ro` 改成可写 — 让 admin UI 能写新 json 文件

## 后续

- [ ] 实时日志流（WebSocket），目前前端只有 5s 轮询
- [ ] Prometheus metrics 端点
- [ ] admin 操作日志审计（谁何时改了什么）
- [ ] 多 admin token（列表而非单值）支持团队运维
