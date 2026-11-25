package main

import (
	"fmt"
	"html/template"
	"net/http"
	"time"
)

var funcMap = template.FuncMap{
	"f1": func(v float64) string { return fmt.Sprintf("%.1f", v) },
	"f2": func(v float64) string { return fmt.Sprintf("%.2f", v) },
	// gu formats a value given in G with an adaptive unit: below 1 G it
	// is shown in M (1 G = 1024 M), otherwise in G.
	"gu": func(v float64) string {
		if v < 1 {
			m := v * 1024
			if m <= 0 {
				return "0 M"
			}
			if m < 10 {
				return fmt.Sprintf("%.1f M", m)
			}
			return fmt.Sprintf("%.0f M", m)
		}
		if v < 10 {
			return fmt.Sprintf("%.2f G", v)
		}
		return fmt.Sprintf("%.1f G", v)
	},
	"pct": func(v float64) string { return fmt.Sprintf("%.0f%%", v) },
	"barpct": func(used, quota float64) float64 {
		if quota <= 0 {
			return 0
		}
		p := used / quota * 100
		if p > 100 {
			return 100
		}
		return p
	},
	"ago": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		d := time.Since(t)
		if d < time.Minute {
			return "刚刚"
		}
		if d < time.Hour {
			return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
		}
		return fmt.Sprintf("%.1f 小时前", d.Hours())
	},
	"timef": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Format("01-02 15:04:05")
	},
}

const css = `
body{font-family:system-ui,sans-serif;background:#0f1419;color:#e6e6e6;margin:0;padding:20px}
h1{font-size:20px;margin:0 0 4px}h2{font-size:16px;margin:22px 0 8px}
.sub{color:#9aa4b2;font-size:13px;margin-bottom:16px}
nav{display:flex;gap:14px;align-items:center;margin-bottom:18px;flex-wrap:wrap}
nav a{color:#9aa4b2;text-decoration:none;font-size:15px;padding:6px 10px;border-radius:6px}
nav a.act{color:#fff;background:#2563eb}
nav form{margin-left:auto}
nav button{background:none;border:0;color:#9aa4b2;cursor:pointer;font-size:14px}
table{border-collapse:collapse;width:100%;background:#1a222c;border-radius:10px;overflow:hidden}
th,td{padding:10px 12px;text-align:left;font-size:14px;border-bottom:1px solid #26303c;white-space:nowrap}
th{color:#9aa4b2;font-weight:600;background:#151d26}
tr:last-child td{border-bottom:0}
.dot{display:inline-block;width:9px;height:9px;border-radius:50%;margin-right:6px}
.on{background:#22c55e}.off{background:#ef4444}
.bar{width:130px;height:8px;background:#26303c;border-radius:4px;display:inline-block;vertical-align:middle;margin-right:8px}
.bar>i{display:block;height:100%;border-radius:4px;background:#2563eb}
.bar>i.warn{background:#f59e0b}.bar>i.bad{background:#ef4444}
small{color:#9aa4b2}
.card{background:#1a222c;border-radius:10px;padding:18px;max-width:640px}
label{display:block;font-size:13px;color:#9aa4b2;margin:12px 0 4px}
input[type=text],input[type=password],input[type=number],select{width:100%;box-sizing:border-box;padding:10px;border-radius:6px;border:1px solid #374151;background:#0f1419;color:#e6e6e6;font-size:16px}
input[type=checkbox]{transform:scale(1.2);margin-right:8px}
button.btn{margin-top:16px;padding:9px 18px;min-height:44px;border:0;border-radius:6px;background:#2563eb;color:#fff;cursor:pointer;font-size:15px}
button.alt{background:#374151}
.ok{background:#14532d;color:#bbf7d0;padding:10px 14px;border-radius:8px;margin-bottom:14px;max-width:640px}
.err{background:#7f1d1d;color:#fecaca;padding:10px 14px;border-radius:8px;margin-bottom:14px;max-width:640px}
.hint{color:#9aa4b2;font-size:13px;line-height:1.6}
code{background:#26303c;padding:1px 5px;border-radius:4px;word-break:break-all}
.delbtn{background:none;border:1px solid #374151;color:#9aa4b2;border-radius:6px;padding:3px 12px;font-size:12px;cursor:pointer;margin-top:5px;min-height:28px}
.delbtn:hover{color:#f87171;border-color:#f87171}
@media (max-width:860px){
body{padding:12px}
h1{font-size:18px}h2{font-size:15px;margin:18px 0 6px}
.sub{font-size:12px}
nav{gap:4px;margin-bottom:14px}
nav a{font-size:14px;padding:9px 10px}
nav button{font-size:14px;padding:9px 6px}
.card{padding:14px}
.bar{width:84px;margin-right:6px}
table,tbody,tr,td{display:block;width:100%;box-sizing:border-box}
table{background:none;border-radius:0;overflow:visible}
table>tbody>tr:first-child{display:none}
tbody>tr{background:#1a222c;border-radius:10px;padding:8px 14px;margin-bottom:12px}
td{display:block;padding:7px 0;font-size:14px;white-space:normal;text-align:right;border-bottom:1px solid #232d38;overflow:hidden}
tr>td:last-child{border-bottom:0}
td::before{content:attr(data-label);float:left;color:#9aa4b2;font-size:12px;padding-top:2px}
td:first-child{text-align:left;padding:9px 0}
td:first-child::before{content:none}
td:first-child strong{font-size:16px}
}
`

