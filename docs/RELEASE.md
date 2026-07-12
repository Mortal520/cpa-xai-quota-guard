# 发版步骤（GitHub Release）

## 前置

1. 仓库 `main` 已包含 `.github/workflows/build.yml`（多架构 + Release job）
2. 推送账户对 workflow 有 **`workflow` scope**（classic PAT 勾选 `repo` + `workflow`；或 `gh auth refresh -s repo,workflow`）
3. **禁止**把 PAT 写入仓库、配置文件、截图或 commit message

## 打 tag 触发 Release

版本号与 `main.go` 的 `pluginVer`、`registry.json` 对齐，例如 `0.2.23`：

```bash
git checkout main
git pull
git tag -a v0.2.23 -m "release: v0.2.23"
git push origin v0.2.23
```

CI 在 tag `v*` 时：

1. 跑 unit test
2. 构建 6 平台（windows/arm64 可能因工具链 soft-fail）
3. 上传 zip 到 GitHub Release

## 校验

- Actions：`build` workflow 全绿（windows/arm64 允许跳过）
- Release 页存在 `cpa-xai-quota-guard_<goos>_<goarch>.zip`
- 解压路径符合 `plugins/<goos>/<goarch>/cpa-xai-quota-guard.*`

## 本地临时提权推 workflow（仅会话环境变量）

```powershell
# 切勿 echo / 写入文件
$env:GH_TOKEN = "<PAT_WITH_workflow>"
$env:ALL_PROXY = "socks5://..."   # 若需要
# 使用 gh 或 git credential，不要把 token 写进 remote URL 后提交
git push origin main
Remove-Item Env:GH_TOKEN -ErrorAction SilentlyContinue
```

或：`gh auth login` / `gh auth refresh -s repo,workflow` 后 `git push`。