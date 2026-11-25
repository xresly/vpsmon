package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", a.handleLogin)
	mux.HandleFunc("/logout", a.requireAuth(a.handleLogout))
	mux.HandleFunc("/", a.requireAuth(a.handleDashboard))
	mux.HandleFunc("/client", a.requireAuth(a.handleClient))
	mux.HandleFunc("/client/reseed", a.requireAuth(a.handleReseed))
	mux.HandleFunc("/server", a.requireAuth(a.handleServerPage))
	mux.HandleFunc("/server/gentoken", a.requireAuth(a.handleGenIngestToken))
	mux.HandleFunc("/settings", a.requireAuth(a.handleSettings))
	mux.HandleFunc("/settings/genapi", a.requireAuth(a.handleGenAPIToken))
	mux.HandleFunc("/api/report", a.handleReport)
	mux.HandleFunc("/api/nodes", a.handleNodes)
	return mux
}

// ---------- auth ----------

func (a *App) currentSession(r *http.Request) *session {
	c, err := r.Cookie("vpsmon_session")
	if err != nil {
		return nil
	}
	a.mu.RLock()
	s, ok := a.sessions[c.Value]
	a.mu.RUnlock()
	if !ok || time.Now().After(s.Expires) {
		return nil
	}
	return s
}

func (a *App) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/client" && r.URL.Path != "/server" &&
			r.URL.Path != "/settings" && r.URL.Path != "/logout" &&
			!strings.HasPrefix(r.URL.Path, "/client/") && !strings.HasPrefix(r.URL.Path, "/server/") &&
			!strings.HasPrefix(r.URL.Path, "/settings/") {
			http.NotFound(w, r)
			return
		}
		if a.currentSession(r) == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

