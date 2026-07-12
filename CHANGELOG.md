# Changelog

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