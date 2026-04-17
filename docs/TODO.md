# FreeBuff2API TODO

## P0（影响可用性）
- [ ] **runId 池化**: 预热 runId 池，消除每请求 ~700ms 开销
- [ ] 支持客户端通过 header 覆盖 `cost_mode`（如 `X-Freebuff-Cost-Mode: free`）

## P1
- [ ] Prometheus metrics 端点（多账号维度）
- [ ] 启动时后端连通性自检
- [ ] 多账号加权 / least-loaded 策略
- [ ] HTTP 管理端点（参考 CLIProxyAPI `/v0/management`）
- [ ] 管理 UI 可视化 limits 面板（v0.8.1）

## 已完成
- [x] 2026-04-16: 项目初始化 + Go 反代核心实现
- [x] 2026-04-16: FreeBuff API 协议研究
- [x] 2026-04-16: 流式传输协议研究
- [x] 2026-04-17: 修正 runId 必须预注册的架构缺陷
- [x] 2026-04-17: 修正后端 URL（apex -> www）
- [x] 2026-04-17: 端到端真实请求测试 + 性能报告
- [x] 2026-04-17: 模型列表全量补齐（67 models）
- [x] 2026-04-17: **多账号轮询（v0.3.0）** —— 多 key round-robin 负载均衡
- [x] 2026-04-17: **熔断 + 热加载（v0.4.0）** —— 12h 冷却熔断 + auths/ 动态目录
- [x] 2026-04-17: **YAML 配置（v0.5.0）** —— config.yaml + fsnotify 秒级热加载
- [x] 2026-04-17: **移除 env 兼容（v0.5.1）** —— YAML 成为唯一配置来源
- [x] 2026-04-17: **下游多 key + OpenRouter 兜底（v0.6.0）** —— `server.api_keys` 列表 + `upstream.openrouter` 段；sk-or-* 自动识别与 FreeBuff 失败兜底
- [x] 2026-04-17: **移除 /v1/models 端点（v0.6.1）** —— 静态白名单会过期，与 OpenRouter 一致让客户端自己决定 model
- [x] 2026-04-17: **Admin UI + REST API（v0.7.0）** —— token.key 鉴权，单文件 glassmorphism 前端（淡蓝青绿渐变），可热改 config + 增删 key + 手动熔断
- [x] 2026-04-17: **错误过滤 + 多账号重试 + RPM 限速（v0.8.0）** —— 上游 4xx/5xx 脱敏为中文通用消息；单请求最多重试 3 个账号；三层令牌桶（global/account/client），reject-only
- [x] 2026-04-17: **公开众筹登录页（v0.9.0）** —— `/login.html` + `/public/oauth/*` 脱敏薄包装；任意用户 OAuth 登录即可捐号
- [x] 2026-04-17: **绑定式 donor key（v0.10.0）** —— 众筹成功发放 `sk-or-v1-<64hex>` key（外观与 OR 一致），强绑上游账号；账号限流/熔断时 key 同步不可用，防恶意滥用
- [x] 2026-04-18: **双激励模式（v0.11.0）** —— `incentive.mode` 可选 `donor_key`（默认）或 `redeem_code`（卡密池一次性发放）；新增 `/admin/api/redeem` 上传/查询 + 面板配置
- [x] 2026-04-18: **v0.11.0 安全加固** —— 修复三处代码审查发现问题：per-fingerprint 结果缓存防 `/public/oauth/poll` 重复消费；RedeemStore.Pop 写盘失败不再静默；donor 生成全链路 mutex 闭合 TOCTOU
- [x] 2026-04-18: **无奖励模式（v0.11.1）** —— `incentive.mode` 新增 `none` 选项，登录成功仅展示感谢语，不发放任何激励
