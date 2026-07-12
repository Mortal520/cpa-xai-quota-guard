# cpa-xai-quota-guard 技术路线 0.2.x → 0.3.x

> 每小版本：实现 → go test → 自审 → 远端部署实测 → CHANGELOG → 下一版  
> 线上基线：**0.2.19** · 工作区：`F:\project\cpa-xai-quota-guard`  
> 更新：2026-07-13

## 目标 / 原则 / 成功标准 / 约束

| 项 | 内容 |
|----|------|
| 目标 | xAI 额度与死号管控可靠；不误杀；UI 清晰；迭代至 0.3.x |
| 原则 | 仅 xAI；白名单；宁可不动作不误删；模型/区域/IP ≠ 凭证死号 |
| 成功 | region/404/402/426/网络 不删；真 403/401 才删；日池=启用×1M；关键路径有测试+实测 |
| 约束 | 密钥不进 Git；小步可回滚；每版必测 |

## 错误矩阵（权威，与代码一致）

| 信号 | 动作 |
|------|------|
| 429 free-usage | plugin_auto 冷却 + tick 恢复 |
| 402 spending-limit | plugin_auto 冷却 + 巡查恢复（可选换模） |
| 403 chat endpoint denied | DELETE |
| 403 model not available in region | **不删** |
| 401 invalid credentials | DELETE |
| 426 CLI version | **不删**（注入 CLI 头后重试环境） |
| 404/5xx/网络 | error 分桶，不存活不删；网络最多 3 次重试 |

## 已落地版本（摘要）

| 版 | 内容 | 状态 |
|----|------|------|
| 0.2.6–0.2.8 | region 不删、探测 base、表单 dirty、代理不回写 | 已上线 |
| 0.2.9 | 巡查 429 冷却对齐 HandleUsage | 已上线 |
| 0.2.10–0.2.11 | 代理/模型不自动回退；脏表单保护 | 已上线 |
| 0.2.12–0.2.15 | 被动日志、分页、默认 grok-4.5、周期分钟、日志合并 | 已上线 |
| 0.2.16 | 全量只扫启用；复核只扫冷却 | 已上线 |
| 0.2.17 | last_patrol 持久化 + action_history | 已上线 |
| 0.2.18 | 日额度池/真实已用/滚动快照口径 | 已上线 |
| 0.2.19 | 异常分桶、网络重试、汇总单源 | 已上线 |
| 0.2.20 | 信号列、首轮延迟 | 已上线 |
| 0.2.21 | 去立即扫描、日志筛选 | 已上线 |
| 0.2.22 | 清零今日已用校准 | 已上线 |
| **0.2.23+** | MIT LICENSE / README 截图 / 多架构 CI 正式启用 | **文档发版中** |

## 待落地 / 回填（详见 GAP_BACKLOG）

### 0.2.20 硬化（下一版）
- 账号状态 signal 展示（free-usage vs spending_limit）更醒目
- free-usage 冷却号是否允许「仅复核」覆盖（可配置）
- patrol 429 冷却集成单测；网络重试单测
- 重启后 last_patrol 空快照时 UI 明确「尚无结果」
- 历史 used_today 污染说明/可选清洗工具（默认不自动改）

### 0.2.21 UX
- 处理日志筛选（来源 usage/patrol、动作类型）
- 账号表：signal 列 + 搜索/过滤（启用/冷却/手动）
- 巡查运行中禁重复点；完成 toast 与进度行一致

### 0.3.0 里程碑
- 前端从 management.go 字符串拆出（embed 或静态）
- 配置向导（auth_dir / proxy / model 最小检查）
- 导出脱敏报告；批量操作（仅冷却恢复扫描）
- 文档与代码字段完全对齐；连续两版无 P0 误删

## 每版自审清单
1. 是否扩大删除面？
2. 402/429/region/401/403/426 是否互串？
3. proxy/model UI 是否被轮询覆盖？
4. toggle 是否写 quota_guard_enabled 且 GET+merge+PUT？
5. 远端 `state.version` 是否真更新？
6. metrics 是否仍把禁用/删号/rolling actual 算进日池或已用？

## 停止条件（0.3.x）
连续两版无 P0 误删；五路径实测过；UI 单源一致；文档=代码。
