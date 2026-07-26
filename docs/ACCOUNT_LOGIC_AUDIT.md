# 账号处理逻辑审查（0.3.32）

## 两条入口

| 入口 | 触发 | 作用 |
|------|------|------|
| `HandleUsage` | CPA 真实请求 usage 事件 | 运行中额度/死号处置 |
| `PatrolSweep` | 主动/定时巡查探测 | 主动筛死号 + 402 恢复 + 429 冷却 |

两者共用 `match.go` 分类器，但处置分支不完全对称（0.2.9 已对齐 429）。

## 权威错误矩阵（实现核对）

| 信号 | HandleUsage | Patrol |
|------|-------------|--------|
| 429 free-usage（MatchShortWindowQuota） | plugin_auto 冷却 + Tick 恢复 | **0.2.9：plugin_auto 冷却**；spending 冷却号上的 429 → 恢复启用 |
| 429 无识别信号 | 跳过 + 日志 | error，不删不存活 |
| 402 spending-limit | plugin_auto 冷却（signal=spending_limit） | 同上；候选含 spending 冷却号 |
| 403 region | 不删，warn | error，不删 |
| 403 chat endpoint / 真 permission | DELETE | DELETE |
| 401 invalid/expired | DELETE | DELETE |
| 404/405/422/5xx 探测 | n/a | error |
| 网络超时/代理失败 | n/a | error |
| 200，低于阈值 | 记用量 | alive；插件冷却号则 reenable |
| 200，达到额度阈值 | plugin_auto 冷却 24h | plugin_auto 冷却/保持冷却，不重置时钟 |

## 所有权与手动禁用

- `Owner=cpa_xai_quota_plugin` + `disable_source=plugin_auto` + `!PreDisabled` 才允许自动启用（Tick / 巡查 reenable）。
- 已禁用且无本插件所有权 → 标 `user_manual`，永不自动启用。
- `SetDisabled` 竞态 prev=true → user_manual。

## 巡查候选

- **all**：仅启用中的 xAI，供手动全量巡查。
- **spending_only**：仅本插件拥有的已禁用冷却号；历史名称保留，实际覆盖 429/402/threshold。
- **scheduled**：启用号 + 本插件冷却号，冷却号优先；用户手动停用不进入候选。
- `patrol_recheck_cooldowns=false` 时定时巡查退回 all。

## 已确认的实现缺口 / 风险

### P0（0.2.9 已修）
- 巡查遇 429 曾只标 `alive`，**未冷却**启用账号（与 HandleUsage 不一致）。现已对齐。

### P1（仍在）
1. **探测依赖代理/出口**：`patrol_proxy_url` 坏或直连超时 → 全 error，易误判“全挂”。
2. **默认/错误模型** 可导致 region 或假 402（模型选择责任在用户；自动换模默认关）。
3. **region 历史删除日志** 仍显示旧误删，非新行为。
4. **probe 循环 region 早期 return** 与末尾 region 分支重复（无害，可读性差）。

### P2
1. free-usage 与 spending 都用 `StateAutoDisabled`，仅靠 `Signal` 区分；UI 需明确展示 signal。
2. soft 冷却墙钟 24h 与真实滚动恢复可能不一致（定时/手动复查可提前恢复）。
3. `recordProbeResult` 默认分支（0.2.19）：未知 action **计 error**，不再计 alive。
4. 表单 dirty 与配置 Reconfigure 仍可能偶发覆盖（0.2.8 已缓解）。

## 不变量（测试覆盖）

- `TestGuardDisableAndRecoverPluginAuto`
- `TestGuardNeverRecoversUserManual`
- `TestGuardDeletesPermissionDenied` / `TestGuardDeletesInvalidCredentials401`
- `TestGuardCooldownsSpendingLimit402`
- `TestRegionPermissionDeniedNotDead`
- `TestMatchGrokFreeUsageExhausted24h`
- `TestMatchSpendingLimitQuotaDistinctFrom429`

- `TestProbeOneCredentialCoolsOnSuccessfulThresholdHeaders`
- `TestProbeOneCredentialReenablesRecoveredPluginCooldown`
- `TestProbeOneCredentialKeepsCooldownWhenStillAboveThreshold`
- `TestSelectPatrolCandidatesScheduledIncludesEnabledAndPluginCooldownOnly`

## 结论

- **429 不删除**：HandleUsage 与 Patrol 均成立。
- **巡查 429 的正确动作是冷却（启用号）或恢复（spending 冷却号）**，不是删除，也不是简单 alive。
- usage 与巡查的 429/阈值/恢复边界已对齐；剩余主要风险来自探测模型、代理出口与上游业务语义变化。

## 0.2.19 增补

- 网络：timeout/cancel/DNS/TLS/connect 分桶 + 同模型最多 3 次重试（取消不重试）。
- 426/region/5xx/422 有独立 action，均不删除。
- 巡查 UI 汇总与 by_http 同源；去掉第二套动作 chips。
