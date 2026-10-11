# moshdrop backlog（v1.1+）

用户拍板（2026-07-03）：

- **大文件回问确认 + 可终止**（原⑦，用户点名 v1.1）：超过阈值先问"确定上传吗"，且上传中要有终止手段。难点：确认交互发生在终端会话内（不能破坏透传）——候选方案：通知中心带按钮 / 终端外小弹窗 / 约定按键。设计前先积累真实大文件使用数据（events.log）。
- **注入防错位**（原⑧）：2026-10-11 已获批准并在本地实现，尚未安装/发布。接受操作起默认 5 秒内自动插入；迟到成功仅保存结果，通知的 Copy path 或本机 `moshdrop pending --copy <id>` 明确复制；迟到失败不回放输入。原生生产包 UI/升级及实际发布安装验收待另行批准。详见 [late-upload-delivery](../late-upload-delivery.md)。
- Linux 本机端支持（有真实需求再做）
- 目录拖拽（tar 打包）——审计评审明确 rejected，除非用户反馈翻案

会话内 Ctrl+V 贴图（2026-07-10 对抗评审 GO_WITH_SCOPE，已落地）观察项：

- **退出条款**：events.log 已按来源分账（src=clipboard/paste-cmd）。约一个月后（2026-08 中）回看用量，clipboard 路径趋零 → paste_key 默认改 off 或撤功能。
- 无图 Ctrl+V 延迟（实测 60-110ms）嫌钝再做：编译版 Swift 剪贴板探测 helper（warm ≤10ms）；仍不够再考虑 changeCount 异步轮询归零延迟。
- vim 可视块 / quoted-insert / tmux copy-mode 用户报告真实碰撞再做：双击透传逃生门或自定义触发键。
- 原⑧注入防错位：禁止迟到图片上传**自动**插入或**自动**把远端路径写回剪贴板（会覆盖源图）。2026-10-11 用户批准的明确 Copy path / pending --copy 操作可以复制；关闭通知或点击通知正文不复制。
- 自动侦测输出流找 URL/图片之类——**永久否决**（远端可控字节驱动本地动作，信任模型缺陷，见 2026-07-10 反向打开裁决）。
- Windows/Linux 本地端支持（含 Ctrl+V 贴图）：作者无测试条件，等社区贡献（PRs welcome 已挂 README）。

发布后运营：

- mosh#1184 / #1135 留言（转 public 后）
- HN / r/commandline / V2EX 分享帖
- README 演示 GIF（用户实拍 15s 屏录 → ffmpeg 转 GIF）
