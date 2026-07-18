
### Management Key（同一把，0.3.19+）

浏览器填写的 Key **就是** 进程调用 auth-files 的 Key。点「保存并同步进程」后：

1. localStorage（页面调插件 API）
2. 插件进程内存（立即用于禁用/恢复/巡查）
3. 尽量写入插件 yaml `management_key`（重启后仍有效）

也可在任意管理请求的 `X-Management-Key` 头里携带，进程会自动采用。

## 纯 CPA 最小配置（0.3.18+）

插件**不依赖 CPAMP**。只要 CPA 自身 management API 可达，即可完成冷却 / 到期恢复 / 主动巡查。

### yaml 最小示例

```yaml
plugins:
  enabled: true
  configs:
    cpa-xai-quota-guard:
      enabled: true
      quota_guard_enabled: true
      # management_url 可省略 → 默认 http://127.0.0.1:8317（与 CPA 同机/同容器网络）
      management_key: "<CPA_MANAGEMENT_KEY>"
      # 或进程环境变量：CPA_MANAGEMENT_KEY / MANAGEMENT_PASSWORD
      tick_seconds: 30
      patrol_enabled: false
```

### management 地址解析顺序（吸收 grok-inspection）

1. 插件配置 `management_url`
2. 环境变量 `CPA_MANAGEMENT_BASE_URL` 或 `CPA_BASE_URL`
3. `PORT` / `CPA_PORT` → `http://127.0.0.1:<port>`（TLS 环境见 `CPA_TLS=true`）
4. 默认 `http://127.0.0.1:8317`

环回地址调用 management **不走 HTTP_PROXY**，避免代理劫持导致 auth-files 失败。

### 两套 Key 不要混淆

| 位置 | 用途 |
|------|------|
| 浏览器 localStorage（配置页填写） | 调用插件管理路由 `/v0/management/cpa-xai-quota-guard/*` |
| yaml `management_key` 或 `CPA_MANAGEMENT_KEY` | 插件进程调用 CPA `auth-files` 做禁用/启用/删除 |

页面显示 `key_set=false` 且 `management_url=(empty)` 时：优先检查**浏览器是否保存了 Key**；用管理 Key 直接请求 `.../state` 若返回完整 config，则是 UI 鉴权问题而非服务器未配置。

### CPAMP（可选）

`cpamp_url` / `cpamp_admin_key` 仅用于「今日用量回补」。未配置时回补按钮禁用，**不影响**额度冷却与巡查。

### Docker 注意

- 插件与 CPA 同容器：空 `management_url` + 默认 `127.0.0.1:8317` 通常可用。
- 插件在宿主机、CPA 在容器：请设 `management_url: http://<可达主机>:<映射端口>` 或 `CPA_MANAGEMENT_BASE_URL`。
- 不要对跨主机场景使用 `127.0.0.1`。