func navHTML(active, csrf string) string {
	items := []struct{ href, name string }{
		{"/", "看板"}, {"/client", "客户端"}, {"/server", "服务端"}, {"/settings", "设置"},
	}
	s := "<nav>"
	for _, it := range items {
		cls := ""
		if it.href == active {
			cls = ` class="act"`
		}
		s += `<a href="` + it.href + `"` + cls + `>` + it.name + `</a>`
	}
	s += `<form method="post" action="/logout"><input type="hidden" name="csrf" value="` + csrf + `"><button type="submit">退出登录</button></form></nav>`
	return s
}

func page(active, csrf, body string) string {
	return `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<title>VPS 监控</title><style>` + css + `</style></head><body>` +
		navHTML(active, csrf) + body + `</body></html>`
}

// ---------- login ----------

func renderLogin(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := ""
	if errMsg != "" {
		err = `<div class="err">` + errMsg + `</div>`
	}
	fmt.Fprint(w, `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8">`+
		`<meta name="viewport" content="width=device-width, initial-scale=1">`+
		`<title>VPS 监控 - 登录</title><style>`+css+`</style></head>`+
		`<body><div style="display:flex;justify-content:center;padding-top:12vh"><div>`+
		`<h1>VPS 监控</h1><div class="sub">vpsmon `+version+`</div>`+err+
		`<form method="post" action="/login" class="card" style="width:min(300px,84vw)">`+
		`<label>用户名</label><input type="text" name="username" autocomplete="username">`+
		`<label>密码</label><input type="password" name="password" autocomplete="current-password">`+
		`<button class="btn" type="submit" style="width:100%">登录</button></form>`+
		`</div></div></body></html>`)
}

// ---------- dashboard ----------

type dashboardData struct {
	Nodes         []*nodeEntry
	ServerEnabled bool
	ClientEnabled bool
	CSRF          string
}

