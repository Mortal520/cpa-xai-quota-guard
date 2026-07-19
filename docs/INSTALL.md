# 安装 / 升级 / 卸载

> 当前稳定实现版本：**0.3.13**（见 [CHANGELOG](../CHANGELOG.md)）。商店/脚本安装以 GitHub Release 资产为准。

插件 ID：`cpa-xai-quota-guard`  
当前版本以 `main.go` 的 `pluginVer` 与 [registry.json](../registry.json) 为准。

本安装方式对齐 CPA 插件商店生态（参考 [cpa-plugin-grok-panel](https://github.com/TizenryA/cpa-plugin-grok-panel) 的商店源 + Release 资产模型），并保留本仓库的手动部署路径。

## 平台与产物

| 平台 | GOOS/GOARCH | 库文件名 | Release zip（CPA 商店 / CI） |
|------|-------------|---------|-------------------|
| Linux x86_64 | linux/amd64 | `cpa-xai-quota-guard.so`（**zip 根目录**） | `cpa-xai-quota-guard_{ver}_linux_amd64.zip` |
| Linux ARM64 | linux/arm64 | `cpa-xai-quota-guard.so` | `cpa-xai-quota-guard_{ver}_linux_arm64.zip` |
| macOS Intel | darwin/amd64 | `cpa-xai-quota-guard.dylib` | `cpa-xai-quota-guard_{ver}_darwin_amd64.zip` |
| macOS Apple Silicon | darwin/arm64 | `cpa-xai-quota-guard.dylib` | `cpa-xai-quota-guard_{ver}_darwin_arm64.zip` |
| Windows x86_64 | windows/amd64 | `cpa-xai-quota-guard.dll` | `cpa-xai-quota-guard_{ver}_windows_amd64.zip` |
| Windows ARM64 | windows/arm64 | `cpa-xai-quota-guard.dll` | `cpa-xai-quota-guard_{ver}_windows_arm64.zip`（可能缺工具链） |

> CPA 插件商店硬性约定（否则安装 API 返回 **HTTP 502 / plugin_install_failed**）：
> 1. zip 文件名：`{plugin_id}_{version}_{goos}_{goarch}.zip`（version **无** `v` 前缀，如 `0.3.10`）
> 2. zip 内动态库位于**根目录**（不能是 `linux/amd64/...` 嵌套路径）
> 3. 同 Release 必须附带 `checksums.txt`（sha256）

> **当前 v0.3.10 已发布资产**：`linux_amd64` / `linux_arm64` / `darwin_amd64` / `darwin_arm64` / `windows_amd64` + `checksums.txt`。`windows_arm64` 可能因工具链缺席未发布，请用手动编译或后续 CI 补齐。


CPA 加载目录约定：

```text
plugins/<goos>/<goarch>/cpa-xai-quota-guard.so|.dll|.dylib
```

示例（Linux amd64）：

```text
plugins/linux/amd64/cpa-xai-quota-guard.so
```

## 方式 A：插件商店（推荐给可访问 GitHub 的环境）

### 1. 配置 store 源

在 CPA `config.yaml`：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  store-sources:
    - "https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/registry.json"
  configs:
    cpa-xai-quota-guard:
      enabled: true
      quota_guard_enabled: true
      management_url: "http://127.0.0.1:8317"
      management_key: "<CPA_MANAGEMENT_KEY>"
      state_path: "data/cpa-xai-quota-guard-state.json"
      patrol_auth_dir: "/root/.cli-proxy-api"   # 按实际 auth 目录改
```

### 2. 重启 CPA

### 3. 安装

管理中心 → 插件商店 → **xAI Quota Guard** → 安装。

或 API（示例，密钥勿提交）：

```bash
curl -X POST \
  -H "Authorization: Bearer <CPA_MANAGEMENT_KEY>" \
  "http://<CPA_HOST>:<PORT>/v0/management/plugin-store/cpa-xai-quota-guard/install?source=<SOURCE_ID>&version=0.3.10"
```

> 商店安装依赖 CPA 能拉取 GitHub Release 资产。若机器无法访问 GitHub，改用方式 B/C。

### 4. 打开

插件菜单 → **cpa-xai-quota-guard** 配置页。


## 方式 A2：GitHub 加速商店源（公开可用）

当机器访问 `raw.githubusercontent.com` / GitHub Release 不稳定时，可把 store 源换成公共加速前缀（示例使用 [ghproxy](https://ghproxy.com) 类公开镜像；可用性随第三方变化，可替换为你自建镜像）：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  store-sources:
    # 原版
    # - "https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/registry.json"
    # 加速 raw（二选一即可）
    - "https://ghproxy.com/https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/registry.json"
    # 或使用仓库内镜像清单文件（内容与 registry.json 相同，便于自建 CDN 只同步此文件）
    # - "https://ghproxy.com/https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/registry.mirror.json"
  configs:
    cpa-xai-quota-guard:
      enabled: true
      quota_guard_enabled: true
      management_url: "http://127.0.0.1:8317"
      management_key: "<CPA_MANAGEMENT_KEY>"
```

Release zip 下载也可同样加前缀，例如：

```text
https://ghproxy.com/https://github.com/Mortal520/cpa-xai-quota-guard/releases/download/v0.3.10/cpa-xai-quota-guard_0.3.10_linux_amd64.zip
```

> 加速域名仅为网络可达性方案，**不改变**插件校验与版本语义；密钥仍只写本地配置。


## 方式 B：GitHub Release 手动安装

1. 打开仓库 Releases，下载与宿主匹配的 zip  
2. 解压后把库文件放到 `plugins/<goos>/<goarch>/`  
3. 写入/合并 `plugins.configs.cpa-xai-quota-guard`  
4. 重启 CPA  
5. 用 state API 确认版本：

```bash
curl -sS -H "X-Management-Key: <KEY>" \
  "http://127.0.0.1:8317/v0/management/cpa-xai-quota-guard/state" | jq .version
```

## 方式 C：本机构建部署（当前生产常用）

Linux + CGO（示例 zig cc）：

```bash
export PATH=$HOME/tools/go1.26/bin:$PATH GOROOT=$HOME/tools/go1.26
export GOPROXY=https://goproxy.cn,direct GOSUMDB=off CGO_ENABLED=1
export CC="$HOME/tools/zig-linux-x86_64-0.14.0/zig cc"
cd ~/src/cpa-xai-quota-guard
# go.mod: replace CLIProxyAPI => ./CLIProxyAPI-src
go test ./internal/xaiquota/ -count=1
go build -buildmode=c-shared -o bin/cpa-xai-quota-guard.so .
cp -f bin/cpa-xai-quota-guard.so "/path/to/cliproxyapi/plugins/linux/amd64/"
docker restart cli-proxy-api   # 或 systemctl restart ...
```

## 升级

| 路径 | 步骤 |
|------|------|
| 商店 | 插件管理点更新，或 reinstall 指定 `version` |
| Release zip | 替换同路径库文件 → 重启 CPA → 查 `state.version` |
| 源码 | `git pull` → `go test` → `go build` → 覆盖 so/dll → 重启 |

**升级注意**

- `state_path` JSON 会保留冷却/历史；一般无需迁移  
- 大版本若改 state schema，看 CHANGELOG  
- 确认日志：`plugin registered ... version=x.y.z`  
- 勿同时放多个旧 so 副本导致加载错文件  

## 卸载

1. CPA 插件管理卸载 `cpa-xai-quota-guard`，或配置里 `enabled: false`  
2. 删除 `plugins/<goos>/<goarch>/cpa-xai-quota-guard.*`  
3. 重启 CPA  
4. （可选）备份后删除 `state_path` 与配置段  

卸载**不会**自动恢复已冷却账号；若需恢复，卸载前先关管控并执行恢复扫描/手动启用。

## 常见故障

| 现象 | 排查 |
|------|------|
| `plugin_install_failed` / **HTTP 502** | 见下方「商店 502」专节；常见：资产名不含版本、zip 内嵌套路径、缺 checksums.txt、CPA 无法访问 GitHub |
| `plugin_store_registry_failed` / 502 | CPA 拉不到 `registry.json`（raw.githubusercontent.com 被墙）；改用 ghproxy 商店源或手动安装 |
| 404 管理路由 | 插件未加载/版本错；查 host 日志 plugin registered |
| 配置页空白 | 管理密钥 / iframe 会话；浏览器 localStorage key |
| xai 凭证数 0 | management_url/key；auth-files API；缓存粘滞日志 |
| 巡查全网络错误 | `patrol_proxy_url` / 出口 IP / CLI 头 426 |


## 商店安装返回 502（plugin_install_failed）

CPA `POST /v0/management/plugin-store/:id/install` 在安装失败时统一返回 **HTTP 502**，`error` 多为 `plugin_install_failed` / `plugin_store_registry_failed`。

### 根因对照（本仓库历史问题）

| 错误 message 特征 | 原因 | 处理 |
|-------------------|------|------|
| `release asset cpa-xai-quota-guard_0.x.y_linux_amd64.zip not found` | Release 只挂了旧名 `cpa-xai-quota-guard_linux_amd64.zip` | 升级到 **≥ v0.3.10** 带版本号资产；或手动安装 |
| `release asset checksums.txt not found` | 缺校验文件 | 同左 |
| `target dynamic library must be at zip root` / `zip does not contain cpa-xai-quota-guard.so` | zip 打成了 `linux/amd64/xxx.so` 嵌套结构 | 用新 CI 产物（库在 zip 根） |
| `unexpected status 403/429` / timeout | CPA 出网访问 GitHub 失败 | 配置出网代理 / `store` 相关 token；或方式 B/C 手动装 |
| `plugin_store_registry_failed` | 拉 registry 失败 | 商店源换 ghproxy raw，或自建镜像 `registry.mirror.json` |

### 正确的商店源配置

```yaml
plugins:
  enabled: true
  dir: "plugins"
  store-sources:
    - "https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/registry.json"
    # 国内/受限网络示例：
    # - "https://ghproxy.com/https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/registry.json"
```

### 正确的 Release 资产示例（0.3.10）

```text
cpa-xai-quota-guard_0.3.10_linux_amd64.zip   # 内含根目录 cpa-xai-quota-guard.so
cpa-xai-quota-guard_0.3.10_windows_amd64.zip
checksums.txt
```

### 临时绕过（推荐在 502 时）

1. 从 Releases 下载对应 zip（或用 `scripts/install.sh`）
2. 解压得到 `cpa-xai-quota-guard.so`，放到 `plugins/linux/amd64/`
3. 合并 `plugins.configs.cpa-xai-quota-guard` 后重启 CPA

## 安全

- 源码、Release、registry **禁止**含 management key / token  
- 面板不回显完整 key  
- 删除走 CPA 管理 API，不直接 rm 用户磁盘（除 CPA 自身 auth 删除语义）

## 版本与发版流程（维护者）

1. 改代码 → `go test` → 部署实测  
2.  bump `pluginVer` + `registry.json` version + CHANGELOG  
3. commit / push `main`  
4. 打 tag：`git tag v0.3.10 && git push origin v0.3.10`  
5. CI 构建多平台 zip 并挂到 GitHub Release  
6. 确认 raw `registry.json` 可访问  

## 与 grok-panel 安装模型的差异

| 项 | grok-panel | 本插件 |
|----|------------|--------|
| 商店 registry | 有 | 有（本文件同级 `registry.json`） |
| 核心能力 | 面板统计/清理建议 | 额度冷却 + 死号删除 + 巡查 |
| 配置 | 可近零配置看统计 | 需要 management_url/key 才能操作账号 |
| 多平台 | Release 六包 | CI 对齐六包（win/arm64 可能缺） |

## 开源协议

MIT，见仓库根目录 [LICENSE](../LICENSE)。界面预览见 [screenshots](./screenshots/)。发版见 [RELEASE.md](./RELEASE.md)。

## 纯 CPA（无 CPAMP）

见 [PURE_CPA.md](./PURE_CPA.md)。


