# cpa-xai-quota-guard

CLIProxyAPI **原生 Go 插件**（当前版本 **0.2.4**）：仅针对 **xAI** 登录凭证做额度/死号管控、主动巡查，并提供管理 UI 与用量统计。

## 做什么

1. 监听 `usage.handle`（成功计用量；失败按规则处理）
2. **仅** `provider=xai`
3. **HTTP 429 + `subscription:free-usage-exhausted`（滚动 24h）** → 临时禁用（`plugin_auto`），到期自动恢复
4. **403 permission-denied / 401 invalid credentials** → **DELETE** 凭证（不可恢复）
5. **402 + personal-team-blocked:spending-limit** → `plugin_auto` 冷却禁用（signal=`spending_limit`），**与 429 free-usage 区分**；巡查探测恢复后自动启用
6. 状态标签持久化：`plugin_auto` / `user_manual`
7. ticker 到期后**只恢复**本插件自动禁用的账号（含 spending 软上限）
8. 用户手动禁用永不自动启用
9. **主动巡查 (Patrol)**：全量探测**已启用** xAI + **spending_limit 冷却号**；403/401 删除，402 冷却/延长，200/429 恢复 spending 号
10. 管理页：状态栏、巡查配置+操作合并卡片、删除历史、账号表分页

## 明确不做

- 不处理 Codex / OpenAI / Gemini / NVIDIA 等其它 provider
- 不处理网络错误、`context canceled`、HTTP 200 流式中断、5xx 等非业务额度错误
- **不照搬** Codex 的 `usage_limit_reached` / `x-codex-*` 窗口逻辑
- 时间解析失败时 **不禁用**（记日志，静默跳过）
- 普通已禁用凭证**不巡查**；例外：plugin_auto + signal=spending_limit 必须再探测以恢复
- 巡查不加 `failed>0 && success==0` 筛选（全量启用凭证）

## 错误处理矩阵

| 场景 | 条件（摘要） | 动作 |
|------|----------------|------|
| 免费额度用尽 | 429 + free-usage-exhausted / rolling 24h | `plugin_auto` 冷却，默认约 24h 内到点恢复 |
| 权限拒绝 | 403 + permission-denied | DELETE auth-files |
| 凭证失效 | 401 + invalid/expired / no auth context 等 | DELETE |
| 订阅/积分耗尽 | 402 + spending-limit / run out of credits | `plugin_auto` 冷却（signal=`spending_limit`）；巡查探测恢复后启用 |
| 客户端取消 | 200 SSE + `context canceled` | **忽略** |
| 其它 4xx/5xx/网络 | — | **忽略** |

详情与字段白名单见 [DESIGN.md](./DESIGN.md)。

## 配置

`plugins.configs.cpa-xai-quota-guard` 示例（**勿提交真实 key**）：

```yaml
plugins:
  configs:
    cpa-xai-quota-guard:
      enabled: true                 # CPA 主机加载开关（卸载插件用；UI 功能开关见下）
      quota_guard_enabled: true     # 功能开关（UI 切换写此字段；缺省回退 enabled）
      tick_seconds: 30
      max_reset_seconds: 86400
      min_reset_seconds: 0
      management_url: "http://127.0.0.1:8317"
      management_key: "<CPA_MANAGEMENT_KEY>"
      state_path: "data/cpa-xai-quota-guard-state.json"
      include_unobserved_quota_est: true
      cpamp_url: "http://<CPAMP_HOST>:<PORT>"   # 可选，回补用量
      cpamp_admin_key: "<PLUS_ADMIN_KEY>"       # 可选
      webhook_url: ""                           # 可选
      # 主动巡查
      patrol_enabled: true
      patrol_interval: 7200          # 秒
      patrol_timeout: 10
      patrol_batch_size: 0           # 0=不限
      patrol_auth_dir: "/root/.cli-proxy-api"
      patrol_proxy_url: ""           # 可选 socks5://...
      patrol_concurrency: 16
      patrol_model: "grok-4.5-build-free"  # 探测模型；勿用无免费额度的付费模型
```

