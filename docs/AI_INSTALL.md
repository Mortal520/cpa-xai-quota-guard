# AI 一键安装指令（cpa-xai-quota-guard）

面向：**把本段完整复制给任意 AI 助手**，让其在你的 CPA 主机上安装/升级插件。  
当前稳定版：**v0.3.13**  
仓库：https://github.com/Mortal520/cpa-xai-quota-guard

---

## 给 AI 的一键提示词（复制整段）

```text
你是运维助手。请在当前机器上为 CLIProxyAPI（CPA）安装/升级开源插件 cpa-xai-quota-guard v0.3.13。

目标：
1) 下载官方 Release 对应架构的 zip
2) 解压库文件到 CPA 的 plugins/<goos>/<goarch>/
3) 合并 plugins.configs.cpa-xai-quota-guard 最小配置（不要覆盖其它插件配置）
4) 重启 CPA（docker 容器名或 systemd 以现场为准）
5) 用 management API 验证 state.version == 0.3.13

硬性约束：
- 插件 ID：cpa-xai-quota-guard
- 仅处理 xAI 凭证额度/死号；不要改其它 provider
- 禁止把 management_key / token 写进 git 或公开日志；密钥只写本机配置
- 不要删除现有 state 文件（保留冷却历史）
- 若已存在配置，只补缺字段，不擅自清空用户已填的 proxy/auth_dir

参数（若用户未提供，先探测再问最少问题）：
- CPA 根目录或 plugins 目录（常见：/path/to/cliproxyapi/plugins 或 docker 挂载卷）
- management_url（如 http://127.0.0.1:8317）
- management_key（仅本机环境变量或现有配置读取）
- patrol_auth_dir（xAI auth json 目录，如 /root/.cli-proxy-api）
- 是否使用代理加速 GitHub（可选 ghproxy 前缀）

架构映射：
| OS/Arch | zip 资产名 | 库文件 |
| linux/amd64 | cpa-xai-quota-guard_0.3.13_linux_amd64.zip | cpa-xai-quota-guard.so |
| linux/arm64 | cpa-xai-quota-guard_linux_arm64.zip | cpa-xai-quota-guard.so |
| darwin/amd64 | cpa-xai-quota-guard_darwin_amd64.zip | cpa-xai-quota-guard.dylib |
| darwin/arm64 | cpa-xai-quota-guard_darwin_arm64.zip | cpa-xai-quota-guard.dylib |
| windows/amd64 | cpa-xai-quota-guard_windows_amd64.zip | cpa-xai-quota-guard.dll |

Release 下载（官方）：
https://github.com/Mortal520/cpa-xai-quota-guard/releases/download/v0.3.13/<资产名>

加速示例（可选）：
https://ghproxy.com/https://github.com/Mortal520/cpa-xai-quota-guard/releases/download/v0.3.13/<资产名>

安装路径示例（Linux amd64）：
$CPA_ROOT/plugins/linux/amd64/cpa-xai-quota-guard.so

最小配置（合并进 CPA config.yaml 的 plugins.configs）：
```yaml
cpa-xai-quota-guard:
  enabled: true
  quota_guard_enabled: true
  management_url: "http://127.0.0.1:8317"
  management_key: "<CPA_MANAGEMENT_KEY>"
  state_path: "data/cpa-xai-quota-guard-state.json"
  patrol_enabled: false
  patrol_interval: 3600
  patrol_timeout: 15
  patrol_auth_dir: "/root/.cli-proxy-api"
  patrol_proxy_url: ""
  patrol_concurrency: 16
  patrol_model: "grok-4.5"
  patrol_auto_model_switch: false
```

验证：
curl -sS -H "X-Management-Key: $KEY" \
  "$MGMT/v0/management/cpa-xai-quota-guard/state?view=focus" | 检查 version 字段

交付时报告：安装路径、version、enabled、是否需要用户补 management_key/patrol_auth_dir。
失败时给出精确错误与下一步，不要反复盲试同一命令超过 2 次。
```

---

## 人类/AI 可直接跑：Linux 一键脚本

> 在 **CPA 主机** 执行。先改顶部 4 个变量。

