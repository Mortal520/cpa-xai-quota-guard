# CI workflow 状态

## 已完成（2026-07-14）

- 远端 `main` 已包含商店兼容 CI：`c7753e9` `ci: package store-compatible release assets`
- 打包约定：
  1. zip 名：`cpa-xai-quota-guard_{version}_{goos}_{goarch}.zip`
  2. 库文件在 zip **根目录**
  3. Release 附带 `checksums.txt`
- 备份：`docs/ci-build.yml.store-compatible`（与 workflow 一致时可作为参考）

## 发版注意

- 已发布的 **v0.3.10** 资产布局正确，无需重发
- 下次 `git tag v0.x.y && git push origin v0.x.y` 会走新 CI
- **不要**再上传旧命名 `cpa-xai-quota-guard_linux_amd64.zip` 或嵌套 `linux/amd64/` 的 zip

## 权限说明

推送 `.github/workflows/*` 需要 `repo` + `workflow` scope。  
**禁止**把 PAT 写入 remote URL 或提交进仓库；仅会话环境变量注入。