var dashTmpl = template.Must(template.New("dash").Funcs(funcMap).Parse(`
<h1>VPS 监控</h1>
<div class="sub">页面每 60 秒自动刷新 · 单位 G（小于 1 G 显示为 M）{{if not .ServerEnabled}} · 服务端未启用{{end}}{{if not .ClientEnabled}} · 本机客户端未启用{{end}}</div>
{{if and (not .ServerEnabled) (not .ClientEnabled)}}<div class="card hint">服务端与客户端都未启用。去 <a href="/server" style="color:#93c5fd">服务端</a> 打开汇总，或去 <a href="/client" style="color:#93c5fd">客户端</a> 把本机接入一台服务端。</div>{{end}}
<table>
<tr><th>节点</th><th>状态</th><th>CPU</th><th>内存</th><th>磁盘</th><th>当月流量（已用 / 配额）</th><th>剩余</th><th>过去 5 分钟</th><th>累计</th><th>下次重置</th><th>到期</th><th>最后汇报</th></tr>
{{range .Nodes}}
<tr>
<td><strong>{{.Report.Node}}</strong>{{if .Self}} <small>(本机)</small>{{end}}<br><small>{{.Report.Hostname}} · {{.Report.OS}}</small><form method="post" action="/nodes/delete" onsubmit="return confirm('从看板删除节点 {{.Report.Node}}？客户端不受影响，下次上报后会重新出现。')"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="node" value="{{.Report.Node}}"><button type="submit" class="delbtn">删除</button></form></td>
<td data-label="状态">{{if .Online}}<span class="dot on"></span>在线{{else}}<span class="dot off"></span>离线{{end}}</td>
<td data-label="CPU">{{pct .Report.CPUPct}}<br><small>5 分钟均值 {{pct .Report.CPUPct5m}}</small></td>
<td data-label="内存">{{gu .Report.MemUsedG}} / {{gu .Report.MemTotalG}}<br><small>{{pct .Report.MemPct}}</small></td>
<td data-label="磁盘">{{gu .Report.DiskUsedG}} / {{gu .Report.DiskTotalG}}<br><small>{{pct .Report.DiskPct}}</small></td>
<td data-label="当月流量"><span class="bar"><i style="width:{{barpct .Report.Traffic.CycleUsedG .Report.Traffic.QuotaG}}%" class="{{if gt (barpct .Report.Traffic.CycleUsedG .Report.Traffic.QuotaG) 95.0}}bad{{else if gt (barpct .Report.Traffic.CycleUsedG .Report.Traffic.QuotaG) 80.0}}warn{{end}}"></i></span>{{gu .Report.Traffic.CycleUsedG}} / {{gu .Report.Traffic.QuotaG}}</td>
<td data-label="剩余">{{gu .Report.Traffic.RemainingG}}</td>
<td data-label="过去 5 分钟">{{gu .Report.Traffic.Last5mG}}</td>
<td data-label="累计">{{gu .Report.Traffic.TotalG}}</td>
<td data-label="下次重置">{{.Report.Traffic.NextReset}}<br><small>每月 {{.Report.Traffic.ResetDay}} 日</small></td>
<td data-label="到期">{{if .Report.Traffic.Expiry}}{{.Report.Traffic.Expiry}}{{if .Report.Traffic.DaysLeft}}<br><small>剩 {{.Report.Traffic.DaysLeft}} 天</small>{{end}}{{else}}-{{end}}</td>
<td data-label="最后汇报">{{ago .ReceivedAt}}</td>
</tr>
{{else}}
<tr><td colspan="12">暂无节点汇报</td></tr>
{{end}}
</table>
<script>setTimeout(function(){location.reload()}, 60000)</script>`))

func renderDashboard(w http.ResponseWriter, s *session, d dashboardData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body := &strBuf{}
	if err := dashTmpl.Execute(body, d); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, page("/", s.CSRF, body.s))
}

// ---------- client page ----------

type clientPageData struct {
	Cfg          ClientConfig
	Saved        bool
	Running      bool
	LastAt       time.Time
	LastError    string
	Last         *Report
	Ifaces       []ifaceInfo
	DefaultIface string
}

