# moshdrop v0.1 "feels like native terminal" 验收记录

日期：2026-07-03　环境：macOS (本机) ↔ ccc (真实远端, Tailscale)

## 自动化验收（expect + 真实 mosh + 真实 ccc）

| 项 | moshdrop | 裸 mosh 基线 | 结论 |
|---|---|---|---|
| 键击往返 RTT（20 次均值） | 3.214 ms | 3.08 ms | **代理开销 ≈0.13ms，不可感知** — PASS |
| Ctrl-C 透传（远端 trap INT） | 中断成功 | 中断成功 | PASS |
| resize 透传（80→111 列） | 远端 tput cols 跟随 | 同 | PASS |
| 退出行为 | exit 0 | exit 0 | 与原生一致（mosh 本身不透传远端退出码）— PASS |
| 拦截-上传-注入全链路 | 带空格/中文名文件真传 ccc，注入远端路径，内容逐字节校验 | 不适用（mosh 无此能力） | PASS（TestProxyInterceptE2E） |
| 非文件粘贴零干扰 | 原样放行 | — | PASS（单元+管道测试） |
| 透传保真 | 200 轮随机字节模糊 + 全边界切碎矩阵，逐字节无损 | — | PASS |

## 过程中发现并修复的产品问题

1. **ControlPath 超长**（macOS unix socket ≥104 字节报错）→ 自动回退 `/tmp/moshdrop-<uid>`。
2. **stdin EOF 后子进程滞留** → 双 EOT + SIGHUP + KILL 分级兜底。
3. **0×0 pty 使 mosh 客户端崩溃**（`Error: vector`，裸 mosh 同样崩，属 mosh 自身 bug）→ 尺寸不可得时兜底 80×24。副作用是 moshdrop 反而能让 mosh 在非 tty 环境跑起来（裸 mosh 直接 tcgetattr 失败）。

## 环境备注

- `TERM=xterm-ghostty` 时 ccc 缺 terminfo（与 moshdrop 无关，用户日常入口正常）。
- mosh 对"瞬时命令"偶发遗留 mosh-server（裸 mosh 同样），非 moshdrop 缺陷。

## 用户真机验收（2026-07-03 完成）

- [x] **shell 拖拽**：浮窗拖进 moshdrop 会话 → 注入 `/Users/ccc/.moshdrop/Screenshot…png` 远端路径，`file` 验证 2.4MB 合法 PNG（3420×2214）— PASS
- [x] **CC 拖拽**：拖图进远端 CC → `[Image #N]` → CC 像素级评估：3 个不可编造细节全对、路径/大小/magic number 验证、新鲜度 41 秒 → CC 判定 **PASS ✅**
- [x] 带空格文件名（截图默认名即含空格）— PASS
- [ ] 多文件拖拽（日用中顺带验证）
- [ ] 断线重连（日用中顺带验证）
- [ ] 日用一周 dogfood（进行中，起于 2026-07-03）

**v0.1 验收结论：通过。** "feels like native terminal"（键击开销 0.13ms + 全透传一致性）+ 核心场景（拖浮窗喂远端 CC）双达标。
