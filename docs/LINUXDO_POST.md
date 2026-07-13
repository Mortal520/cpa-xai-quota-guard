# [开源] cpa-xai-quota-guard：CLIProxyAPI 原生插件，xAI 额度冷却 / 死号清理 / 主动巡查

> 针对 **xAI / Grok 登录凭证** 的 CPA 原生 Go 插件。  
> 只干一件事：额度用尽先冷却，死号才删除，手动停用永不误开。

**仓库**：[https://github.com/Mortal520/cpa-xai-quota-guard](https://github.com/Mortal520/cpa-xai-quota-guard)  
**协议**：MIT  
**当前版本**：`0.3.7`  
**形态**：CLIProxyAPI / CPA **c-shared 原生插件**（不是油猴、不是独立旁路服务）

---

## 一句话

跑 CPA 多号 xAI 时，最烦的是：

- free 额度 rolling 24h 用完还在轮询 → 全池被 429 拖垮  
- 402 积分耗尽 / 401 失效 / 403 权限混在一起 → 误删或漏删  
- 死号堆在 auth 目录 → 成功率越来越差  

这个插件把 **被动拦截 + 到期恢复 + 主动巡查 + 管理页** 做在一起，规则按 **xAI 真实错误码** 写，不照搬 Codex 冷却逻辑。

---

## 适合谁

- 在用 **CLIProxyAPI（CPA）**，且有一批 **xAI oauth / grok 登录凭证**
- 想要 **额度耗尽自动冷却、到期自动恢复**
- 想要 **定时/手动巡查**，把 401/真 403 死号清掉
- 不想写一堆脚本盯日志、手工改 `disabled`

**不适合**：非 CPA 架构、非 xAI 号池、指望一个插件兼容全网模型供应商。

---

## 核心能力

### 1. 被动额度管控（`usage.handle`）

| 上游信号 | 动作 |
|----------|------|
| **429** `free-usage-exhausted`（rolling 24h） | `plugin_auto` 临时禁用 → tick 到期自动恢复 |
| **402** `spending-limit` | 冷却 **不删**；巡查可再测恢复 |
| **401** 凭证失效 / **403** 真权限拒绝 | **删除** auth 文件 |
| 区域不可用、426 CLI 版本、网络/5xx 等 | **不删**（记异常分桶） |
| 用户手动停用 | **永远不自动启用** |

状态持久化区分：

- `plugin_auto`：程序自动停用（可恢复）
- `user_manual`：用户手动停用（只读保护）

### 2. 主动 / 定时巡查

- **全量巡查**：只扫 **当前已启用** 的 xAI（跳过全部禁用号）
- **仅复查冷却号**：只扫本插件 `plugin_auto` 冷却中的号
- 探测模型默认可配（如 `grok-4.5`），可选 402 自动换模再测
- **弹性并发**：配置项是 **硬上限**；实际线程按 `load1` + 探测超时/网络错误率伸缩
- 管理页实时显示：进度、速率、ETA、实际线程 / 上限、HTTP 分桶

### 3. 管理 UI

- 状态栏：xAI 凭证数、日额度池(估)、今日已用、滚动快照、累计已用
- 巡查配置 + 操作合并卡（周期 / 超时 / 代理 / 模型 / 并发上限）
- 探测日志（优先展示冷却/删除/异常）
- 账号状态分页（Free/Super/Heavy、恢复倒计时、今日用量）
- 主题 **跟随 CPA/CPAMP 宿主**，不做插件独立深浅色开关

### 4. 设计原则（踩坑总结）

- **只认 xAI**，其它 provider 一律忽略  
- **宁可不动作，也不误删**  
- 时间戳解析失败 → **不禁用**，只记日志  
- 429 / 402 / 区域 403 / 真权限 403 **必须拆开**  
- 巡查模型选错会「全员 402/区域拒绝」——默认用更贴近 free 可用链路的模型，而不是随便塞付费模型  

---

## 界面示意（脱敏）

> 图为仓库内用真实 `console.html` + 合成数据渲染，不含真实 key / 邮箱 / 代理密码。

### 状态栏 / 额度

![状态栏](https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/docs/screenshots/dashboard.png)

### 主动巡查

![巡查](https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/docs/screenshots/patrol.png)

### 账号状态

![账号](https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/docs/screenshots/accounts.png)

若 raw 图裂了，可看仓库：  
https://github.com/Mortal520/cpa-xai-quota-guard/tree/main/docs/screenshots

---

## 安装（简版）

1. 从 [Releases](https://github.com/Mortal520/cpa-xai-quota-guard/releases) 下载对应平台 `.so` / 二进制产物  
2. 放到 CPA 插件目录，例如：`plugins/linux/amd64/cpa-xai-quota-guard.so`  
3. 在 CPA 插件配置里启用 `cpa-xai-quota-guard`  
4. 填 `management_url` / `management_key`、`patrol_auth_dir`（巡查必填）  
5. 重启 CPA / 重载插件，打开管理页验证版本号

更细的安装 / 加速源 / 多架构说明：

- [docs/INSTALL.md](https://github.com/Mortal520/cpa-xai-quota-guard/blob/main/docs/INSTALL.md)  
- 仓库源：`registry.json` / `registry.mirror.json`（含 ghproxy 加速 raw）

配置片段（**key 请用占位符**）：

```yaml
plugins:
  configs:
    cpa-xai-quota-guard:
      enabled: true
      management_url: "http://127.0.0.1:8317"
      management_key: "<CPA_MANAGEMENT_KEY>"
      state_path: "data/cpa-xai-quota-guard-state.json"
      patrol_enabled: false
      patrol_interval: 3600
      patrol_auth_dir: "/root/.cli-proxy-api"
      patrol_proxy_url: ""          # 建议固定出口，减少区域误伤
      patrol_concurrency: 16       # 硬上限；实际线程弹性 ≤ 该值
      patrol_model: "grok-4.5"
      patrol_auto_model_switch: false
```

---

## 最近版本在修什么（0.3.x）

- **0.3.3** 弹性并发（load + 探测健康，用户上限硬封顶）  
- **0.3.4** status lite 轮询、日志优先非存活，减轻管理页卡顿  
- **0.3.6** 删除插件自有深色模式，跟随 CPA/CPAMP 主题  
- **0.3.7** 修复 `PATROL_POLL is not defined`、配置项回填空白、巡查日志刷新  

完整记录：[CHANGELOG.md](https://github.com/Mortal520/cpa-xai-quota-guard/blob/main/CHANGELOG.md)

---

## 和「脚本 / 油猴 / 通用限流器」差在哪

| | 本插件 | 常见脚本方案 |
|--|--------|--------------|
| 接入点 | CPA 原生 `usage` / management | 外挂轮询日志或改文件 |
| 错误语义 | xAI 专用矩阵（429/402/401/403 拆分） | 容易一刀切 |
| 手动停用 | 硬保护 | 容易被定时任务误开 |
| 巡查 | 启用号全量 + 冷却号复核 | 往往只有一种扫法 |
| UI | CPA 管理页内嵌 | 另起控制台 |

---

## 已知边界（诚实说）

- **只服务 xAI**，不会兼容 OpenAI / Claude / Gemini 号池  
- 日额度池是 **启用数 × 1M 的估算**，不是 xAI 官方账单 API  
- 巡查质量强依赖 **探测模型 + 出口 IP 区域**；模型/IP 不对会出现成片区域拒绝或 402  
- 超大号池（几千）时，管理页默认 focus 视图，避免一次塞全表卡死  
- 需要你已有可运行的 CPA 环境与管理 key  

---

## 链接

- GitHub：https://github.com/Mortal520/cpa-xai-quota-guard  
- License：https://github.com/Mortal520/cpa-xai-quota-guard/blob/main/LICENSE  
- Issues：欢迎贴 **脱敏** 后的 `HTTP 状态 + body.code + 是否删除/冷却` 样本  

---

## 求什么反馈

如果你也在 CPA 上跑 xAI：

1. 你的号池规模、巡查一轮耗时、并发上限怎么设的  
2. 还遇到过哪些 **不该删却删了 / 该冷却却没冷却** 的错误体  
3. 想不想要：Webhook 通知、导出报表、和 CPAMP 更深联动  

Star / Issue / PR 都欢迎。  
有用的话转给同样在 concurrent 扫 Grok 的朋友，少踩一点「全池 429」的坑。

---

*本帖描述基于仓库 `0.3.7`；功能以 GitHub README 与 CHANGELOG 为准。*
