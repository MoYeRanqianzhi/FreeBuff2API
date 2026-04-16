# YAML 配置 + fsnotify 热加载

**引入版本**: v0.5.0（env 兼容） / v0.5.1（纯 YAML，移除 env 兼容）
**日期**: 2026-04-17
**参考**: `github.com/router-for-me/CLIProxyAPI`（YAML + fsnotify + reloadCallback 设计模式）

## 目的

将原先散落在 env 变量里的所有配置（upstream / cost mode / default model / breaker / 客户端鉴权 / auths_dir / watch_interval 等）集中到一个 `config.yaml`，并通过 **fsnotify + 轮询兜底** 实现秒级热加载——改配置保存文件即生效，无需重启服务。

## 文件布局

```
config.yaml            ← 实际配置（.gitignore 忽略，含敏感 key）
config.example.yaml    ← 模板（入库）
auths/                 ← codebuff credentials.json 多账号目录（独立管理）
  alice.json
  bob.json
```

## 配置结构

```yaml
server:
  listen: ":8080"
  api_key: ""                # 客户端访问需要的 Bearer（留空 = 关闭客户端鉴权）

upstream:
  base_url: "https://www.codebuff.com"
  cost_mode: "free"          # "free" | "normal"
  default_model: "anthropic/claude-sonnet-4"

auth:
  api_keys: []               # 内联 FreeBuff authToken 列表
  dir: "auths"               # credentials.json 目录
  watch_interval: 15s        # 兜底轮询间隔（fsnotify 优先）
  breaker:
    threshold: 3             # 连续失败次数触发熔断
    cooldown: 12h            # 熔断冷却时长

logging:
  level: info                # debug/info/warn/error
```

## 启动 & 配置路径

```bash
./freebuff2api                          # 默认使用 ./config.yaml
./freebuff2api -config /etc/fb.yaml     # 显式指定
```
配置文件不存在或解析失败时，启动直接 fatal（不再回退到 env）。

Docker：`docker-compose.yml` 把 `./config.yaml:/app/config.yaml` 挂载进去，容器内通过 `["-config","/app/config.yaml"]` 启动。

## 热加载实现

### Reloader (`authloader.go`)
- `Current() *Config` — 运行期所有组件（middleware / proxy）通过这个读取"当前"配置
- `Reload(reason)` — 重新读 YAML + 扫描 `auths/`，更新 KeyPool（保留熔断状态）+ 替换 `current` 快照

### Watcher (`authloader.go`)
fsnotify 监听 **配置文件父目录** 与 **auths/ 目录**（监听目录比监听单个文件更可靠，因编辑器常用 rename-on-save），收到事件后：
1. 过滤无关文件（隐藏文件 / swap / ~ 后缀）
2. 判定是否匹配 `config.yaml` 路径或 `auths/*.json`
3. **200ms 防抖**合并连续事件（一次原子保存常产生 3-4 个 event）
4. 触发 `Reloader.Reload`

同时还启动一个 `watch_interval` 轮询（默认 15s），对比 name+size+mtime 签名兜底——网络挂载 / 某些 Docker 卷驱动 fsnotify 会漏事件。

### 保留的运行态状态
- **熔断**：survivor key（同 token）保留 `Fails/Broken/BrokenUntil`
- **请求计数器**：`KeyPool.counter` 持续累加，reload 不影响轮询顺序

## 唯一配置来源

`config.yaml` 是**唯一**配置来源（v0.5.1 起），**不再**读取任何环境变量。所有运行时参数必须在 YAML 里写明；缺失走内置默认值。这样避免了 env / YAML 两套事实来源混淆的运维陷阱——看文件即看生效值。

## 各模块如何读取实时配置

| 模块 | 旧做法 | 新做法 |
|---|---|---|
| `ProxyHandler` | `p.cfg.DefaultModel` | `p.reloader.Current().Upstream.DefaultModel` |
| `authGuard` middleware | `cfg.APIKey` | `reloader.Current().Server.APIKey` |
| `KeyPool breaker` | 常量 | `SetBreakerTuning` 在每次 reload 时调用 |

**中间件 / Handler 不缓存 `*Config`**；每请求调一次 `Current()`（读锁，纳秒级），确保改 YAML 立即生效。

## 测试覆盖

- `TestLoadConfigDefaults` — YAML 省略字段走默认值
- `TestLoadConfigFullYAML` — 全字段解析、trailing slash 清理、内联 key 去重
- `TestLoadConfigMissingFileFails` — 文件缺失 → 启动失败
- `TestLoadConfigEmptyPathFails` — 空路径 → 启动失败
- `TestLoadConfigValidateCostMode` — 非法 cost_mode 启动失败
- `TestReloaderPickupConfigChange` — `Reload()` 后 model + pool 同步变化
- `TestReloaderKeepsBreakerStateAcrossReload` — 熔断状态跨 reload 保留
- `TestWatcherDetectsConfigEdit` — fsnotify 真实 watch → edit → reload 端到端（≤2s）

## 已知 & 后续

- [ ] 配置 reload 失败时的告警（目前仅 log.Printf，可接入 Prometheus）
- [ ] 支持通过 HTTP `/v0/management/*` 端点远程推送配置（CLIProxyAPI 有，后续可参照）
- [ ] 按请求 per-auth 限速 / metrics
- [ ] `logging.level` 还只影响启动日志，请求级 log level 后续补齐
