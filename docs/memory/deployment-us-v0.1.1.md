# US 节点部署与 FREE 模式验证报告

> 测试时间: 2026-04-17  
> 部署目标: remote3 (Los Angeles, US · AS400619 AROSSCLOUD INC. · IP 38.55.179.54)  
> 部署方式: Docker Compose v2  
> 部署路径: `~/m/freebuff2api/`  
> 端口: 28666（仅内网）

---

## 1. FREE 模式可用性

### ✅ 在 US 节点 FREE 模式工作正常

首次测试响应样本：
```
HTTP 200 in 2.08s
{
  "model": "google/gemini-2.5-flash",
  "choices": [{"message": {"content": "Hi there, welcome!"}}],
  "usage": {"cost": 0, "is_byok": true, ...}
}
```

**关键验证点**：
- `cost: 0` → FREE 模式未产生账户扣费
- `is_byok: true` → 后端使用 BYOK 池，符合 FREE 模式规则
- 无 `free_mode_unavailable` 错误

---

## 2. 部署架构

```
┌──────────────── remote3 (US) ────────────────┐
│                                              │
│  External Internet ──X──▶ 38.55.179.54:28666 │
│                                              │
│  Host processes ─────▶ 127.0.0.1:28666 ──┐   │
│                                          │   │
│  Containers (any net) ─▶ 172.17.0.1:28666│   │
│                                          ▼   │
│                        ┌─────────────────────┐
│                        │  freebuff2api       │
│                        │  :8080              │
│                        │  cost_mode=free     │
│                        └──────────┬──────────┘
│                                   │           │
│                                   ▼           │
│                        https://www.codebuff.com
└──────────────────────────────────────────────┘
```

### 端口绑定策略

```yaml
ports:
  - "127.0.0.1:28666:8080"   # host loopback
  - "172.17.0.1:28666:8080"  # docker0 bridge gateway
```

`docker0` 网桥 (`172.17.0.1`) 是所有 Docker 容器的默认出口网关，因此**任何容器**都可以通过这个地址访问，无需加入特定网络。

### 访问控制矩阵

| 访问路径 | 结果 | 备注 |
|---|---|---|
| 公网 IP `38.55.179.54:28666` | ❌ 拒绝 | 外部防护 ✅ |
| Host `127.0.0.1:28666` | ✅ 200 | 本机直连 |
| Host `172.17.0.1:28666` | ✅ 200 | docker0 网桥 |
| 容器 → `172.17.0.1:28666` | ✅ 200 | 默认 gateway |
| 跨网容器 `moapi_default` → `172.17.0.1` | ✅ 200 | 符合需求 |
| `freebuff-net` 容器名 `freebuff2api:8080` | ✅ 200 | 内部 DNS |

**验证命令**：`ss -tlnp | grep 28666`
```
LISTEN  172.17.0.1:28666  (docker-proxy)
LISTEN  127.0.0.1:28666   (docker-proxy)
```
0.0.0.0 未监听，公网端口扫描无结果。

---

## 3. 性能数据（US · FREE mode）

### 同 prompt 对比：US FREE vs CN normal

| 测试用例 | 模型 | Stream | CN TTFT | **US TTFT** | CN 总耗时 | **US 总耗时** | 改善 |
|---|---|---|---|---|---|---|---|
| 短 prompt | gemini-2.5-flash | ✅ | 2310 ms | **1633 ms** | 3151 ms | **1979 ms** | -29% / -37% |
| 短 prompt | claude-sonnet-4 | ✅ | 3188 ms | **1950 ms** | 5118 ms | **2424 ms** | -39% / -53% |
| 中 prompt | gemini-2.5-flash | ✅ | 2209 ms | **1381 ms** | 2528 ms | **2205 ms** | -37% / -13% |

### US 节点独立测试

| 用例 | 模型 | Stream | TTFT | 总耗时 |
|---|---|---|---|---|
| NS-short | gemini-2.5-flash | ❌ | — | 1845 ms |
| NS-short | claude-sonnet-4 | ❌ | — | 2768 ms |
| ST-short | gemini-2.5-flash | ✅ | 1633 ms | 1979 ms |
| ST-short | claude-sonnet-4 | ✅ | 1950 ms | 2424 ms |
| ST-short | z-ai/glm-5.1 | ✅ | — | 2528 ms |
| ST-medium | gemini-2.5-flash | ✅ | 1381 ms | 2205 ms |
| ST-medium | claude-sonnet-4 | ✅ | 4752 ms | 7652 ms |

> gemini-2.5-flash 在 US 节点的 TTFT 稳定在 **1.3 ~ 1.6 秒**，已具备可生产使用的响应体感。

---

## 4. 运维信息

### 部署命令
```bash
ssh remote3
cd ~/m/freebuff2api
docker compose up -d --build
```

### 日志查看
```bash
docker logs freebuff2api --follow --tail 100
```

### 健康检查
```bash
curl http://127.0.0.1:28666/health    # 宿主机
curl http://172.17.0.1:28666/health   # 任意容器
```

### 配置文件
- `/root/m/freebuff2api/.env` — 环境变量（含 API key）
- `/root/m/freebuff2api/docker-compose.yml` — 服务定义
- Docker 网络: `freebuff-net`

### 资源占用
- 镜像大小: ~15 MB (alpine + binary)
- 运行时内存: ~12 MB RSS
- CPU 几乎为 0（无请求时）

---

## 5. 结论

1. **FREE 模式已在 US 节点完全打通**，Gemini/Sonnet/GLM 系列均可调用，无 credit 消耗
2. **访问控制达标**：公网阻断、所有本机/容器场景放行
3. **性能显著优于 CN 节点**：TTFT 降低 30-50%，gemini-flash 可达 1.3s 首字
4. **零外部依赖、零 CGO**，镜像仅 15MB

下一步建议：
- 在 Claude Code 中直接使用 `http://172.17.0.1:28666/v1` 作为 OpenAI endpoint
- 根据 TODO.md 的 P0 项做 runId 池化，进一步把 TTFT 压到 1s 以下