```bash
# ===== 用户改这里 =====
export CPA_PLUGINS_DIR="/path/to/cliproxyapi/plugins"   # 必改：plugins 根目录
export CPA_MGMT_URL="http://127.0.0.1:8317"             # 管理 API
export CPA_MGMT_KEY="<CPA_MANAGEMENT_KEY>"              # 管理密钥（勿提交）
export PATROL_AUTH_DIR="/root/.cli-proxy-api"           # xAI auth 目录
export VER="v0.3.13"
export USE_GHPROXY=0   # 1=走 ghproxy 加速
# =====================

set -euo pipefail
ARCH=$(uname -m)
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS-$ARCH" in
  linux-x86_64|linux-amd64)  GOOS=linux;  GOARCH=amd64; LIB=cpa-xai-quota-guard.so;   ZIP=cpa-xai-quota-guard_0.3.13_linux_amd64.zip ;;
  linux-aarch64|linux-arm64) GOOS=linux;  GOARCH=arm64; LIB=cpa-xai-quota-guard.so;   ZIP=cpa-xai-quota-guard_linux_arm64.zip ;;
  darwin-x86_64)             GOOS=darwin; GOARCH=amd64; LIB=cpa-xai-quota-guard.dylib; ZIP=cpa-xai-quota-guard_darwin_amd64.zip ;;
  darwin-arm64)              GOOS=darwin; GOARCH=arm64; LIB=cpa-xai-quota-guard.dylib; ZIP=cpa-xai-quota-guard_darwin_arm64.zip ;;
  *) echo "不支持的架构: $OS $ARCH"; exit 1 ;;
esac

BASE="https://github.com/Mortal520/cpa-xai-quota-guard/releases/download/${VER}/${ZIP}"
if [ "${USE_GHPROXY}" = "1" ]; then
  URL="https://ghproxy.com/${BASE}"
else
  URL="$BASE"
fi

DEST="${CPA_PLUGINS_DIR}/${GOOS}/${GOARCH}"
TMP=$(mktemp -d)
echo "[1/4] 下载 $URL"
curl -fL --retry 3 -o "$TMP/$ZIP" "$URL"
echo "[2/4] 解压到 $DEST"
mkdir -p "$DEST"
unzip -o "$TMP/$ZIP" -d "$TMP/out"
# zip 内可能是扁平文件或带目录
find "$TMP/out" -type f \( -name "*.so" -o -name "*.dylib" -o -name "*.dll" \) -exec cp -f {} "$DEST/$LIB" \;
test -f "$DEST/$LIB"
ls -la "$DEST/$LIB"

echo "[3/4] 重启 CPA（按环境二选一，失败可手动）"
if docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'cli-proxy-api'; then
  docker restart cli-proxy-api
elif command -v systemctl >/dev/null 2>&1 && systemctl list-units --type=service 2>/dev/null | grep -qi cliproxy; then
  systemctl restart cliproxyapi || systemctl restart cli-proxy-api || true
else
  echo "未检测到自动重启目标，请手动重启 CPA 使插件生效"
fi

echo "[4/4] 验证版本（若 key 正确）"
sleep 3
if [ -n "${CPA_MGMT_KEY}" ] && [ "${CPA_MGMT_KEY}" != "<CPA_MANAGEMENT_KEY>" ]; then
  curl -sS -m 12 -H "X-Management-Key: ${CPA_MGMT_KEY}" \
    "${CPA_MGMT_URL}/v0/management/cpa-xai-quota-guard/state?view=focus" \
    | sed -n '1,5p' || true
  echo
  echo "请确认 JSON 中 version 为 0.3.13 且 enabled 为 true"
else
  echo "未设置有效 CPA_MGMT_KEY，跳过 API 验证；请到管理页确认插件版本"
fi

echo "完成。库文件: $DEST/$LIB"
echo "请确保 CPA config 已配置 plugins.configs.cpa-xai-quota-guard（见下方最小配置）"
rm -rf "$TMP"
```

### 最小配置（首次安装必须写进 CPA `config.yaml`）

```yaml
plugins:
  enabled: true
  dir: "plugins"
  # 可选商店源：
  # store-sources:
  #   - "https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/registry.json"
  #   - "https://ghproxy.com/https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/registry.json"
  configs:
    cpa-xai-quota-guard:
      enabled: true
      quota_guard_enabled: true
      management_url: "http://127.0.0.1:8317"
      management_key: "<CPA_MANAGEMENT_KEY>"
      state_path: "data/cpa-xai-quota-guard-state.json"
      patrol_enabled: false
      patrol_interval: 3600
      patrol_timeout: 15
      patrol_auth_dir: "/root/.cli-proxy-api"
      patrol_proxy_url: ""
      patrol_concurrency: 16
      patrol_model: "grok-4.5"
      patrol_auto_model_switch: false
```

