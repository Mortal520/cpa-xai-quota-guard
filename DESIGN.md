# cpa-xai-quota-guard 设计文档

> xAI 专用额度/死号管控插件（CLIProxyAPI native Go）  
> 当前实现版本：**0.2.5**（以 `main.go` 中 `pluginVer` 为准）

## 1. 目标

仅针对 **xAI** 登录凭证：

1. 明确的 **短时免费额度用尽（rolling 24h / 429）** → 临时禁用，到期自动恢复  
2. 明确的 **积分/订阅 spending-limit（402）** → 临时禁用（signal=`spending_limit`），与 429 区分；巡查探测可用后启用  
3. 明确的 **死号**（权限拒绝 403 / 凭证失效 401）→ 删除凭证  
4. 用户手动禁用永不自动启用；状态标签持久化  

## 2. 硬性约束

1. 只监控 xAI（`provider`/`auth_type` 规范化后为 `xai`），其它 provider 全部忽略。  
2. 仅捕获白名单错误；网络/`context canceled`/鉴权以外业务码/封禁模糊错误全部跳过。  
3. 429 free-usage：解析或默认滚动 24h 冷却；解析失败 → **不禁用**。  
4. 合法 429 → 临时禁用对应登录文件（`plugin_auto`）。  
5. 重置时间到达 → 自动启用。  
6. 仅恢复本插件自动禁用的文件。  
7. 用户手动禁用永远不会被自动启用。  
8. 状态标签：`plugin_auto` / `user_manual`，持久化到 `state_path`。  
9. 401/403 死号路径 → **DELETE**；402 spending-limit → **plugin_auto 冷却**（非删除）。  

## 3. 与 CPAMP 的对齐

复用 CPAMP 冷却的**所有权模型**，**不复用 Codex 匹配字段**：

| 概念 | 取值 |
|------|------|
| owner | `cpa_xai_quota_plugin` |
| pre_disabled | 禁用前读到的 `disabled` |
| recover_at | 解析或默认的未来重置时间 |
| 恢复条件 | owner 匹配 + 非 pre_disabled + 到期 + `plugin_auto` |

可选：`cpamp_url` + `cpamp_admin_key` 用于今日用量回补地板（不替代 `usage.handle`）。

## 4. xAI 真实机制（不照搬 Codex）

### 4.1 与 Codex 的差异

| 维度 | Codex | xAI（本插件） |
|------|-------|----------------|
| provider | codex | **仅 xai** |
| 主冷却信号 | `usage_limit_reached` / `x-codex-*` | **429 + free-usage-exhausted（rolling 24h）** |
| 死号 | 视实现 | **403 / 401 明确码 → DELETE** |
| 积分/订阅额度 | 部分 usage_limit | **402 spending-limit → 冷却（spending_limit），巡查恢复** |

### 4.2 冷却匹配（429 short-window）

`MatchShortWindowQuota` 全部满足才禁用：

1. `Failed == true`  
2. Provider 为 xAI  
3. `StatusCode == 429`  
4. 明确短时/免费额度信号（见下）  
5. 成功得到 **未来** `recover_at`（含 rolling 24h 默认）  
6. `recover_at - now <= max_reset_seconds`  

### 4.3 真实样例（生产）

**冷却 — HTTP 429：**

```json
{
  "code": "subscription:free-usage-exhausted",
  "error": "You've used all the included free usage for model grok-4.5-build-free for now. Usage resets over a rolling 24-hour window — tokens (actual/limit): 1091108/1000000."
}
```

- 无 `Retry-After` 时：按 **rolling 24h** 默认 recover（受 `max_reset_seconds` / `min_reset_seconds` 约束）  
- 同时解析 `actual/limit` 写入滚动池快照  

**删除 — HTTP 403：**

```json
{"code":"permission-denied","error":"Access to the chat endpoint is denied. ..."}
```

**删除 — HTTP 401：**

```text
Invalid or expired credentials (auth_kind=bearer, ... reason=no auth context)
```

**冷却 — HTTP 402（与 429 区分，signal=`spending_limit`）：**

```json
{"code":"personal-team-blocked:spending-limit","error":"You have run out of credits or need a Grok subscription. Add credits at https://grok.com/?_s=usage or upgrade at https://grok.com/supergrok."}
```