| 字段 | 默认 | 说明 |
|------|------|------|
| `enabled` | `false` | CPA 是否加载本插件 |
| `quota_guard_enabled` | 跟随 `enabled` | **功能开关**；UI 切换写入此字段，并保持 host `enabled=true` |
| `tick_seconds` | `15` | 恢复扫描周期 |
| `max_reset_seconds` | `86400` | 重置等待上限 |
| `min_reset_seconds` | `0` | 最小冷却地板 |
| `management_url` / `management_key` | 空 | CPA 管理 API |
| `state_path` | `data/cpa-xai-quota-guard-state.json` | 持久化状态 |
| `include_unobserved_quota_est` | `true` | 总额度是否含未观测账号×默认 1M（上限=凭证数×1M） |
| `patrol_enabled` | `false` | 定时巡查 |
| `patrol_interval` | `3600` | 巡查周期（秒） |
| `patrol_timeout` | `15` | 单凭证探测超时 |
| `patrol_auth_dir` | 空 | auth JSON 目录（必填才可巡查） |
| `patrol_proxy_url` | 空 | 探测代理 |
| `patrol_concurrency` | `8` | worker 数 |
| `patrol_batch_size` | `0` | 每轮上限，0=不限 |
| `patrol_model` | `grok-4.5-build-free` | 巡查探测模型（建议免费档） |

未配置 management 时只记日志，不操作账号。  
UI 保存配置通过 `GET+merge+PUT` 写回 CPA `plugins/<id>/config`，避免部分 PUT 清空兄弟字段。

## 管理 API / UI

| 路径 | 说明 |
|------|------|
| `GET .../state?view=focus\|all` | 状态；默认 focus；会 prune 已从 CPA 删除的幽灵账号 |
| `GET .../config` | 非敏感配置摘要（含 patrol 字段） |
| `POST .../toggle` | 功能开关 → 写 `quota_guard_enabled` |
| `POST .../run` | 立即扫描恢复 |
| `POST .../patrol` | 启动主动巡查 |
| `GET .../patrol/status` | 巡查状态 + 删除历史 |
| `POST .../patrol/stop` | 停止巡查 |
| `POST .../patrol/config` | 保存巡查配置（写回 CPA） |
| `GET .../deletes` | 删除历史 |
| `GET .../export` | 导出 |
| 菜单 `.../index.html` | 内嵌管理 UI |

## 主动巡查

- 只扫 **disabled=false** 的 xAI 凭证
- 直读 `patrol_auth_dir` 下 auth JSON 的 `access_token`，经可选代理打上游最小请求
- 探测模型由 `patrol_model` 控制（默认免费档 `grok-4.5-build-free`）；UI 可从凭证 `/models` 列表选择
- 200/429/其它非死信号 → 存活；403/401/402 → DELETE + 写删除日志
- 冷却中的 `plugin_auto` 账号跳过探测
- ticker 按 `patrol_interval` 定时触发；UI 可手动启动/停止

## 构建与部署

```bash
# 依赖 CLIProxyAPI SDK（本地 replace 示例）
# replace github.com/router-for-me/CLIProxyAPI/v7 => ./CLIProxyAPI-src

export CGO_ENABLED=1
go build -buildmode=c-shared -o bin/cpa-xai-quota-guard.so .
cp bin/cpa-xai-quota-guard.so <CPA>/plugins/linux/amd64/
# 重启 CPA / docker restart cli-proxy-api
```

Windows 本地通常只改源码；Linux amd64 `.so` 在目标机交叉或本机构建。

## 安全

- 禁止提交 management_key / cpamp_admin_key / token / auth JSON
- 仓库已 ignore `_*.py` 本地探针与 patch 脚本、`bin/`、`CLIProxyAPI-src/`、`data/`

## 文档

- [DESIGN.md](./DESIGN.md) — 规则与匹配细节
- [CHANGELOG.md](./CHANGELOG.md) — 版本记录
