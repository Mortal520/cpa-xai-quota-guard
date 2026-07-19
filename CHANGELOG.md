# Changelog

## 0.3.24

### 402 spending：多次核实后再冷却
- 被动路径对 `personal-team-blocked:spending-limit` 不再单次即冷却
- 默认 **连续 3 次**（窗口 900s）核实后才按设计 `plugin_auto` 软冷却
- 中间成功请求会清零核实计数（短时波动恢复则不进冷却）
- 配置：`spending_confirm_hits`（默认 3，设 1=旧行为）、`spending_confirm_window_sec`（默认 900）
- 日志/被动动作：`spending_confirm` / `spending_confirm_ok`


## 0.3.23

### 修复 plugin_auto 停用标识丢失
- **根因**：`state` 拉取 auth-files 失败时 `inv` 为空，`PruneMissingInventory` 把全部自动停用记录当「凭证已删除」清掉
- 仅在 inventory 列表成功且非「空库存+仍有冷却记录」时才 prune
- `MarkActive` 保留 `last_cooldown_*` 痕迹，恢复后仍可追溯上次程序停用原因


## 0.3.22

### 状态栏「可用」修正
- 可用 = `xai_enabled`（CPA 当前仍启用、可调度）
- 不再用 `总数 - 程序停用 - 手动停用`（会把 CPA 已停用但未记 plugin_auto 的号算成可用，出现「启用1 / 可用62」）
- 副标题展示 CPA停用 / 程序冷却 / 手动 拆分


## 0.3.21

### 修复巡查 `patrol_auth_dir not configured`
- 空目录自动探测：`/root/.cli-proxy-api`、`~/.cli-proxy-api`、环境变量 `CPA_AUTH_DIR` 等
- 保存巡查配置时**不会用空值覆盖**已有 `patrol_auth_dir`
- state 回显有效目录（自动解析后）


## 0.3.20

### 对齐 grok-inspection 的纯 CPA 体验
- 自动从 CPA 管理面板 localStorage 读取 Management Key（支持 `enc::v1::` 混淆解密，算法同 grok-inspection）
- 打开配置页自动 bind-key：浏览器 Key = 进程 Key，尽量持久化 yaml
- 不依赖 CPAMP；URL 默认 `http://127.0.0.1:8317` / 环境变量
- UI 文案：单 Key，自动填充提示


## 0.3.19

### 纯 CPA 单 Key
- 浏览器与进程共用同一把 CPA X-Management-Key
- 保存 / 自动 bind-key：进程内存 + 尽量持久化 yaml
- 每次管理请求从请求头采用 Key
- cfgMeta 不再拆成「浏览器 Key / 进程 Key」误导


## 0.3.18

