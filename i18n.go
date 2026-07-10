package main

import "os"

// 用户可见文案目录：en 优先（开源受众），zh 全量。
// 语言解析优先级：setLang(config) > $MOSHDROP_LANG > en。
var curLang string

func setLang(l string) {
	if l == "zh" || l == "en" {
		curLang = l
	}
}

// effectiveLang 只读地解析生效语言：setLang 设过的 curLang 优先，否则看环境变量，兜底 en。
// 关键：绝不写 curLang——msg 会被多个 goroutine（含迟到通知的 AfterFunc）并发调用，
// 惰性写全局变量会造成 data race（-race 在测试里因预先固定 curLang 而看不到）。
func effectiveLang() string {
	if curLang != "" {
		return curLang
	}
	if os.Getenv("MOSHDROP_LANG") == "zh" {
		return "zh"
	}
	return "en"
}

func msg(key string) string {
	m, ok := messages[key]
	if !ok {
		return key
	}
	if effectiveLang() == "zh" {
		return m[1]
	}
	return m[0]
}

// {en, zh}
var messages = map[string][2]string{
	// errors.go —— ssh 错误分类（保留原文首行：… — %s）
	"err.auth":    {"remote rejected ssh login (key auth broken?) — %s", "远端拒绝了 ssh 登录（免密密钥失效？）— %s"},
	"err.fsperm":  {"remote file permission denied (directory not writable?) — %s", "远端文件权限不足（目录不可写？）— %s"},
	"err.nospace": {"remote disk full — %s", "远端磁盘已满 — %s"},
	"err.resolve": {"cannot resolve hostname — %s", "解析不了主机名 — %s"},
	"err.network": {"cannot reach remote (network) — %s", "连不上远端（网络问题）— %s"},
	"err.ctlpath": {"ControlPath too long (please file an issue) — %s", "ControlPath 过长（请提 issue）— %s"},
	"err.hostkey": {"host key verification failed (ssh once manually first) — %s", "主机指纹校验失败（先手动 ssh 一次确认指纹）— %s"},
	// uploader.go
	"up.notarget": {"no ssh target derived from args; drag-upload disabled", "未能从参数确定 ssh 目标，拖拽上传已停用"},
	"up.dirparse": {"failed to resolve remote dir: %q", "解析远端目录失败: %q"},
	"up.timeout":  {"upload timed out (%s %.1f MB; link too slow or dead)", "上传超时（%s %.1f MB，网络太慢或已断开）"},
	"up.noecho":   {"remote did not echo final filename", "远端未回显文件名"},
	// proxy.go 通知
	"n.uploading":  {"uploading %.1f MB → %s …", "正在上传 %.1f MB → %s …"},
	"n.delivered":  {"delivered ✓", "已送达 ✓"},
	"n.failed":     {"upload failed; your original paste was passed through. Reason: %s", "上传失败，已放行本地路径。原因：%s"},
	"n.toobig":     {"drag is %.1f MB, over the %d MB limit; not auto-uploaded — scp it manually", "这次拖拽共 %.1f MB，超过 %d MB 上限，未自动上传——请手动 scp"},
	"n.clipslow":   {"clipboard had no image — that Ctrl+V was held back (slow clipboard read), not forwarded", "剪贴板里没有图片——这次 Ctrl+V 因剪贴板响应慢已被扣下，未转发"},
	"n.clipfailed": {"clipboard image upload failed: %s", "剪贴板图片上传失败：%s"},
	"n.cliptoobig": {"clipboard image is %.1f MB, over the %d MB limit; not uploaded — save it to a file and scp it", "剪贴板图片 %.1f MB，超过 %d MB 上限，未上传——请存成文件后手动 scp"},
	"r.queuefull":  {"too many transfers in flight", "在途传输过多"},
	"r.tmpgone":    {"temp file vanished", "临时文件消失"},
	// main.go
	"m.notarget": {"moshdrop: cannot determine ssh target; drag-upload disabled (mosh itself unaffected):", "moshdrop: 未能确定 ssh 目标，拖拽上传停用（mosh 本身不受影响）:"},
	// paste.go
	"p.usage": {"usage: moshdrop paste <host>   (upload clipboard image; remote path lands in your clipboard)", "用法: moshdrop paste <host>（上传剪贴板图片，远端路径回填剪贴板）"},
	"p.noimg": {"no image in clipboard (take a screenshot with Cmd+Ctrl+Shift+4 first)", "剪贴板里没有图片（先用 Cmd+Ctrl+Shift+4 截图到剪贴板）"},
	"p.ok":    {"remote path copied to clipboard — switch to your mosh window and paste.", "远端路径已进剪贴板——切到 mosh 窗口直接粘贴。"},
	// doctor.go
	"d.title":    {"moshdrop doctor", "moshdrop doctor"},
	"d.mosh":     {"mosh binary", "mosh 可执行"},
	"d.moshmiss": {"not found: %s (brew install mosh)", "找不到 %s（brew install mosh）"},
	"d.target":   {"ssh target", "ssh 目标"},
	"d.usage":    {"usage: moshdrop doctor <host>", "用法: moshdrop doctor <host>"},
	"d.via":      {"%s (channel: %s)", "%s（通道: %s）"},
	"d.ctl":      {"ControlPath length", "ControlPath 长度"},
	"d.conn":     {"ssh connectivity", "ssh 免密连通"},
	"d.connok":   {"%d ms, remote dir %s", "%d ms，远端目录 %s"},
	"d.hint":     {"   hint: run  ssh %s true  manually to check key auth", "   提示: 先手动跑一次  ssh %s true  确认免密可用"},
	"d.probe":    {"probe upload", "探针上传"},
	"d.e2e":      {"probe end-to-end check", "探针端到端校验"},
	"d.lastfail": {"   last failure: ", "   最近一次失败: "},
	"d.allpass":  {"All checks passed — drag-drop pipeline healthy.", "全部通过 — 拖拽链路健康。"},
	"d.nfail":    {"%d check(s) failed.\n", "%d 项未通过。\n"},
}