func (a *App) checkCSRF(w http.ResponseWriter, r *http.Request) bool {
	s := a.currentSession(r)
	if s == nil || r.FormValue("csrf") != s.CSRF {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *App) loginLocked(ip string) bool {
	a.failsMu.Lock()
	defer a.failsMu.Unlock()
	now := time.Now()
	var recent []time.Time
	for _, t := range a.fails[ip] {
		if now.Sub(t) < 10*time.Minute {
			recent = append(recent, t)
		}
	}
	a.fails[ip] = recent
	return len(recent) >= 5
}

func (a *App) loginFailed(ip string) {
	a.failsMu.Lock()
	a.fails[ip] = append(a.fails[ip], time.Now())
	a.failsMu.Unlock()
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		ip := clientIP(r)
		if a.loginLocked(ip) {
			http.Error(w, "尝试次数过多，请 10 分钟后再试", http.StatusTooManyRequests)
			return
		}
		cfg := a.getCfg()
		user := r.FormValue("username")
		pass := r.FormValue("password")
		if user != cfg.Web.Username || !verifyPassword(pass, cfg.Web.Password) {
			a.loginFailed(ip)
			renderLogin(w, "用户名或密码不正确")
			return
		}
		sid := randomTokenHex(32)
		s := &session{Username: user, CSRF: randomTokenHex(16), Expires: time.Now().Add(7 * 24 * time.Hour)}
		a.mu.Lock()
		a.sessions[sid] = s
		a.mu.Unlock()
		http.SetCookie(w, &http.Cookie{
			Name:     "vpsmon_session",
			Value:    sid,
			Path:     "/",
			MaxAge:   7 * 24 * 3600,
			HttpOnly: true,
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if a.currentSession(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	renderLogin(w, "")
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie("vpsmon_session"); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "vpsmon_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---------- ingest + API ----------

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && h[:7] == "Bearer " {
		return h[7:]
	}
	return ""
}

func (a *App) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := a.getCfg()
	if !cfg.Server.Enabled {
		http.Error(w, "server role disabled", http.StatusForbidden)
		return
	}
	if !tokenOK(bearer(r), cfg.Server.IngestToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var rep Report
	if err := json.Unmarshal(body, &rep); err != nil || rep.Node == "" {
		http.Error(w, "bad report", http.StatusBadRequest)
		return
	}
	a.store.put(rep)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"ok":true}`)
}

func (a *App) handleNodes(w http.ResponseWriter, r *http.Request) {
	cfg := a.getCfg()
	if a.currentSession(r) == nil && !tokenOK(bearer(r), cfg.Web.APIToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"generated_at": time.Now().Format(time.RFC3339),
		"nodes":        a.mergedNodes(),
	})
}

// ---------- form helpers ----------

func fStr(r *http.Request, name, old string) string {
	if v := strings.TrimSpace(r.FormValue(name)); v != "" {
		return v
	}
	return old
}

func fFloat(r *http.Request, name string, old float64) float64 {
	if v := strings.TrimSpace(r.FormValue(name)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return old
}

func fInt(r *http.Request, name string, old int) int {
	if v := strings.TrimSpace(r.FormValue(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return old
}

func fBool(r *http.Request, name string) bool {
	return r.FormValue(name) == "on"
}

// ---------- pages ----------

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	cfg := a.getCfg()
	renderDashboard(w, a.currentSession(r), dashboardData{
		Nodes:         a.mergedNodes(),
		ServerEnabled: cfg.Server.Enabled,
		ClientEnabled: cfg.Client.Enabled,
	})
}

func (a *App) handleClient(w http.ResponseWriter, r *http.Request) {
	s := a.currentSession(r)
	if r.Method == http.MethodPost {
		if !a.checkCSRF(w, r) {
			return
		}
		cfg := a.getCfg()
		nc := *cfg
		c := &nc.Client
		c.Enabled = fBool(r, "enabled")
		c.ServerURL = strings.TrimRight(fStr(r, "server_url", c.ServerURL), "/")
		if v := strings.TrimSpace(r.FormValue("token")); v != "" {
			c.Token = v
		}
		c.NodeName = fStr(r, "node_name", c.NodeName)
		c.QuotaG = fFloat(r, "quota_g", c.QuotaG)
		c.ResetDay = fInt(r, "reset_day", c.ResetDay)
		c.Expiry = strings.TrimSpace(r.FormValue("expiry"))
		c.InitialUsedG = fFloat(r, "initial_used_g", c.InitialUsedG)
		c.TrafficMode = fStr(r, "traffic_mode", c.TrafficMode)
		c.Interface = fStr(r, "interface", c.Interface)
		c.SampleIntervalSec = fInt(r, "sample_interval_sec", c.SampleIntervalSec)
		c.ReportIntervalSec = fInt(r, "report_interval_sec", c.ReportIntervalSec)
		nc.applyDefaults()
		if err := a.updateCfg(&nc); err != nil {
			http.Error(w, "保存失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		a.applyClient()
		http.Redirect(w, r, "/client?saved=1", http.StatusSeeOther)
		return
	}
	cfg := a.getCfg()
	running, lastAt, lastErr, last := a.agent.snapshot()
	renderClient(w, s, clientPageData{
		Cfg:          cfg.Client,
		Saved:        r.URL.Query().Get("saved") == "1",
		Running:      running,
		LastAt:       lastAt,
		LastError:    lastErr,
		Last:         last,
		Ifaces:       listInterfaces(),
		DefaultIface: defaultInterface(),
	})
}

func (a *App) handleReseed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !a.checkCSRF(w, r) {
		return
	}
	seed := fFloat(r, "seed_g", 0)
	if err := a.reseedClient(seed); err != nil {
		http.Error(w, "重新播种失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/client?saved=1", http.StatusSeeOther)
}

func (a *App) handleServerPage(w http.ResponseWriter, r *http.Request) {
	s := a.currentSession(r)
	if r.Method == http.MethodPost {
		if !a.checkCSRF(w, r) {
			return
		}
		cfg := a.getCfg()
		nc := *cfg
		nc.Server.Enabled = fBool(r, "enabled")
		if v := strings.TrimSpace(r.FormValue("ingest_token")); v != "" {
			nc.Server.IngestToken = v
		}
		if err := a.updateCfg(&nc); err != nil {
			http.Error(w, "保存失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/server?saved=1", http.StatusSeeOther)
		return
	}
	cfg := a.getCfg()
	renderServer(w, s, serverPageData{
		Cfg:      cfg.Server,
		Saved:    r.URL.Query().Get("saved") == "1",
		NodeNum:  len(a.mergedNodes()),
		PanelPort: cfg.Web.Port,
		TLS:      cfg.Web.TLSCert != "",
	})
}

func (a *App) handleGenIngestToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !a.checkCSRF(w, r) {
		return
	}
	cfg := a.getCfg()
	nc := *cfg
	nc.Server.IngestToken = randomTokenHex(16)
	if err := a.updateCfg(&nc); err != nil {
		http.Error(w, "保存失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/server?saved=1", http.StatusSeeOther)
}

func (a *App) handleGenAPIToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !a.checkCSRF(w, r) {
		return
	}
	cfg := a.getCfg()
	nc := *cfg
	nc.Web.APIToken = randomTokenHex(16)
	if err := a.updateCfg(&nc); err != nil {
		http.Error(w, "保存失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	s := a.currentSession(r)
	cfg := a.getCfg()
	if r.Method == http.MethodPost {
		if !a.checkCSRF(w, r) {
			return
		}
		nc := *cfg
		nc.Web.Username = fStr(r, "username", nc.Web.Username)
		if pw := r.FormValue("new_password"); pw != "" {
			if pw != r.FormValue("new_password2") {
				renderSettings(w, s, settingsData{Cfg: nc.Web, Error: "两次输入的新密码不一致"})
				return
			}
			h, err := hashPassword(pw)
			if err != nil {
				http.Error(w, "密码处理失败", http.StatusInternalServerError)
				return
			}
			nc.Web.Password = h
		}
		if v := strings.TrimSpace(r.FormValue("api_token")); v != "" {
			nc.Web.APIToken = v
		}
		oldPort, oldTLS := cfg.Web.Port, cfg.Web.TLSCert != "" && cfg.Web.TLSKey != ""
		nc.Web.Port = fInt(r, "port", nc.Web.Port)
		nc.Web.TLSCert = strings.TrimSpace(r.FormValue("tls_cert"))
		nc.Web.TLSKey = strings.TrimSpace(r.FormValue("tls_key"))
		nc.applyDefaults()
		if err := a.updateCfg(&nc); err != nil {
			http.Error(w, "保存失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		newTLS := nc.Web.TLSCert != "" && nc.Web.TLSKey != ""
		if nc.Web.Port != oldPort || newTLS != oldTLS {
			if err := a.restartHTTP(); err != nil {
				renderMessage(w, s, "设置已保存，但新监听启动失败", "错误："+err.Error()+"。旧配置可能已停止监听，请用 vpsmon menu 检查，或重启服务。")
				return
			}
			scheme := "http"
			if newTLS {
				scheme = "https"
			}
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			url := fmt.Sprintf("%s://%s:%d/settings", scheme, host, nc.Web.Port)
			renderMessage(w, s, "设置已保存", "面板已在新地址监听："+url)
			return
		}
		http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
		return
	}
	renderSettings(w, s, settingsData{Cfg: cfg.Web, Saved: r.URL.Query().Get("saved") == "1"})
}