---

## 真·一行命令（Linux amd64 + 已设环境变量）

```bash
VER=v0.3.13 GOOS=linux GOARCH=amd64 LIB=cpa-xai-quota-guard.so ZIP=cpa-xai-quota-guard_0.3.13_linux_amd64.zip \
DEST="${CPA_PLUGINS_DIR:-./plugins}/linux/amd64" && mkdir -p "$DEST" && \
curl -fL "https://github.com/Mortal520/cpa-xai-quota-guard/releases/download/${VER}/${ZIP}" -o /tmp/${ZIP} && \
unzip -o /tmp/${ZIP} -d /tmp/cpaqg && \
find /tmp/cpaqg -type f -name '*.so' -exec cp -f {} "$DEST/$LIB" \; && \
ls -la "$DEST/$LIB" && echo "请重启 CPA 并配置 management_key / patrol_auth_dir"
```

GitHub 慢时把 URL 换成：

```text
https://ghproxy.com/https://github.com/Mortal520/cpa-xai-quota-guard/releases/download/v0.3.13/cpa-xai-quota-guard_0.3.13_linux_amd64.zip
```

---

## Windows（PowerShell，amd64）

```powershell
$Ver = "v0.3.13"
$Plugins = "C:\path\to\cliproxyapi\plugins"   # 改这里
$Dest = Join-Path $Plugins "windows\amd64"
$Zip = "cpa-xai-quota-guard_windows_amd64.zip"
$Url = "https://github.com/Mortal520/cpa-xai-quota-guard/releases/download/$Ver/$Zip"
New-Item -ItemType Directory -Force -Path $Dest | Out-Null
$Tmp = Join-Path $env:TEMP "cpaqg-$Ver"
New-Item -ItemType Directory -Force -Path $Tmp | Out-Null
Invoke-WebRequest -Uri $Url -OutFile (Join-Path $Tmp $Zip)
Expand-Archive -Force -Path (Join-Path $Tmp $Zip) -DestinationPath (Join-Path $Tmp "out")
Get-ChildItem -Recurse (Join-Path $Tmp "out") -Filter *.dll | Copy-Item -Destination (Join-Path $Dest "cpa-xai-quota-guard.dll") -Force
Get-Item (Join-Path $Dest "cpa-xai-quota-guard.dll")
Write-Host "请重启 CPA，并写入 plugins.configs.cpa-xai-quota-guard"
```

---

## 商店安装（AI 可代写配置）

在 `config.yaml` 增加：

```yaml
plugins:
  store-sources:
    - "https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/registry.json"
```

加速：

```yaml
    - "https://ghproxy.com/https://raw.githubusercontent.com/Mortal520/cpa-xai-quota-guard/main/registry.json"
```

重启后：管理中心 → 插件商店 → **xAI Quota Guard** → 安装。

---

## 安装后自检清单

```bash
# 1) 版本
curl -sS -H "X-Management-Key: $KEY" \
  "$URL/v0/management/cpa-xai-quota-guard/state?view=focus"

# 期望： "version":"0.3.13"  "enabled": true

# 2) 日志关键字
# plugin registered plugin_id=cpa-xai-quota-guard version=0.3.13
```

| 现象 | 处理 |
|------|------|
| version 旧 | 确认覆盖了正确 `plugins/<os>/<arch>/` 且已重启 |
| 404 管理路由 | 插件未加载 / 架构 zip 放错 |
| 凭证数 0 | 查 management_url/key 与 CPA auth-files |
| 巡查全失败 | 配 `patrol_auth_dir` + 代理出口 |

---

## 安全

- 命令里的 `<CPA_MANAGEMENT_KEY>` 必须替换为本地密钥，**不要发到公开频道**
- 不要把 key 写进 git / 截图 / Issue

---

## 参考

- 安装详解：https://github.com/Mortal520/cpa-xai-quota-guard/blob/main/docs/INSTALL.md  
- Releases：https://github.com/Mortal520/cpa-xai-quota-guard/releases/tag/v0.3.13  
- 仓库：https://github.com/Mortal520/cpa-xai-quota-guard  

