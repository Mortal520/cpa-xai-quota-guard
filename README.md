# cpa-xai-quota-guard

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![Version](https://img.shields.io/badge/version-0.3.13-blue.svg)](./CHANGELOG.md)
[![Release](https://img.shields.io/github/v/release/Mortal520/cpa-xai-quota-guard)](https://github.com/Mortal520/cpa-xai-quota-guard/releases)

CLIProxyAPI **原生 Go 插件**（**0.3.13**）：只管控 **xAI** 凭证——额度冷却、死号删除、主动/定时巡查、管理 UI。

| | |
|--|--|
| 仓库 | https://github.com/Mortal520/cpa-xai-quota-guard |
| Release | https://github.com/Mortal520/cpa-xai-quota-guard/releases |
| 插件 ID | `cpa-xai-quota-guard` |
| 协议 | [MIT](./LICENSE) |

## 推荐：CPA 插件商店安装

CPA **没有**把本插件写死在内置官方源里；把本仓库 `registry.json` 加到 `store-sources` 后，即可在 **管理中心 → 插件商店** 里点安装（与 grok-panel 同类商店源模型）。

```yaml
plugins:
  enabled: true
  dir: "plugins"
  store-sources:
    - "https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/registry.json"
    # GitHub 慢时可改用加速前缀，例如：
    # - "https://ghproxy.com/https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/registry.json"
  configs:
    cpa-xai-quota-guard:
      enabled: true
      quota_guard_enabled: true
      management_url: "http://127.0.0.1:8317"
      management_key: "<CPA_MANAGEMENT_KEY>"
      state_path: "data/cpa-xai-quota-guard-state.json"
      patrol_auth_dir: "/root/.cli-proxy-api"   # 按实际 auth 目录修改
```

1. 写入/合并上述配置并**重启 CPA**
2. 管理中心 → **插件商店** → **xAI Quota Guard** → 安装
3. 插件菜单打开配置页；确认 state 中 `version == 0.3.13`

商店从 GitHub Release 拉 zip，CPA 能访问 GitHub（或你配置的加速）时可用。  
**502 / plugin_install_failed**：资产名必须是 `cpa-xai-quota-guard_{version}_{goos}_{goarch}.zip`，库在 zip **根目录**，且 Release 带 `checksums.txt`。v0.3.10 起已按此布局发布；当前代码 **0.3.13**（商店资产需对应 Release tag）。详见 [docs/INSTALL.md](./docs/INSTALL.md)。

## 其它安装方式

| 方式 | 说明 |
|------|------|
| 脚本 | `CPA_PLUGINS_DIR=/path/to/plugins bash scripts/install.sh`（或 curl 管道，见 [docs/INSTALL.md](./docs/INSTALL.md)） |
| 手动 | 下载 [Releases](https://github.com/Mortal520/cpa-xai-quota-guard/releases) 最新对应架构 zip → 库文件放到 `plugins/<goos>/<goarch>/` → 合并配置 → 重启 |
| AI 助手 | [docs/AI_INSTALL.md](./docs/AI_INSTALL.md) |

库文件名：`cpa-xai-quota-guard.so` / `.dll` / `.dylib`。

## 界面预览

| 状态栏 | 巡查 | 账号 |
|--------|------|------|
| ![状态栏](./docs/screenshots/dashboard.png) | ![巡查](./docs/screenshots/patrol.png) | ![账号](./docs/screenshots/accounts.png) |

截图为真实 `web/console.html` + 脱敏 mock；重渲见 [docs/screenshots/README.md](./docs/screenshots/README.md)。

## 能力摘要

- **仅 xAI**：其它 provider 一律忽略
- **429 free-usage-exhausted**（rolling 24h）→ `plugin_auto` 临时禁用；**恢复锚点=首次禁用+24h**，复检 soft 不后推时钟（0.3.13）；到期 Tick 自动启用并离开冷却列表；巡查 200 可提前恢复
- **402 spending-limit** → 冷却、不删除；巡查探测恢复后可启用
- **401 / 真 403 权限** → 删除凭证
- **区域/模型不可用、426、404/5xx、网络** → 不删（日志/分桶）
- **用户手动禁用**永不自动启用；仅恢复本插件自动禁用的号
- **主动/定时巡查**、弹性并发、网络闸门（连续传输失败时检测公网/代理并中止）
- **主题跟随 CPA/CPAMP**（无插件独立深浅色开关；0.3.10 起深色 token 协调；0.3.11 日池默认 2M；0.3.13 修复 soft 冷却时钟）

完整错误矩阵与设计：[DESIGN.md](./DESIGN.md) · 变更记录：[CHANGELOG.md](./CHANGELOG.md)

## 最小配置说明

| 字段 | 用途 |
|------|------|
| `management_url` / `management_key` | CPA 管理 API（禁用/启用/删除） |
| `patrol_auth_dir` | auth JSON 目录（巡查必填） |
| `patrol_enabled` / `patrol_interval` | 定时巡查开关与周期（秒；UI 可按分钟编辑） |
| `patrol_model` | 探测模型，默认 `grok-4.5` |
| `patrol_concurrency` | 巡查并发**硬上限**（实际按负载弹性 ≤ 该值） |
| `patrol_proxy_url` | 探测代理（可选，建议固定出口） |

更多字段与升级/卸载：[docs/INSTALL.md](./docs/INSTALL.md)

## 验证

```bash
curl -sS -H "X-Management-Key: <KEY>" \
  "http://127.0.0.1:8317/v0/management/cpa-xai-quota-guard/state?view=focus"
```

期望含 `"version":"0.3.13"`。

## 文档

| 文档 | 内容 |
|------|------|
| [docs/INSTALL.md](./docs/INSTALL.md) | 商店 / 手动 / 升级卸载 / 502 排障 |
| [docs/AI_INSTALL.md](./docs/AI_INSTALL.md) | AI 一键安装提示词 |
| [DESIGN.md](./DESIGN.md) | 设计与处理矩阵 |
| [CHANGELOG.md](./CHANGELOG.md) | 版本记录 |
| [docs/RELEASE.md](./docs/RELEASE.md) | 发版与商店资产约定 |
| [docs/THIRD_PARTY.md](./docs/THIRD_PARTY.md) | 第三方归因 |

## 安全

禁止提交 management key、auth 目录、state 导出、代理账密。密钥仅写本地配置。

## 协议

[MIT License](./LICENSE)。第三方参考见 [docs/THIRD_PARTY.md](./docs/THIRD_PARTY.md)。