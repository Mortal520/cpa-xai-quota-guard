# 界面截图

本目录存放 README 与文档用的管理页截图。

## 当前文件

| 文件 | 说明 |
|------|------|
| `dashboard.svg` | 状态栏 / 额度概览示意（矢量占位，非真实截图） |
| `patrol.svg` | 主动巡查与处理日志示意 |
| `accounts.svg` | 账号状态表示意 |

## 如何更新为真实截图

1. 打开 CPA 管理中心 → 插件菜单 **xAI Quota Guard**（或 `.../cpa-xai-quota-guard/index.html`）
2. 浏览器全页截图（推荐 1280×800 左右）
3. 导出为 PNG：`dashboard.png` / `patrol.png` / `accounts.png`
4. 脱敏：遮盖 management key、完整邮箱、代理 URL、真实 auth_index
5. 替换本目录文件后，更新 README 中对应图片路径（PNG 优先于 SVG）

> 切勿提交含密钥、Cookie、完整 token 的截图。