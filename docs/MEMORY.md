# FreeBuff2API 持久化记忆

## 项目概况
- [架构说明](memory/architecture.md) - 项目架构与核心设计
- [API 协议研究](memory/api-protocol.md) - FreeBuff 后端 API 完整协议
- [流式传输与模型配置](memory/streaming-models.md) - SSE 流式协议 + 模型 ID 映射
- [v0.1.0 测试报告](memory/test-report-v0.1.0.md) - 真实请求性能数据 (CN/normal)
- [US 节点部署报告](memory/deployment-us-v0.1.1.md) - **FREE 模式 US 节点可用性 + 性能**
- [多账号轮询](memory/multi-account-rotation.md) - v0.2.0 多账号 round-robin 负载均衡设计
- [YAML 配置热加载](memory/config-hot-reload.md) - v0.5.0 config.yaml + fsnotify 实时生效
- [OpenRouter 兜底](memory/openrouter-fallback.md) - v0.6.0 多下游 key + sk-or-* 兜底转发

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
- 2026-04-17: **多账号轮询（v0.3.0）** —— `FREEBUFF_API_KEY` 支持逗号/分号/换行分隔多 key；`KeyPool` 用原子计数器做请求级 round-robin；一个请求的 `startAgentRun` + `chat/completions` 绑定同一 key（runId 按账号归属）；日志用指纹脱敏
- 2026-04-17: **熔断 + 热加载（v0.4.0）** —— 连续 3 次失败熔断，12h 后自动恢复；`auths/*.json` 目录放 codebuff `credentials.json`（读取 `authToken`），默认 15s 扫描一次热加载；`/status/keys` 端点查看健康状况
- 2026-04-17: **YAML 配置热加载（v0.5.0）** —— 参考 `router-for-me/CLIProxyAPI`，引入 `config.yaml` 作为单一事实来源（server/upstream/auth/breaker/logging），fsnotify + 15s 轮询兜底实现秒级热加载；`-config` CLI 参数；Reloader 保留熔断状态 + 活 `Current()` 读取，middleware/proxy 运行时读配置，无需重启
- 2026-04-17: **移除 env 兼容（v0.5.1）** —— 彻底清理环境变量支持，config.yaml 成为唯一事实来源；missing/empty path 启动直接 fatal，避免 env+yaml 两套配置混淆
- 2026-04-17: **下游多 key + OpenRouter 兜底（v0.6.0）** —— `server.api_key` (string) 升级为 `server.api_keys` ([]string)，**不保留**单值兼容；新增 `upstream.openrouter` 段（默认 enabled=true, base_url=https://openrouter.ai/api/v1）；客户端 Bearer 若匹配 `^sk-or-[a-zA-Z0-9_\-]{20,}$` 且不在 `api_keys` → 直接转发 OpenRouter；若在列表中且本身是 sk-or- 格式 → FreeBuff 全部失败时兜底 OpenRouter。详见 `memory/openrouter-fallback.md`
- 2026-04-17: **移除 /v1/models 端点（v0.6.1）** —— 静态白名单会过期，OpenRouter 本身也不暴露稳定目录；model 字段交由客户端决定并直通上游
- 2026-04-17: **Admin UI + REST（v0.7.0）** —— 独立 `token.key` 文件承载 admin token（缺失即 `/admin/*` 全 404），与 `server.api_keys` 语义分离；REST 端点 `/admin/api/{status,config,keys,reload}` 全部热加载文件而非进程内修改（fsnotify 自然 reload）；单文件 glassmorphism 前端（淡蓝青绿渐变），零依赖 + `//go:embed`。详见 `memory/admin-ui.md`
- 2026-04-17: **错误过滤 + 多账号重试 + RPM 限速（v0.8.0）** —— 三件事合并发布：(1) 上游 4xx/5xx 错误体统一脱敏，不再泄漏 "account suspended" 等细节；空池/繁忙均中文提示；(2) 单次请求内最多重试 3 个账号（`min(3, healthy)`），覆盖 401/402/403/429/5xx/网络错误，429 不触发熔断；(3) 可选 `limits.{global,account,client}_rpm` 三层令牌桶，reject-only（不排队），`account_rpm` 天然充当负载均衡器（round-robin 跳过达限账号），保持零外部依赖
- 2026-04-17: **公开众筹登录页（v0.9.0）** —— `/login.html` 免鉴权，任意用户 OAuth 登录后自动把 codebuff 凭证存入 `auths/` 扩充号池。`/public/oauth/{start,poll}` 为 admin OAuth 的脱敏薄包装：`start` 只回 `login_url/fingerprint_*`，`poll` 成功时只回 `{done:true, email_masked:"jo***@gmail.com"}`，**不**含 authToken/user id/name/label。label 沿用 `sanitizeLabel(email)`，fingerprint 前缀 `fp_pub_` 与 admin `fp_admin_` 分离便于审计。`/index.html` 保持 404 以避免暴露 admin 面板存在
- 2026-04-17: **绑定式 donor key（v0.10.0）** —— 众筹登录成功后自动发放一把 `sk-or-v1-<64hex>` 格式的 API key（外观与真实 OpenRouter v1 key 完全一致，便于贡献者直接放入任何支持 OR 的客户端），作为对贡献者的反哺。该 key 写入 `auths/<label>.json` 的 `donorKey` 字段，与其上游账号严格 1:1；持有者用它做客户端 Bearer 调 `/v1/*` 时，proxy 会 pin 到对应上游账号：**账号限流 → 429、熔断 → 503、全部不跨账号兜底**。KeyPool 新增 `donors []string` 平行数组 + `ResolveDonorKey/SetDonorKey/IsBroken`；authGuard 按值（非前缀）查 donor 表，命中 pin、未命中再 fall through 到 sk-or OR 转发，所以注册的 donor token 不会被误当作 OR key；proxy 单独 `servePinned` 分支。admin 面板每行增 donor 列（生成/重置/复制/清除/自定义），`POST/DELETE /admin/api/keys/{label}/donor` 直接改写凭证 JSON。生成 key 时对全局 donors + server/auth api_keys 做冲突检测并重试 8 次，保证唯一。设计目的：防止贡献者恶意使用 key 清空整个号池
- 2026-04-18: **双激励模式（v0.11.0）** —— 管理员可在 `incentive.mode` 配置项中选 `donor_key`（默认，继承 v0.10.0 行为）或 `redeem_code`（发放一次性卡密）。卡密池 `incentive.redeem_codes_file` 一行一条，`#` 开头与空行忽略；RedeemStore 互斥锁保护 `Pop`（发放即从文件原子删除）和 `Append`（批量去重追加）。OAuth 成功路径按模式二选一地返回 `donor_key` 或 `redeem_code`+`redeem_usage`，池为空时不阻断登录（凭证仍入池，贡献者看到"奖励池已空"提示）。管理面板新增激励 stat 卡 + 激励配置面板 + `/admin/api/redeem` GET/POST 端点；`reloader.onConfig` 回调把 `redeem_codes_file` 变更同步到 RedeemStore
- 2026-04-18: **v0.11.0 安全加固** —— 三处代码审查发现的问题全部修复：(1) **critical**：`/public/oauth/poll` 新增 per-fingerprint 结果缓存（`AdminHandler.pollCache` + 10 min TTL），同一 fingerprint 重复轮询不会再重复消费卡密/重新生成 donor key/创建重复 auths 文件；(2) `RedeemStore.Pop` 写盘失败时返回 `("", false)` 而非静默吞错，杜绝"code 已返还但文件未更新 → 下次 Pop 重复发放"的数据损坏；(3) donor key 生成 TOCTOU 闭合 —— 新增 `AdminHandler.donorMu` 跨所有 mint 路径（admin donor endpoint + oauthPoll）串行化 `generateDonorKey → writeCredentialDonor → reloader.Reload`，8 次重试仍冲突改为返回 error 而非未检查的 candidate

## 实测性能（v0.1.0）
- TTFT: 2.2 ~ 3.2s（含 runId 注册 ~700ms）
- 吞吐: 39 ~ 55 tok/s
- Proxy 自身开销 < 2ms, 内存 ~12MB