### 纯 CPA 兼容（吸收 grok-inspection）
- `management_url` 为空时自动解析：`CPA_MANAGEMENT_BASE_URL` / `PORT` / 默认 `http://127.0.0.1:8317`
- `management_key` 可回退环境变量 `CPA_MANAGEMENT_KEY` / `MANAGEMENT_PASSWORD`
- 环回 management 请求不走 HTTP_PROXY（避免 Docker/代理劫持）
- UI `cfgMeta` 区分「浏览器 Key」与「进程侧 management」；不再把未填 Key 误报为 empty 服务器配置
- CPAMP 明确为可选；未配置时回补按钮禁用，冷却/巡查不受影响
- 参考 [grok-inspection](https://github.com/ywddd/grok-inspection) 的 management 基址解析与面板 Key 体验

## 0.3.17

### 修复
- **修复可用卡片 JS 引用错误**：`xaiE` 变量定义遗漏导致页面 paintStatusBar 报错

## 0.3.16

### 修复
- **可用数计算修正**：改用 `xai_total - auto_disabled - user_manual`，修复因 `xai_enabled` 与 `auto_disabled` 数据源不一致导致可用显示为 0 的问题

## 0.3.15

### 变更
- **运行状态卡片精简**：由 4 张改为 3 张 —— 「xAI 凭证」（总数/CPA 启用/停用）、「可用」（CPA 启用 - 程序停用 - 手动停用）、「自动停用中」（plugin_auto 冷却数）
- 移除「已到点待启用」「用户手动停用」独立卡片（信息合入上述三卡）


## 0.3.14

### 修复
- **复查冷却号结束后页面不刷新**：结束后强制 `loadState({force:true})` 并延迟再刷两次，账号列表/状态栏同步恢复结果
- 结束后解锁「仅复查冷却号 / 全量巡查」按钮（此前正常完成时 spend 按钮会卡在「复查中...」）
- `loadState(force)` 在请求进行中会排队，避免被 soft 跳过后丢刷新


## 0.3.13

### 修复
- **冷却自动恢复时钟被 429/巡查复检无限后推**：soft 额度（rolling 24h free-usage / spending soft ceiling）复检时不再把 `recover_at` 重置为 now+24h
- 到期判定使用 `EffectiveRecoverAtMS`：错误延长到超过 `disabled_at+24h` 的 recover 会被钳回，Tick 到点自动启用并从冷却列表移除
- 状态 API 展示的恢复时间同步使用有效 recover（与 Tick 一致）

### 说明
- free-usage 无精确 reset 时间戳时，恢复锚点为 **首次禁用时刻 + 24h**，不是每次复检重计
- 精确 Retry-After / reset 解析仍可覆盖；`user_manual` 永不自动启用
- 复查冷却号探测 200 仍可提前恢复（0.3.12 逻辑保留）

## 0.3.12

- **修复复查冷却号不自动恢复**：探测 HTTP 200 时恢复所有 `plugin_auto` 冷却号（含 free-usage），不再仅限 `spending_limit`；此前会出现 `alive>0` 但 `reenabled=0`
- **处理日志**：巡查常规 `net_*` 不再写入持久化 action_history，避免淹没被动/恢复日志（仍保留在巡查 recent_log）
## 0.3.11

- **日额度池默认**：单账号估算由 **1M → 2M**（DefaultFreeLimit）；升级后覆盖 state 里旧的 1M 默认（与线上 free-usage limit=2000000 样本对齐）
- 同步 UI 文案 / config 说明 / registry 版本
- 吸收下游 fork [NikaidouYui/cpa-xai-quota-guard](https://github.com/NikaidouYui/cpa-xai-quota-guard) 有价值改动；其商店 CI 本仓 main 已具备
## 0.3.10

- **修复 CPA 商店安装 502**：Release 资产对齐 CLIProxyAPI `pluginstore` 约定
  - zip 名：`cpa-xai-quota-guard_{version}_{goos}_{goarch}.zip`
  - zip 内库文件在**根目录**（不再打包 `linux/amd64/...` 嵌套）
  - 附带 `checksums.txt`
  - `registry.json` 显式 `install.type: github-release`，版本同步 0.3.10
- **深色模式协调**：设备/系统夜间模式与 CPA 宿主深色壳下，插件页不再出现浅色残片
  - 按钮、标签、账号行、输入框、滚动条全部改 CSS 变量
  - 去掉「浅色行样式写在深色规则之后」的覆盖问题
  - @media (prefers-color-scheme: dark) 在 JS 前预置深色 token，减少白闪
  - 主题检测增强（parent 亮度采样 + data-theme/class + 系统偏好）

## 0.3.9

- **中止释放**：网络闸门/手动停止时 `context.Cancel` 取消在途探测 HTTP，worker 立即退出，不再卡到超时
- **按钮解锁**：`running=false` 后立即恢复「启动巡查 / 仅复查」；中止完成态红色提示仍保留原因
- 停止中 UI 显示「中止释放中 / 释放中...」，避免误以为永久锁死

## 0.3.8

- **巡查网络闸门**：连续 ≥6 次传输层网络失败（超时/DNS/TLS/连接等）后，自动检测**公网**与**巡查代理**连通性
  - 公网或代理任一侧异常 → **中止本轮巡查**，`last_error` 写明原因，处理日志记 `patrol_abort_net`
  - 两侧均正常 → 继续巡查，连续计数清零（可再次触发复检）
  - 不把 401/403/429/402 等业务错误算作网络连续失败
- **UI**：中止时状态行红色「已中止 · 网络/代理异常」+ 详情文案

## 0.3.7

- **修复** `PATROL_POLL is not defined`：补全局声明；启动巡查不再弹窗中断
- **修复** 配置项（并发上限/代理等）不回填：`loadState` 在 patrol 跟随时抛错导致表单填充中断；增加空字段恢复填充
- **巡查日志**：运行中约 2.4s 拉一次日志（原 4.8s），首 tick 即拉日志

## 0.3.6

- **主题**：删除插件自有「深色/浅色」切换与 localStorage 主题缓存；自动跟随 CPA/CPAMP 宿主主题（`data-theme` / `dark` class / 背景亮度 / `prefers-color-scheme`），iframe 内 1.5s 同步 + MutationObserver
- 含 0.3.5：巡查状态单组汇总、定时巡查自动跟随、账号表 UI、脱敏截图

## 0.3.5

- **巡查状态文案**：主行仅进度/线程/速率/ETA；副行 load·伸缩原因；汇总数字只保留进度条下方一组 chips
- **定时巡查实时跟随**：自动探测 `patrol/status`，定时扫启动后无需手动整页刷新
- **账号状态 UI**：行底渐变、原因主/副文案、恢复倒计时样式、隐藏 CPA成功0
- **文档截图**：`docs/screenshots/*.png` 脱敏真实页

## 0.3.4

- **巡查 status 瘦身**：默认 `recent_log` 最多 50 条，**优先非存活**（冷却/删除/异常），避免 126KB 轮询卡 UI
- **lite 轮询**：`patrol/status?lite=1` 不含日志；前端巡查中每 1.2s lite，约 4.8s 拉一次日志
- **完成态**：状态行显示耗时、速率( /s )、预计剩余
- **默认并发上限** 16 → **24**（仍为硬上限，弹性在上限内伸缩）
- `state` 内嵌 patrol 同步截断日志，降低管理页加载体积

## 0.3.3

- **弹性并发**：`patrol_concurrency` 为硬上限；实际 worker 在上限内按 `/proc/loadavg` + 探测超时/网络错误率 1s 伸缩
  - 空闲/健康：快速爬升至用户上限（I/O 密集，起步约 4×CPU 或上限半数）
  - 高负载/超时压力：收缩到 min(CPU, 上限)，避免卡死 CPA
  - status 暴露 `workers` / `workers_max` / `workers_min` / `load1` / `scale_reason`
- **UI**：巡查状态显示「实际N/上限M · load1 · 伸缩原因」；配置文案标明硬上限+弹性
- **性能**：巡查进行中跳过全量 state 软轮询（由 patrol/status 1.2s 刷新）；HTTP 连接池按上限扩容复用
- 补 `maxInt`（修复 0.3.3 WIP 编译）

## 0.3.2

- **UI**：巡查进度/探测日志改为按内容展开，去掉固定高度空白；处理日志改 max-height
- **巡查并发**：自适应改为约 `2×CPU`（4 核≈8 线程），默认上限 16；用户配置仍为硬上限
- 文案：并发上限提示改为「网络 I/O 密集 · 2×CPU」


## 0.3.1

- **删除**「额度/巡查速览」柱图（数据常为 0 / 易误导）
- **巡查并发**：按 CPU 自动限流，界面「并发」为**上限**（`resolvePatrolWorkers`）
- **UI 性能**：状态轮询 30s；倒计时 2s 仅更新 DOM 文本；页面隐藏时停轮询；巡查日志 DOM 限 40 行；处理日志最多 80 条
- **风格收敛**：去掉厚重卡片渐变/阴影，回到更简洁的面板布局；账号行增加状态圆点
- **文案**：副标题、巡查/日志/账号卡说明、按钮 title 精简
- **安装源**：`registry.json` + `registry.mirror.json`（ghproxy 加速 raw）；`docs/INSTALL.md` 增加加速源说明
- 前端资源：`web/console.html` + `//go:embed`（management.go）


## 0.3.0

- **主题**：浅色/深色切换（`data-theme` + localStorage；默认跟随系统）
- **移动端**：窄屏统计卡单列/双列、工具栏与表格高度、内边距收紧
- **统计图**：状态栏下方简易柱图（日池 / 今日已用 / 巡查存活 / 删+冷却）
- CSS 变量统一卡片/表格/输入在深色下的对比度（吸收 grok-panel 主题思路，非整页照搬）
- 版本号 → 0.3.0（`main.go` / `registry.json`）

## 0.2.23
- **开源交付**：根目录 `LICENSE`（MIT）；README 协议徽章与「开源协议」章节；界面预览截图区（`docs/screenshots/`）
- **CI**：正式启用多架构 workflow（linux/darwin/windows × amd64/arm64）+ tag 触发 Release zip

- **吸收 grok-panel 分类能力**（参考 TizenryA/cpa-plugin-grok-panel，MIT）
  - Free / Super / Heavy 启发式分类（note/label/prefix/tag/account_type + auth JSON 字段）
  - 账号表新增「套餐」列与筛选；状态栏显示 F/S/H 数量
  - `tier_protected` 标记 Super/Heavy/Unknown（UI 保护提示；删除仍仅真 401/403 白名单）
- 文档：`docs/THIRD_PARTY.md` 归因
- **安装交付**：`registry.json` 商店源；`docs/INSTALL.md` 安装/升级/卸载；CI 多平台 zip（linux/darwin/windows × amd64/arm64）+ tag Release

## 0.2.22

- **清零今日已用**（可选校准）：`POST .../metrics/reset-today` + UI 按钮
  - 仅清零日历 `used_today` / `requests_today`（及 per-auth 今日）
  - **不改** `used_total`、滚动 free-usage 快照、冷却/删除状态
  - 用于 0.2.18 前 ObserveFreeQuota 污染后的人工校准（需 confirm）
- 单测 `TestResetCalendarToday`

## 0.2.21

- **移除**配置页「立即扫描恢复」按钮（恢复仍由 tick 自动执行；`POST /run` API 保留兼容）
- **处理日志筛选**：来源（被动/巡查/tick）+ 动作（冷却/删除/恢复/异常/跳过）+ 搜索；最多显示 100 条
- **账号搜索**：支持信号 / 原因 / 状态关键词

## 0.2.20

- **账号信号列**：区分 429 免费额度 / 402 积分订阅 / 403 / 401 / 区域，筛选器可按信号过滤
- **巡查文案对齐 0.2.16**：全量=仅启用；仅复核=plugin_auto 冷却（含 free-usage 与 spending）
- **空巡查结果**：未跑过时显示「尚无巡查结果」而非误导性 0 完成
- **定时首轮延迟**：`patrol_initial_delay_sec` 默认 60s，避免重启立刻全量探测

## 0.2.19

- **巡查异常状态扩展**：网络超时/取消/DNS/TLS/连接失败分桶（HTTP 合成码 -1..-5 / 0），422/5xx/区域/CLI 细分动作
- **网络与瞬时 5xx 重试**：同模型最多 3 次（取消不重试；backoff 0.4s/0.8s）
- **汇总单源**：去掉 chips 内重复“动作”条；进度行 + HTTP chips + 异常细分来自同一 patrol 快照；探测数与 by_http 合计校验
- default 未知 action 不再误计为存活

## 0.2.18

- **状态栏额度口径重写**（日额度不等于禁用/删号/滚动 actual 混算）
  - **日额度池** = 当前启用 xAI 数 x 1M（rolling 24h 免费档）；禁用凭证不计入
  - **已用今日/累计** = 仅 usage.handle 真实 token；不再用 free-usage actual 抬高
  - **滚动快照 used/limit** = 仍存活凭证的 free-usage 观测；已删号快照剔除
  - 进度条 = 今日已用 / 日额度池
  - ObserveFreeQuota 只写快照，不改日历 UsedToday/UsedTotal
- 测试对齐新口径

## 0.2.17

- **巡查结果持久化**：计数/by_http/by_action/探测日志写入 state.json（`last_patrol`），重启后可恢复
- 巡查过程中每 25 条 checkpoint；结束强制落盘
- 巡查关键动作（删除/冷却/恢复/错误）同步写入 `action_history`
- state API 附带 `patrol` 快照，UI 单源汇总（去掉重复 HTTP 文案）
- 删除/动作历史容量提升（200/500）


## 0.2.16

- **全量巡查**：只扫**当前启用**的 xAI 凭证，跳过全部已禁用（含冷却号）
- **仅复核冷却号**：只扫 **plugin_auto 已禁用冷却号**，不碰启用中凭证


## 0.2.15

- **默认探测模型**改为 `grok-4.5`（Suggested 仍含 free）
- **定时巡查周期** UI 改为**分钟**（默认 60；配置仍存秒）
- **删除历史 + 被动处理日志**合并为「处理日志」单表（50 条去重）
- 含 0.2.14：巡查 HTTP 状态 chips、账号表固定分页


## 0.2.14

- **巡查 HTTP 状态统计**：按 200/429/402/403/401/426 等汇总显示（进度区 chips + 完成文案）
  - 新增 by_http / by_action / total_cooldown / total_429_cooldown / total_402_cooldown / total_reenabled
- **账号状态列表**：固定可视高度（height min 52vh/480px）+ 固定每页 20/30/50（上限 50，去掉 100/200）

## 0.2.13

- **UI**：刷新模型列表时探测模型下拉**宽度**不再被撑开
  - 根因：patrolModelHint 与 `flex:1` 下拉同行；加载时 hint 变短 → select 被拉宽，成功后 hint 变长又缩回
  - 修复：hint 独立成行 + 探测模型 label `max-width:420px` + 刷新按钮固定 `min-width`
  - 模型选项仍有 24 条硬上限，避免选项暴增
- **巡查 426**：cli-chat-proxy 返回 `CLI version (none) is outdated` 时识别为探测客户端问题
  - 自动注入默认 Grok CLI 头（`x-grok-client-version=0.2.93` / `x-xai-token-auth` / User-Agent 等），auth 文件 headers 可覆盖
  - 426 **永不删除**凭证；日志标记「CLI版本被拒」

## 0.2.12

- 审查：被动 HandleUsage 冷却/删除矩阵未改坏（region 不删、401/403 删、429/402 冷却、manual 保护、Tick 恢复）
- 新增 **被动处理日志** 卡片（`action_history` 持久化：cooldown/delete/recover/skip_*）
- 账号状态表：`max-height` 固定可视区 + 默认 30/页 + `auth_index` 去重 + 同页指纹跳过重复 DOM 重写，避免无效叠加/无限变长

## 0.2.11

- **探测模型不再自动回退默认**
- 去掉 loadState 首次填充后自动 `refreshPatrolModels`（重建 select 会把选项打回 free/首项）
- 手动刷新模型：await 后重读当前选择；dirty 时绝对保留用户模型；当前项置顶写入 options
- 记住 `_PATROL_UI_MODEL` / `_PATROL_UI_PROXY`，避免同页刷新列表时丢失

## 0.2.10

- **代理输入框不再被 15s loadState 自动回写**
- 根因：表单条件写成 `!dirty || !applied`，未编辑时每次轮询都会用服务端值覆盖输入
- 现仅：首次进入页填充一次；点「刷新」且确认后可强制同步；保存成功用接口回显
- dirty 绑定在 init 即生效

## 0.2.9

- **巡查 429 free-usage**：启用账号探测到短时额度 429 → `plugin_auto` 冷却（与 HandleUsage 一致），**绝不删除**
- 402 spending 冷却号遇 429/200 仍自动恢复启用
- 未识别信号的 429 记 error（不存活、不删）
- 修正 probe 模型循环内 region 分支缩进/结构可读性

## 0.2.8

- **表单稳定**：巡查配置 dirty 标记；loadState 15s 轮询不再覆盖未保存的模型/代理/上限
- **刷新模型列表**：保留当前 UI 选择（不再强制回 patrol_model/grok-4.5-build-free）；按钮 loading 反馈；超时 45s
- **代理**：保存后按服务端回显；清空也写回；编辑中不被定时刷新抹掉
- **每轮上限**：patrol_batch_size=0 为不限且可从配置正确落盘（>=0 解析）

## 0.2.7

- 巡查探测优先 `POST {base}/responses`（对齐 CPA `xai_executor`），body: `input` + `max_output_tokens`
- `/responses` 返回 404/405 时回退 `POST /chat/completions`
- 保留 0.2.6 错误矩阵（region/404 不删；402 冷却；真 403/401 删）

## 0.2.6

- **紧急**：`permission-denied` + `not available in your region` **不删除**凭证（IP/区域）
- 真死号仍删：chat endpoint denied / 401 invalid
- 探测默认 base：`cli-chat-proxy.grok.com/v1`（oauth 文件多无 base_url，旧 `api.x.ai` 易 404）
- 404/405/422/5xx 探测记 **error**，不记存活、不删
- 代理：`patrol_proxy_url` 保存时始终写回（含清空）；state/config **回显**；修复 UI 每次刷新清空输入
- 模型列表：`refreshPatrolModels` 正确解包 `api().result`；空列表保留当前模型
- 巡查配置：`patrol_auto_model_switch` 随保存持久化
- 编译修复：`listModels` 局部变量 `any` 与内置类型冲突 → `rawObj`
- 路线图：docs/ROADMAP_0.3.md

## 0.2.5

- **402 spending-limit 工作流完善**
  - 语义：积分/订阅耗尽（`personal-team-blocked:spending-limit`），**不是**死号；plugin_auto 冷却，**不删除**
  - 与 429 free-usage 状态区分（signal=`spending_limit` vs free-usage）
  - 新增 `patrol_auto_model_switch`（默认关）：开则 402 时拉取该凭证 `/models` 并尝试备用模型；全失败才冷却
  - 关自动换模：仅用 `patrol_model` 探测，402 → 冷却禁用
  - 新增 `POST .../patrol/spending` + UI「仅复查冷却号」：改模型/开关后只扫 spending 冷却账号
  - 状态记录 `last_probe_model`；日志含 tried 模型列表
- 文档：DESIGN/README 同步 402 与探测策略

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
