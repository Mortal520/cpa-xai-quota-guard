# 第三方参考

## cpa-plugin-grok-panel

- 仓库：https://github.com/TizenryA/cpa-plugin-grok-panel
- License：MIT
- 吸收内容（思想与启发式，非整文件复制）：
  - Free / Super / Heavy 账号分类信号（note/label/prefix/subscription 字段）
  - 保护高级套餐的产品语义（UI 标记；删除仍受本插件白名单约束）
  - 面板统计与筛选交互的参考
- 未吸收 / 有意差异：
  - 本插件核心是 **额度冷却 + 死号删除**（usage.handle），不是纯面板
  - 巡查走上游探测与错误矩阵，而非仅 CPA runtime status
  - 删除通过 management API 真实执行，且 402/region/426 永不因分类误删

## 安装/发版模型（学习点）

从 grok-panel 吸收的**工程交付**而非业务逻辑：

1. 根目录 `registry.json` 作为 CPA `store-sources` 入口  
2. 多平台 zip 只上 GitHub Release，不进 git  
3. 文档写清：商店安装 / Release 手动 / 本机构建 三条路径  
4. 升级=换库文件+重启+核对 version；卸载=关配置+删库文件+重启

## Downstream fork feedback

- [NikaidouYui/cpa-xai-quota-guard](https://github.com/NikaidouYui/cpa-xai-quota-guard)：DefaultFreeLimit 1M→2M 与 state 升级强制对齐；已合入上游 0.3.11。
- [YaoZeChuan/cpa-xai-quota-guard](https://github.com/YaoZeChuan/cpa-xai-quota-guard)：CI/registry 自托管改名；zip 仍嵌套 goos/goarch，**未**合入（会商店 502）。

## Inspiration

- [ywddd/grok-inspection](https://github.com/ywddd/grok-inspection): management base URL resolution (env/default 127.0.0.1), loopback proxy bypass, management key UX.