var clientTmpl = template.Must(template.New("client").Funcs(funcMap).Parse(`
<h1>客户端设置</h1>
<div class="sub">本机作为客户端，每 {{.Cfg.ReportIntervalSec}} 秒向服务端汇报一次</div>
{{if .Saved}}<div class="ok">已保存并生效</div>{{end}}
<div class="card" style="margin-bottom:18px">
<div>状态：{{if .Running}}<span class="dot on"></span>运行中{{else}}<span class="dot off"></span>未运行{{end}}
{{if .Last}} · 最近汇报 {{timef .LastAt}}{{if .LastError}} · <span style="color:#fca5a5">失败：{{.LastError}}</span>{{end}}<br>
当月已用 {{gu .Last.Traffic.CycleUsedG}} / {{gu .Last.Traffic.QuotaG}} · 剩余 {{gu .Last.Traffic.RemainingG}} · 累计 {{gu .Last.Traffic.TotalG}}{{end}}</div>
</div>
<form method="post" action="/client" class="card">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<label><input type="checkbox" name="enabled" {{if .Cfg.Enabled}}checked{{end}}>启用客户端</label>
<label>服务端地址（不带路径，如 https://data.121436.xyz:18080）</label>
<input type="text" name="server_url" value="{{.Cfg.ServerURL}}">
<label>上报 token（与服务端的 ingest token 一致；留空 = 不修改）</label>
<input type="text" name="token" value="" placeholder="{{if .Cfg.Token}}已设置{{else}}未设置{{end}}">
<label>节点名</label>
<input type="text" name="node_name" value="{{.Cfg.NodeName}}">
<label>流量配额（G）</label>
<input type="number" step="0.1" name="quota_g" value="{{.Cfg.QuotaG}}">
<label>每月重置日（1-28）</label>
<input type="number" name="reset_day" value="{{.Cfg.ResetDay}}">
<label>到期日（YYYY-MM-DD，可空）</label>
<input type="text" name="expiry" value="{{.Cfg.Expiry}}">
<label>初始已用流量（G）— 仅在首次运行或重新播种时生效</label>
<input type="number" step="0.1" name="initial_used_g" value="{{.Cfg.InitialUsedG}}">
<label>计费口径</label>
<select name="traffic_mode">
<option value="both" {{if eq .Cfg.TrafficMode "both"}}selected{{end}}>both — 上下行相加</option>
<option value="out" {{if eq .Cfg.TrafficMode "out"}}selected{{end}}>out — 只算出站</option>
<option value="in" {{if eq .Cfg.TrafficMode "in"}}selected{{end}}>in — 只算入站</option>
<option value="max" {{if eq .Cfg.TrafficMode "max"}}selected{{end}}>max — 取上下行较大值</option>
</select>
<label>统计网卡</label>
<select name="interface">
<option value="auto" {{if or (eq .Cfg.Interface "auto") (eq .Cfg.Interface "")}}selected{{end}}>auto — 自动选择（默认路由网卡{{if .DefaultIface}}：{{.DefaultIface}}{{end}}）</option>
{{range .Ifaces}}<option value="{{.Name}}" {{if eq $.Cfg.Interface .Name}}selected{{end}}>{{.Label}}</option>{{end}}
</select>
<div class="hint">列表为本机实际读取到的网卡，累计流量是该网卡开机以来的总流量。一般保持 auto 即可；只有想单独统计某块网卡时才需要指定。</div>
<label>采样间隔（秒）</label>
<input type="number" name="sample_interval_sec" value="{{.Cfg.SampleIntervalSec}}">
<label>汇报间隔（秒）</label>
<input type="number" name="report_interval_sec" value="{{.Cfg.ReportIntervalSec}}">
<button class="btn" type="submit">保存并生效</button>
</form>
<h2>重新播种</h2>
<form method="post" action="/client/reseed" class="card" onsubmit="return confirm('将清空本机流量账本并按下面的已用量重新开始，确定？')">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<div class="hint">清空本机累计计数，把"当月已用"设为下面这个数（G，通常填面板上当前的已用量），然后重新开始记账。换套餐、改重置日、或账对不上时用。</div>
<label>当月已用（G）</label>
<input type="number" step="0.1" name="seed_g" value="0">
<button class="btn alt" type="submit">重新播种</button>
</form>`))

type clientView struct {
	clientPageData
	CSRF string
}

func renderClient(w http.ResponseWriter, s *session, d clientPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body := &strBuf{}
	if err := clientTmpl.Execute(body, clientView{d, s.CSRF}); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, page("/client", s.CSRF, body.s))
}

// ---------- server page ----------

