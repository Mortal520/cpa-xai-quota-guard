# 安装 / 升级 / 卸载

插件 ID：`cpa-xai-quota-guard`  
当前版本以 `main.go` 的 `pluginVer` 与 [registry.json](../registry.json) 为准。

本安装方式对齐 CPA 插件商店生态（参考 [cpa-plugin-grok-panel](https://github.com/TizenryA/cpa-plugin-grok-panel) 的商店源 + Release 资产模型），并保留本仓库的手动部署路径。

## 平台与产物

| 平台 | GOOS/GOARCH | 库文件名 | Release zip（CI） |
|------|-------------|---------|-------------------|
| Linux x86_64 | linux/amd64 | `cpa-xai-quota-guard.so` | `cpa-xai-quota-guard_linux_amd64.zip` |
| Linux ARM64 | linux/arm64 | `cpa-xai-quota-guard.so` | `cpa-xai-quota-guard_linux_arm64.zip` |
| macOS Intel | darwin/amd64 | `cpa-xai-quota-guard.dylib` | `cpa-xai-quota-guard_darwin_amd64.zip` |
| macOS Apple Silicon | darwin/arm64 | `cpa-xai-quota-guard.dylib` | `cpa-xai-quota-guard_darwin_arm64.zip` |
| Windows x86_64 | windows/amd64 | `cpa-xai-quota-guard.dll` | `cpa-xai-quota-guard_windows_amd64.zip` |
| Windows ARM64 | windows/arm64 | `cpa-xai-quota-guard.dll` | `cpa-xai-quota-guard_windows_arm64.zip`（可能缺工具链） |

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
  "http://<CPA_HOST>:<PORT>/v0/management/plugin-store/cpa-xai-quota-guard/install?source=<SOURCE_ID>&version=v0.2.23"
```

> 商店安装依赖 CPA 能拉取 GitHub Release 资产。若机器无法访问 GitHub，改用方式 B/C。

### 4. 打开

插件菜单 → **cpa-xai-quota-guard** 配置页。

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
| `plugin_install_failed` | 架构 zip 是否存在；CPA 能否访问 GitHub Release；看返回 `message` |
| 404 管理路由 | 插件未加载/版本错；查 host 日志 plugin registered |
| 配置页空白 | 管理密钥 / iframe 会话；浏览器 localStorage key |
| xai 凭证数 0 | management_url/key；auth-files API；缓存粘滞日志 |
| 巡查全网络错误 | `patrol_proxy_url` / 出口 IP / CLI 头 426 |

## 安全

- 源码、Release、registry **禁止**含 management key / token  
- 面板不回显完整 key  
- 删除走 CPA 管理 API，不直接 rm 用户磁盘（除 CPA 自身 auth 删除语义）

## 版本与发版流程（维护者）

1. 改代码 → `go test` → 部署实测  
2.  bump `pluginVer` + `registry.json` version + CHANGELOG  
3. commit / push `main`  
4. 打 tag：`git tag v0.2.23 && git push origin v0.2.23`  
5. CI 构建多平台 zip 并挂到 GitHub Release  
6. 确认 raw `registry.json` 可访问  

## 与 grok-panel 安装模型的差异

| 项 | grok-panel | 本插件 |
|----|------------|--------|
| 商店 registry | 有 | 有（本文件同级 `registry.json`） |
| 核心能力 | 面板统计/清理建议 | 额度冷却 + 死号删除 + 巡查 |
| 配置 | 可近零配置看统计 | 需要 management_url/key 才能操作账号 |
| 多平台 | Release 六包 | CI 对齐六包（win/arm64 可能缺） |
