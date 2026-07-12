# 未落地需求 Backlog（相对历史任务与用户反馈）

> 审查日期：2026-07-13 · 对照线上 0.2.19  
> 状态：`done` / `partial` / `open` / `wontfix`

## 已完成（近期）

| 移除立即扫描恢复按钮 | done | 0.2.21 |
| 处理日志筛选 + 账号搜索增强 | done | 0.2.21 |

| 项 | 状态 | 版本 |
|----|------|------|
| 429 free-usage 冷却（被动+巡查） | done | 0.2.9+ |
| 402 spending 冷却不删 + 巡查恢复 | done | 0.2.x |
| region 403 不删 | done | 0.2.6+ |
| 426 不删 + CLI 头 | done | 0.2.13 |
| 定时巡查开关/配置 + 运行时不被 Reconfigure 冲掉 | done | 0.2.x |
| 全量只扫启用 / 复核只扫冷却 | done | 0.2.16 |
| 删除历史+被动日志合并 | done | 0.2.15 |
| last_patrol / action_history 持久化 | done | 0.2.17 |
| 日额度池=启用×1M；已用=真实 token | done | 0.2.18 |
| 巡查异常分桶 + 网络重试 + 汇总单源 | done | 0.2.19 |
| 代理/模型表单不被 15s 轮询回写 | done | 0.2.10–0.2.11 |
| 账号表固定高度+分页 | done | 0.2.12–0.2.14 |

## 部分完成 / 需回填

| 项 | 状态 | 说明 | 建议版本 |
|----|------|------|----------|
| 账号状态展示 signal | done | 独立信号列 + 筛选 | 0.2.20 |
| 仅复核冷却号覆盖面 | done | 文案/API 说明与实现一致 | 0.2.20 |
| 巡查结果双汇总 | partial→done | 0.2.19 去掉动作条重复；需用户实测确认 | 验证 |
| 日志持久化 | partial | delete/action/last_patrol 有；recent_log 截断 500 | 可接受 |
| used_today 历史污染 | done | 提供可选「清零今日已用」校准（不自动、不改累计） | 0.2.22 |
| 探测代理会话复用 | partial | 有 patrolHTTP 缓存；代理变更 Invalidate | 观察 |
| 定时巡查重启即触发 | done | patrol_initial_delay_sec 默认 60 | 0.2.20 |

## 明确未做 / 延后

| 项 | 状态 | 原因 |
|----|------|------|
| 失败>0 且 success==0 自动删 | wontfix | 用户要求先保留；且易误杀 |
| 前端 embed 拆分 | open | 0.3.0 |
| 配置向导 | open | 0.3.0 |
| CPAMP 深度联动（除可选回补） | open | 非核心 |
| 自动清历史 used_today | wontfix | 改为手动确认清零今日；累计不自动动 |

## P0 不变量（任何版本不得回退）

1. 仅 xAI  
2. 429 free-usage / 402 spending → 冷却不删  
3. region / 426 / 网络 / 探测 4xx5xx → 不删  
4. 真 403 权限 / 401 → 删  
5. 用户手动禁用永不自动启用  
6. 日额度池不计禁用凭证；已用不用 rolling actual 冒充  

## 下一迭代顺序

1. **0.2.20**：signal UI + 复核说明 + 单测补齐 + 首轮巡查延迟可配  
2. **0.2.21**：处理日志筛选 + 账号搜索过滤（已落地）  
3. **0.3.0**：结构拆分与向导  


## 参考吸收：TizenryA/cpa-plugin-grok-panel

| 能力 | 吸收策略 |
|------|----------|
| Free/Super/Heavy 分类 | **0.2.23** 元数据启发式 `ClassifyAuthTier`（note/label/prefix/JSON） |
| Super/Heavy 保护 | UI 标记 `tier_protected`；默认不自动误清（删除仍仅真 401/403 白名单） |
| 健康检查/连续失败阈值 | 已有巡查+被动矩阵，不照搬「仅 runtime status」探针 |
| 主题/图表/商店 registry | 0.3.x：主题与进度条美化；registry 可选 |
| 面板零配置 host.auth | 我们走 management API；保持现网兼容 |
