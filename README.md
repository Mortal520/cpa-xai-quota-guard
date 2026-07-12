# cpa-xai-quota-guard

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![Version](https://img.shields.io/badge/version-0.2.23-blue.svg)](./CHANGELOG.md)
[![CI](https://github.com/Mortal520/cpa-xai-quota-guard/actions/workflows/build.yml/badge.svg)](https://github.com/Mortal520/cpa-xai-quota-guard/actions/workflows/build.yml)
[![Release](https://img.shields.io/github/v/release/Mortal520/cpa-xai-quota-guard?include_prereleases)](https://github.com/Mortal520/cpa-xai-quota-guard/releases)

CLIProxyAPI **原生 Go 插件**（当前版本 **0.2.23**）：仅针对 **xAI** 登录凭证做额度/死号管控、主动巡查、管理 UI 与用量统计。


## 界面预览

管理页示意（矢量占位；可用真实脱敏 PNG 替换，见 [docs/screenshots/README.md](./docs/screenshots/README.md)）：

| 状态栏 / 额度 | 主动巡查 | 账号状态 |
|---------------|----------|----------|
| ![状态栏](./docs/screenshots/dashboard.svg) | ![巡查](./docs/screenshots/patrol.svg) | ![账号](./docs/screenshots/accounts.svg) |

> 截图请脱敏：勿包含 management key、完整 token、未遮蔽邮箱、代理账号密码。

## 做什么

1. 监听 `usage.handle`（成功计真实 token；失败按白名单规则处理）
2. **仅** `provider=xai`（其它 provider 全部忽略）
3. **HTTP 429 + free-usage-exhausted（rolling 24h）** → `plugin_auto` 临时禁用，到期自动恢复
4. **HTTP 402 + spending-limit** → `plugin_auto` 冷却（signal=`spending_limit`），**不删除**；巡查探测恢复后自动启用
5. **403 真权限 / 401 凭证失效** → **DELETE** 凭证
6. **403 区域/模型不可用、426 CLI 版本、404/5xx/网络** → **不删**（记日志/异常分桶）
7. 状态标签持久化：`plugin_auto` / `user_manual`；用户手动禁用永不自动启用
8. ticker 仅恢复本插件自动禁用的账号
9. **主动巡查**：全量扫**启用中** xAI；**仅复核**扫 plugin_auto 冷却号；网络失败有限重试
10. 管理页：日额度池状态栏、巡查配置+操作合并卡、处理日志、账号表分页
11. 账号套餐分类 Free/Super/Heavy（吸收 grok-panel 启发式，见 docs/THIRD_PARTY.md）

## 明确不做

- 不处理 Codex / OpenAI / Gemini / NVIDIA 等其它 provider
- 不处理模糊业务错误、封禁不确定场景（宁可不动作）
- **不照搬** Codex 的 `usage_limit_reached` / `x-codex-*` 窗口逻辑
- 时间解析失败时 **不禁用**（记日志，静默跳过）
- 普通已禁用凭证不进入全量巡查（冷却复核走独立入口）

## 错误处理矩阵（权威）

| 信号 | 被动 HandleUsage | 主动/定时巡查 |
|------|------------------|---------------|
| 429 free-usage | plugin_auto 冷却 + tick 恢复 | 启用号→冷却；spending 冷却号→可恢复 |
| 429 无识别信号 | 跳过 | error，不删 |
| 402 spending-limit | plugin_auto 冷却（不删） | 冷却；可选自动换模型再测 |
| 403 chat endpoint / 真权限 | DELETE | DELETE |
| 403 region / model unavailable | **不删** | **不删**（region_block） |
| 401 invalid/expired | DELETE | DELETE |
| 426 CLI version | n/a | **不删**（cli_version） |
| 404/405/422/5xx 探测 | n/a | error 分桶，不删 |
| 网络超时/DNS/TLS/连接 | n/a | 同模型最多 3 次重试后分桶，不删 |
| 200 | 记 usage token | alive；spending 冷却可 reenable |

## 状态栏额度口径（0.2.18+）

| 指标 | 口径 |
|------|------|
| **日额度池(估)** | 当前**启用** xAI 数 × 1M（rolling 24h）；**禁用不算容量** |
| **已用 · 日历今日/累计** | 仅 `usage.handle` 真实 token（不用 free-usage actual 抬高） |
| **滚动快照 used/limit** | 仍存活凭证的 free-usage 观测；已删号快照剔除 |
| **进度条** | 今日已用 / 日额度池 |

`include_unobserved_quota_est=true`（默认）时日池=启用×1M；`false` 时仅已观测 limit 合计。

## 配置

`plugins.configs.cpa-xai-quota-guard` 示例（**勿提交真实 key**）：

```yaml
plugins:
  configs:
    cpa-xai-quota-guard:
      enabled: true
      quota_guard_enabled: true
      tick_seconds: 30
      max_reset_seconds: 86400
      min_reset_seconds: 0
      management_url: "http://127.0.0.1:8317"
      management_key: "<CPA_MANAGEMENT_KEY>"
      state_path: "data/cpa-xai-quota-guard-state.json"
      include_unobserved_quota_est: true
      cpamp_url: "http://<CPAMP_HOST>:<PORT>"
      cpamp_admin_key: "<PLUS_ADMIN_KEY>"
      webhook_url: ""
      patrol_enabled: false
      patrol_interval: 3600          # 秒（UI 以分钟编辑）
      patrol_timeout: 15
      patrol_batch_size: 0           # 0=不限
      patrol_auth_dir: "/root/.cli-proxy-api"
      patrol_proxy_url: ""
      patrol_concurrency: 8
      patrol_model: "grok-4.5"       # 默认主探测模型
      patrol_auto_model_switch: false
```

| 字段 | 默认 | 说明 |
|------|------|------|
| `enabled` | — | CPA 是否加载本插件 |
| `quota_guard_enabled` | 跟随 `enabled` | 功能开关；UI 切换写此字段，保持 host `enabled=true` |
| `tick_seconds` | `15` | 恢复扫描周期 |
| `max_reset_seconds` | `86400` | 重置等待上限 |
| `min_reset_seconds` | `0` | 最小冷却地板 |
| `management_url` / `management_key` | 空 | CPA 管理 API |
| `state_path` | `data/cpa-xai-quota-guard-state.json` | 持久化 |
| `include_unobserved_quota_est` | `true` | 日池是否用启用×1M 估算 |
| `patrol_enabled` | `false` | 定时巡查 |
| `patrol_interval` | `3600` | 巡查周期（秒） |
| `patrol_timeout` | `15` | 单凭证探测超时 |
| `patrol_auth_dir` | 空 | auth JSON 目录（巡查必填） |
| `patrol_proxy_url` | 空 | 探测代理（建议固定出口） |
| `patrol_concurrency` | `8` | worker 数 |
| `patrol_batch_size` | `0` | 每轮上限，0=不限 |
| `patrol_model` | `grok-4.5` | 主探测模型 |
| `patrol_auto_model_switch` | `false` | 402 时是否自动换模型再测 |

UI 保存配置：`GET+merge+PUT` 写回 CPA，避免部分 PUT 清空兄弟字段。  
功能开关/巡查开关有 runtime 覆盖，避免 Reconfigure 冲掉 UI 状态。

## 管理 API / UI

| 路径 | 说明 |
|------|------|
| `GET .../state?view=focus\|all` | 状态+metrics+patrol+处理日志；默认 focus |
| `GET .../config` | 非敏感配置摘要 |
| `POST .../toggle` | 功能开关 → `quota_guard_enabled` |
| `POST .../run` | 立即扫描恢复 |
| `POST .../patrol` | 全量巡查（仅启用凭证） |
| `POST .../patrol/spending` | 仅复查 plugin_auto 冷却号 |
| `GET .../patrol/status` | 巡查状态 + recent_log |
| `POST .../patrol/stop` | 停止巡查 |
| `POST .../patrol/config` | 保存巡查配置 |
| `GET .../patrol/models` | 探测模型列表 |
| `GET .../deletes` / action 历史 | 处理日志数据源 |
| `POST .../metrics/reset-today` | 清零日历今日已用（需 confirm；不改累计） |
| `GET .../export` | 导出 |
| 菜单 `.../index.html` | 内嵌管理 UI |

## 主动巡查要点

- **全量**：只扫当前启用的 xAI
- **仅复核冷却号**：只扫 plugin_auto 已禁用冷却（含 429/402）
- 探测优先 `POST {base}/responses`，404/405 回退 `/chat/completions`
- 默认 base：`cli-chat-proxy.grok.com/v1`；自动注入 Grok CLI 头（防 426）
- 网络/瞬时 5xx：同模型最多 3 次重试（取消不重试）
- 结果持久化：`last_patrol` + `action_history`（重启可恢复汇总）
- UI 汇总单源：进度计数 + HTTP chips + 异常细分（不再双套动作条）


## 安装 / 升级 / 卸载

详见 **[docs/INSTALL.md](./docs/INSTALL.md)**（商店源、多平台 Release、手动构建、升级卸载与排障）。

### 快速：商店源

```yaml
plugins:
  enabled: true
  dir: "plugins"
  store-sources:
    - "https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/registry.json"
  configs:
    cpa-xai-quota-guard:
      enabled: true
      quota_guard_enabled: true
      management_url: "http://127.0.0.1:8317"
      management_key: "<CPA_MANAGEMENT_KEY>"
```

### 快速：手动放入插件目录

| 宿主 | 路径 |
|------|------|
| Linux amd64 | `plugins/linux/amd64/cpa-xai-quota-guard.so` |
| Linux arm64 | `plugins/linux/arm64/cpa-xai-quota-guard.so` |
| macOS arm64 | `plugins/darwin/arm64/cpa-xai-quota-guard.dylib` |
| Windows amd64 | `plugins/windows/amd64/cpa-xai-quota-guard.dll` |

构建产物与 zip **不要提交进 git**；发版走 GitHub Release（tag `v*` 触发 CI）。

## 构建与部署

本地 Windows 通常无 Go 交叉链；在 Linux 构建机：

```bash
export PATH=$HOME/tools/go1.26/bin:$PATH GOROOT=$HOME/tools/go1.26
export GOPROXY=https://goproxy.cn,direct GOSUMDB=off CGO_ENABLED=1
export CC="$HOME/tools/zig-linux-x86_64-0.14.0/zig cc"
cd ~/src/cpa-xai-quota-guard
# go.mod 需有：replace github.com/router-for-me/CLIProxyAPI/v7 => ./CLIProxyAPI-src
go test ./internal/xaiquota/ -count=1
go build -buildmode=c-shared -o bin/cpa-xai-quota-guard.so .
cp -f bin/cpa-xai-quota-guard.so "/path/to/cliproxyapi/plugins/linux/amd64/"
docker restart cli-proxy-api
```

验证：`state.version == 0.2.23`，日志 `plugin registered ... version=0.2.23`。

## 文档

- [DESIGN.md](./DESIGN.md) — 设计与错误白名单
- [CHANGELOG.md](./CHANGELOG.md) — 版本记录
- [docs/ROADMAP_0.3.md](./docs/ROADMAP_0.3.md) — 0.2→0.3 路线
- [docs/ACCOUNT_LOGIC_AUDIT.md](./docs/ACCOUNT_LOGIC_AUDIT.md) — 账号逻辑审查
- [docs/GAP_BACKLOG.md](./docs/GAP_BACKLOG.md) — 未落地需求 backlog

## 安全

禁止提交 management key、auth 目录、state 导出、代理账号密码。见仓库根 `AGENTS.md` / 项目安全约定。

## 开源协议

本项目以 **[MIT License](./LICENSE)** 发布。

- 你可以自由使用、复制、修改、合并、发布、分发、再许可与销售
- 需保留版权声明与许可证文本
- 软件按「现状」提供，作者不承担任何担保责任

第三方参考与归因见 [docs/THIRD_PARTY.md](./docs/THIRD_PARTY.md)（含 [cpa-plugin-grok-panel](https://github.com/TizenryA/cpa-plugin-grok-panel) 等 MIT 项目的思想吸收说明）。

商店元数据 `registry.json` 中 `license` 字段与本仓库 LICENSE 一致为 MIT。