### 402 语义（生产已确认）

1. **不是死号**：凭证仍可能对其它 free 档模型可用；`permission-denied`/401 才删。  
2. **模型相关**：同一账号对 `grok-3` 可能 402，对 `grok-4.5-build-free` 可能 200/429。  
3. **业务 usage**：真实请求返回 402 → 立即 `plugin_auto` 冷却（signal=`spending_limit`），与 429 free-usage 分状态。  
4. **巡查探测**：
   - 主模型 = `patrol_model`（默认 free 档）  
   - `patrol_auto_model_switch=false`（默认）：只测主模型；402 → 冷却  
   - `=true`：主模型 402 后 `GET /models`，再试最多 4 个备用（优先 id 含 `free`）；全部 402 → 冷却；任一 200/429 → 存活/恢复  
5. **恢复**：巡查 200/429 → 启用；tick 到软 recover_at 也可启  
6. **复查**：改 `patrol_model` / 自动换模后，用 scope=`spending_only` 仅扫冷却号  

- **不删除**；`plugin_auto` 软冷却（默认 ~24h 软上限，受 `max_reset_seconds` 约束）  
- 状态可记 `last_probe_model` / 日志 `tried=[...]`  

**忽略 — HTTP 200 流式取消：**

```text
context canceled  + Content-Type: text/event-stream
```

客户端/链路取消，额度头可能仍正常；**不冷却、不删除**。

### 4.4 信号与排除

**冷却信号（429 前提下）：**

- `subscription:free-usage-exhausted` / free usage / free-usage  
- 兼容 OpenAI-like：`rate_limit_exceeded` / TPM·RPM 文案 + 可解析重置  
- Headers：`Retry-After`、`x-ratelimit-reset*`、`X-Should-Retry` 辅助  

**删除信号：**

- 403 + `permission-denied`  
- 401 + invalid/expired credentials / no auth context / invalid_grant revoked  

**冷却信号（402 spending，与 429 独立）：**

- 402 + `personal-team-blocked:spending-limit` / run out of credits / need a Grok subscription  
- 状态 `signal=spending_limit`；巡查候选包含此类 disabled 账号  

**永不处理：**

- `context canceled`、纯网络错误、5xx 模糊失败  
- 非 xAI provider  
- 纯泛 429 且无线索且无法默认 recover  
- Codex 专用字段不得作为主信号  

### 4.5 重置时间解析优先级

1. Header `Retry-After`  
2. Header `x-ratelimit-reset*`  
3. Body `retry_after` / `reset_at` / `resets_at` 等  
4. free-usage **rolling 24-hour window** → 默认 now+24h（截断到 max）  

解析失败且无默认路径 → **不禁用**。

## 5. 状态机

```
active
  └─ 429 free-usage 合法 → auto_disabled (disable_source=plugin_auto)
auto_disabled
  └─ now >= recover_at → 启用 → active（仅 plugin_auto）
user_manual_disabled
  └─ 永不自动启用
dead credential (401/402/403 白名单)
  └─ DELETE auth-files + delete_history
```

## 6. 持久化

默认 `data/cpa-xai-quota-guard-state.json`：

- `accounts[auth_index]`：状态机字段 + reason/signal/recover_at  
- usage/metrics：今日/累计 token、请求计数、滚动 actual/limit 快照  
- `delete_history`：最近删除记录（UI 展示）  

## 7. 集成

| 钩子 | 用途 |
|------|------|
| `usage.handle` | 成功计 token；失败匹配冷却/删除 |
| ticker | 扫描 due cooldown 并 enable |
| management List/PATCH/DELETE | inventory 与账号操作 |
| management 路由 | state/config/toggle/run/inject/export/backfill/health |
| resource `index.html` | 内嵌管理 UI |

禁用/启用：`PATCH /v0/management/auth-files/status`  
删除：`DELETE /v0/management/auth-files?name=...`

## 8. 管理 state 性能设计

### 8.1 问题

全量 xAI auth-files 可达 **5k–6k**。对每账号反复读 usage 文件或序列化全表会导致管理 iframe **卡在加载中**。

