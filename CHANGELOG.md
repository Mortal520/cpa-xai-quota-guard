# Changelog

## 0.2.4

- 巡查探测模型可配置：`patrol_model`（默认 `grok-4.5-build-free`）
- 修复硬编码 `grok-3` 导致正常账号被误判 spending-limit/删除
- 新增 `GET .../patrol/models`：用启用凭证拉 `/models` + 建议列表；UI 下拉选择
- 探测日志 reason 带 model= 便于排查

## 0.2.3

- 402 spending-limit: 改为 plugin_auto 禁用（signal=`spending_limit`），不再 DELETE
- 与 429 free-usage 区分：不同 Match 路径、Reason/Signal 不同
- 巡查会纳入 spending_limit 冷却账号并探测；200/429 视为可恢复并自动启用
- 403/401 仍删除；IsSpendingLimit 仅接受 HTTP 402/0
## 0.2.1

- 配置写回：UI 开关/巡查配置 `GET+merge+PUT` 持久化到 CPA plugin config
- 功能开关 `quota_guard_enabled`：避免写 host `enabled=false` 导致插件卸载与路由 404
- 账号状态同步：删除凭证后 `Store.Remove` + `PruneMissingInventory`，消除幽灵条目
- 缓存：成功空 inventory 视为真实 0；`invalidateAuthListCache` 清零衍生指标
- 总额度：`quota_total_est` 上限=凭证数×默认额度；`xai_total=0` 时显示 0
- 巡查 UI：配置与操作合并单卡片；删除历史指纹渲染防抖
- 移除注入测试卡片与死代码 `injectResponse`
- 定时巡查日志：触发时打 info

## 0.2.0

- 主动巡查(Patrol)：全量探测所有启用的xAI凭证，自动删除403/401/402死号
- 直接读取auth file JSON提取access_token，绕过CPA round-robin直接probe上游
- 已禁用凭证不巡查
- 不加任何筛选条件(failed>0等)，全量巡查所有启用凭证
- patrol配置字段：patrol_enabled/patrol_interval/patrol_timeout/patrol_batch_size/patrol_auth_dir
- tickerLoop集成定时巡查调度
- Patrol UI：进度条、实时日志、存活/删除/错误计数
- API路由：patrol(启动)/patrol/status(状态)/patrol/stop(停止)
- 删除日志记录patrol来源标识

## 0.1.27

- auth-files List：TTL 缓存 + 失败/空响应 sticky 回落
- state 增加 `inventory{ok,stale,error,age_ms,xai_total}`
- UI 状态栏 `LAST_GOOD_XAI`，避免刷新闪 0

## 0.1.26

- focus 仅今日活跃 + hot cap 80
- loadState 防并发；失败清除「加载中」
- auth-files List 短缓存

## 0.1.25

- `UsageAndQuotaMaps` 单次读 usage/quota
- focus 少物化（仍可能因历史用量偏大）

## 0.1.24

- `state?view=focus|all` 引入

## 0.1.23

- HTTP 402 spending-limit → DELETE

## 0.1.22

- HTTP 401 invalid/expired credentials → DELETE

## 0.1.20 – 0.1.21

- 状态栏：凭证/额度/已用；去 CPAMP 打开按钮

## 更早

- 429 free-usage rolling 24h 冷却
- 403 permission-denied DELETE
- plugin_auto / user_manual 所有权
- 内嵌 management UI