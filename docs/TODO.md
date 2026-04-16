# FreeBuff2API TODO

## P0（影响可用性）
- [ ] **runId 池化**: 预热 runId 池，消除每请求 ~700ms 开销
- [ ] 支持客户端通过 header 覆盖 `cost_mode`（如 `X-Freebuff-Cost-Mode: free`）
- [ ] 动态模型列表（部分 Haiku 等型号 404）

## P1
- [ ] 添加 YAML 配置文件支持
- [ ] 添加请求重试逻辑（408/429/5xx）—— 失败时切到下一 key 重试
- [ ] Prometheus metrics 端点（多账号维度）
- [ ] Rate limiting
- [ ] 启动时后端连通性自检
- [ ] 多账号熔断 / 健康检查（连续失败跳过）
- [ ] 多账号加权 / least-loaded 策略

## 已完成
- [x] 2026-04-16: 项目初始化 + Go 反代核心实现
- [x] 2026-04-16: FreeBuff API 协议研究
- [x] 2026-04-16: 流式传输协议研究
- [x] 2026-04-17: 修正 runId 必须预注册的架构缺陷
- [x] 2026-04-17: 修正后端 URL（apex -> www）
- [x] 2026-04-17: 端到端真实请求测试 + 性能报告
- [x] 2026-04-17: 模型列表全量补齐（67 models）
- [x] 2026-04-17: **多账号轮询（v0.3.0）** —— 多 key round-robin 负载均衡