### 8.2 策略（0.1.24–0.1.27）

1. **`UsageAndQuotaMaps()`**：单次读 usage store  
2. **`view=focus`（默认）**：只返回可操作/今日热账号；inventory 全量只计数  
3. **`focusHotCap=80`**：今日 hot 行硬截断  
4. **auth-files List 缓存**（TTL ~12s）  
5. **失败 sticky**：List 失败或空响应时回落上次成功 inventory（最长 ~10min），并暴露 `inventory.stale`  
6. **UI `LAST_GOOD_XAI`**：状态栏凭证数不因瞬时 0 覆盖上次非零  

### 8.3 state 关键字段

```json
{
  "version": "0.1.27",
  "view": "focus",
  "accounts": [],
  "summary": {
    "returned": 0,
    "tracked": 0,
    "auto_disabled": 0,
    "hot_total": 0,
    "hot_shown": 0,
    "hot_hidden": 0,
    "focus_hot_cap": 80
  },
  "metrics": { "xai_total": 0, "used_today": 0, "quota_total_est": 0 },
  "inventory": {
    "ok": true,
    "stale": false,
    "error": "",
    "age_ms": 0,
    "xai_total": 0
  }
}
```

## 9. 配置

见 README 配置表。敏感字段：`management_key`、`cpamp_admin_key`。

## 10. 假设与局限

1. free-usage 滚动窗口以 xAI 文案与生产样例为准；无 `Retry-After` 时用 24h 默认。  
2. 402 与 429 free-usage **不得混淆**：均为冷却，但 **signal/Reason/恢复路径不同**（429 到点 tick；402 以巡查探测为主、tick 软上限为辅）。  
3. 凭证数依赖 management `auth-files`；主机繁忙时可能瞬时失败 → sticky 掩盖，非实时强一致。  
4. `include_unobserved_quota_est=true` 时总额度为 **估**，未观测账号按默认 1M 计。  
5. 今日已用依赖 CPA 是否在 usage 事件中带 token Detail；缺 Detail 时可能偏低（可用 CPAMP 回补地板）。  
6. 失败但从未成功的账号默认保留，不自动清池。  

## 11. 演进记录（摘要）

| 日期/版本 | 变更 |
|-----------|------|
| 初版 | 通用 429 rate-limit + 所有权模型 |
| 2026-07-11 | 生产 429 free-usage / 403 DELETE；ManagementResponse UI |
| 0.1.22 | 401 invalid credentials DELETE |
| 0.1.23 | 402 spending-limit DELETE（历史；0.2.3 起改为冷却） |
| 0.2.3 | 402 spending-limit → plugin_auto 冷却 + 巡查恢复；与 429 区分 |
| 0.1.24–0.1.26 | focus 视图与 UI 性能 |
| **0.1.27** | inventory sticky，凭证数不闪 0 |

## 12. 安全

- 仓库与 PR 禁止真实 key/token/Cookie  
- 探针脚本若读取主机 `config.yaml` secret，仅限私有环境，**不得提交**  
- commit 前扫描 staged diff  
## 主动巡查 (Patrol)

- 目标：全量探测**当前启用**的 xAI + **spending_limit 冷却号**；删除不可恢复死号（403/401）；402 冷却/恢复。
- 不巡查 `disabled=true` 的文件；不加 failed/success 筛选。
- 实现：worker pool + 直读 auth 文件 token + 可选代理；结果写入 `delete_history`（reason 前缀 `patrol:`）。
- 调度：`tickerLoop` 在 `patrol_enabled && patrol_auth_dir!=""` 时按 `patrol_interval` 触发。
- 状态同步：删除后 `Store.Remove`；`state` 构建时 prune 不在 CPA inventory 的 tracked 记录。
- 配置写回：UI 改动经 CPA management plugin config 持久化；功能开关字段为 `quota_guard_enabled`。


## 巡查探测模型 (0.2.4)

- 配置项 patrol_model，默认 grok-4.5-build-free。
- 禁止默认使用无免费额度的付费模型（如硬编码 grok-3），否则会全员 402 spending-limit 误伤。
- UI 从凭证 GET /models + 建议列表选择；GET .../patrol/models。