type serverPageData struct {
	Cfg       ServerConfig
	Saved     bool
	NodeNum   int
	PanelPort int
	TLS       bool
}

var serverTmpl = template.Must(template.New("server").Funcs(funcMap).Parse(`
<h1>服务端设置</h1>
<div class="sub">本机作为服务端，接收其它机器的汇报并在看板展示（只展示，不汇总）</div>
{{if .Saved}}<div class="ok">已保存并生效</div>{{end}}
<div class="card" style="margin-bottom:18px">当前已收到 {{.NodeNum}} 个节点的汇报。客户端上报地址就是本面板地址加 <code>/api/report</code>，例如 <code>{{if .TLS}}https{{else}}http{{end}}://&lt;本机地址&gt;:{{.PanelPort}}/api/report</code>，客户端的服务端地址填到端口为止。</div>
<form method="post" action="/server" class="card">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<label><input type="checkbox" name="enabled" {{if .Cfg.Enabled}}checked{{end}}>启用服务端</label>
<label>上报 token（ingest token，客户端填这个）</label>
<input type="text" name="ingest_token" value="{{.Cfg.IngestToken}}">
<button class="btn" type="submit">保存</button>
</form>
<form method="post" action="/server/gentoken" style="max-width:640px">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<button class="btn alt" type="submit">随机生成新 token</button>
<span class="hint">生成后所有客户端都要改成新 token 才能继续汇报。</span>
</form>`))

type serverView struct {
	serverPageData
	CSRF string
}

func renderServer(w http.ResponseWriter, s *session, d serverPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body := &strBuf{}
	if err := serverTmpl.Execute(body, serverView{d, s.CSRF}); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, page("/server", s.CSRF, body.s))
}

// ---------- settings page ----------

type settingsData struct {
	Cfg   WebConfig
	Saved bool
	Error string
}

var settingsTmpl = template.Must(template.New("settings").Funcs(funcMap).Parse(`
<h1>设置</h1>
<div class="sub">面板账户与监听设置</div>
{{if .Saved}}<div class="ok">已保存</div>{{end}}
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
<form method="post" action="/settings" class="card">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<label>用户名</label>
<input type="text" name="username" value="{{.Cfg.Username}}">
<label>新密码（留空 = 不修改）</label>
<input type="password" name="new_password" autocomplete="new-password">
<label>确认新密码</label>
<input type="password" name="new_password2" autocomplete="new-password">
<label>API token（只读，供程序读取 /api/nodes；留空 = 不修改）</label>
<input type="text" name="api_token" value="" placeholder="{{if .Cfg.APIToken}}已设置{{else}}未设置{{end}}">
<label>WebUI 端口（改后立即在新端口监听，本页会给出新地址）</label>
<input type="number" name="port" value="{{.Cfg.Port}}">
<label>HTTPS 证书路径（fullchain，可空）</label>
<input type="text" name="tls_cert" value="{{.Cfg.TLSCert}}">
<label>HTTPS 私钥路径（可空）</label>
<input type="text" name="tls_key" value="{{.Cfg.TLSKey}}">
<button class="btn" type="submit">保存</button>
</form>
<form method="post" action="/settings/genapi" style="max-width:640px;margin-top:14px">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<button class="btn alt" type="submit">随机生成 API token</button>
</form>`))

type settingsView struct {
	settingsData
	CSRF string
}

func renderSettings(w http.ResponseWriter, s *session, d settingsData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body := &strBuf{}
	if err := settingsTmpl.Execute(body, settingsView{d, s.CSRF}); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, page("/settings", s.CSRF, body.s))
}

// ---------- generic message page ----------

func renderMessage(w http.ResponseWriter, s *session, title, bodyText string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, page("/", s.CSRF, `<h1>`+template.HTMLEscapeString(title)+`</h1><div class="card hint">`+template.HTMLEscapeString(bodyText)+`</div>`))
}

// strBuf is a tiny strings.Builder wrapper for template execution.
type strBuf struct{ s string }

func (b *strBuf) Write(p []byte) (int, error) {
	b.s += string(p)
	return len(p), nil
}
