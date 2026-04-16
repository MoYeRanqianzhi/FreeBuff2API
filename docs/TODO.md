# FreeBuff2API TODO

## P0（影响可用性）
- [ ] **runId 池化**: 预热 runId 池，消除每请求 ~700ms 开销
- [ ] 支持客户端通过 header 覆盖 `cost_mode`（如 `X-Freebuff-Cost-Mode: free`）
- [ ] 动态模型列表（部分 Haiku 等型号 404）

## P1
- [ ] 添加请求重试逻辑（408/429/5xx）—— 失败时切到下一 key 重试
- [ ] 单请求内跨 key 自动重试（当前熔断只影响后续请求）
- [ ] Prometheus metrics 端点（多账号维度）
- [ ] Rate limiting
- [ ] 启动时后端连通性自检
- [ ] 多账号加权 / least-loaded 策略
- [ ] HTTP 管理端点（参考 CLIProxyAPI `/v0/management`）

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
