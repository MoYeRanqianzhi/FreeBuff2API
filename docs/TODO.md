# FreeBuff2API TODO

## 待办
- [ ] 添加 YAML 配置文件支持（当前仅环境变量）
- [ ] 添加请求重试逻辑（408/429/5xx）
- [ ] 添加 Prometheus metrics 端点
- [ ] 添加 rate limiting
- [ ] 端到端测试（需要有效的 FreeBuff API key）

## 已完成
- [x] 2026-04-16: 项目初始化 + Go 反代核心实现
- [x] 2026-04-16: FreeBuff API 协议研究
- [x] 2026-04-16: 流式传输协议研究
