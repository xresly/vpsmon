package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------- node store (server side, in memory) ----------

type nodeEntry struct {
	Report     Report    `json:"report"`
	ReceivedAt time.Time `json:"received_at"`
	Online     bool      `json:"online"`
	Self       bool      `json:"self,omitempty"`
}

type nodeStore struct {
	mu    sync.RWMutex
	nodes map[string]*nodeEntry
	file  string // persistence path; "" = in-memory only
}

func newNodeStore() *nodeStore { return &nodeStore{nodes: map[string]*nodeEntry{}} }

// setFile enables disk persistence and restores any previously saved
// node data, so the dashboard survives server restarts.
func (s *nodeStore) setFile(path string) {
	s.file = path
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var entries []*nodeEntry
	if json.Unmarshal(b, &entries) != nil {
		return
	}
	s.mu.Lock()
	for _, e := range entries {
		if e != nil && e.Report.Node != "" {
			s.nodes[e.Report.Node] = e
		}
	}
	n := len(s.nodes)
	s.mu.Unlock()
	if n > 0 {
		log.Printf("vpsmon: restored %d node(s) from %s", n, path)
	}
}

// saveLocked writes the whole node set to disk atomically.
// The caller must hold s.mu (write lock).
func (s *nodeStore) saveLocked() {
	if s.file == "" {
		return
	}
	entries := make([]*nodeEntry, 0, len(s.nodes))
	for _, e := range s.nodes {
		entries = append(entries, e)
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return
	}
	tmp := s.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		_ = os.Rename(tmp, s.file)
	}
}

func (s *nodeStore) put(rep Report) {
	s.mu.Lock()
	s.nodes[rep.Node] = &nodeEntry{Report: rep, ReceivedAt: time.Now()}
	s.saveLocked()
	s.mu.Unlock()
}

// delete removes a node from the dashboard. The client is unaffected:
// its next report simply adds the node back.
func (s *nodeStore) delete(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[name]; !ok {
		return false
	}
	delete(s.nodes, name)
	s.saveLocked()
	return true
}

func (s *nodeStore) snapshot() []*nodeEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*nodeEntry
	for _, e := range s.nodes {
		cp := *e
		cp.Online = time.Since(e.ReceivedAt) <= 10*time.Minute
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Report.Node < out[j].Report.Node })
	return out
}

// ---------- sessions ----------

type session struct {
	Username string
	CSRF     string
	Expires  time.Time
}

// ---------- the application ----------

type App struct {
	cfgPath string

	mu  sync.RWMutex
	cfg *AppConfig

	store    *nodeStore
	sessions map[string]*session
	failsMu  sync.Mutex
	fails    map[string][]time.Time // login failures per IP

	agentMu     sync.Mutex
	agent       *agentStatus
	agentCancel context.CancelFunc

	selfMu sync.RWMutex
	self   *nodeEntry

	httpMu   sync.Mutex
	srv      *http.Server
	srvPlain *http.Server // plain-HTTP server in dual-protocol mode
	ln       net.Listener
}

func newApp(cfgPath string, cfg *AppConfig) *App {
	a := &App{
		cfgPath:  cfgPath,
		cfg:      cfg,
		store:    newNodeStore(),
		sessions: map[string]*session{},
		fails:    map[string][]time.Time{},
		agent:    &agentStatus{},
	}
	if cfg.StateFile != "" {
		a.store.setFile(filepath.Join(filepath.Dir(cfg.StateFile), "server-nodes.json"))
	}
	return a
}

func (a *App) getCfg() *AppConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

// updateCfg replaces the in-memory config and persists it.
func (a *App) updateCfg(c *AppConfig) error {
	a.mu.Lock()
	a.cfg = c
	a.mu.Unlock()
	return c.save(a.cfgPath)
}

// ---------- agent lifecycle ----------

// applyClient (re)starts the client agent according to the current
// config. Called at boot and after every client-settings save.
func (a *App) applyClient() {
	a.agentMu.Lock()
	defer a.agentMu.Unlock()
	if a.agentCancel != nil {
		a.agentCancel()
		a.agentCancel = nil
	}
	cfg := a.getCfg()
	if !cfg.Client.Enabled {
		return
	}
	cc := cfg.Client
	if cc.NodeName == "" || cc.ServerURL == "" || cc.Token == "" {
		log.Printf("vpsmon: client enabled but node_name/server_url/token incomplete; agent not started")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.agentCancel = cancel
	stateFile := cfg.StateFile
	go runAgent(ctx, cc, stateFile, a.agent, func(rep Report) {
		a.selfMu.Lock()
		a.self = &nodeEntry{Report: rep, ReceivedAt: time.Now(), Self: true, Online: true}
		a.selfMu.Unlock()
	}, func(next string) {
		// A rollover advanced the reset schedule: persist the new
		// next-reset moment so the settings page and restarts see
		// it. Config-only update — the agent keeps running.
		c := a.getCfg()
		cp := *c
		cp.Client.NextReset = next
		if err := a.updateCfg(&cp); err != nil {
			log.Printf("vpsmon: persisting next reset: %v", err)
		}
	})
}

// reseedClient resets the agent's traffic accounting: deletes the
// state file and restarts the agent with a new initial-used seed.
func (a *App) reseedClient(seedG float64) error {
	a.agentMu.Lock()
	if a.agentCancel != nil {
		a.agentCancel()
		a.agentCancel = nil
	}
	a.agentMu.Unlock()

	cfg := a.getCfg()
	nc := *cfg
	nc.Client.InitialUsedG = seedG
	if err := a.updateCfg(&nc); err != nil {
		return err
	}
	if err := removeStateFile(cfg.StateFile); err != nil {
		return err
	}
	a.applyClient()
	return nil
}

func removeStateFile(path string) error {
	err := removeFileIfExists(path)
	if err != nil {
		return err
	}
	return removeFileIfExists(path + ".tmp")
}

// ---------- merged node view (server store + local self report) ----------

func (a *App) mergedNodes() []*nodeEntry {
	out := a.store.snapshot()
	a.selfMu.RLock()
	self := a.self
	a.selfMu.RUnlock()
	if self != nil {
		cp := *self
		cp.Online = time.Since(cp.ReceivedAt) <= 10*time.Minute
		found := false
		for i, e := range out {
			if e.Report.Node == cp.Report.Node {
				found = true
				if cp.ReceivedAt.After(e.ReceivedAt) {
					cp.Self = true
					out[i] = &cp
				} else {
					out[i].Self = true
				}
				break
			}
		}
		if !found {
			out = append(out, &cp)
			sort.Slice(out, func(i, j int) bool { return out[i].Report.Node < out[j].Report.Node })
		}
	}
	return out
}

// ---------- HTTP supervisor ----------

// serveOn starts serving the panel on ln. With a TLS certificate
// configured, the port is dual-protocol: each connection's first
// byte decides HTTPS vs HTTP (0x16 opens a TLS handshake). Plain
// HTTP stays available for local/IP access, while HTTP requests
// addressed to a domain name are redirected to HTTPS. Without a
// certificate, the port is plain HTTP only.
func (a *App) serveOn(ln net.Listener, cfg *AppConfig) (srv, plain *http.Server, err error) {
	if cfg.Web.TLSCert == "" || cfg.Web.TLSKey == "" {
		srv = &http.Server{Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Printf("vpsmon: http serve: %v", err)
			}
		}()
		return srv, nil, nil
	}
	if _, err := tls.LoadX509KeyPair(cfg.Web.TLSCert, cfg.Web.TLSKey); err != nil {
		return nil, nil, fmt.Errorf("loading TLS key pair: %w", err)
	}
	certFile, keyFile := cfg.Web.TLSCert, cfg.Web.TLSKey
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
	}
	d := newDemux(ln)
	srv = &http.Server{Handler: a.routes(), TLSConfig: tlsCfg, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ServeTLS(d.tlsL, "", ""); err != nil && err != http.ErrServerClosed {
			log.Printf("vpsmon: https serve: %v", err)
		}
	}()
	plain = &http.Server{Handler: a.plainHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := plain.Serve(d.httpL); err != nil && err != http.ErrServerClosed {
			log.Printf("vpsmon: http serve: %v", err)
		}
	}()
	return srv, plain, nil
}

// plainHandler serves the panel over plain HTTP, except that
// requests addressed to a public domain name are permanently
// redirected to the HTTPS version of the same URL.
func (a *App) plainHandler() http.Handler {
	app := a.routes()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isDomainHost(r.Host) {
			http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusPermanentRedirect)
			return
		}
		app.ServeHTTP(w, r)
	})
}

// isDomainHost reports whether a Host header names a public domain
// (as opposed to local access: an IP literal, localhost, a bare
// hostname, or a LAN suffix like .local/.lan/.home/.internal).
func isDomainHost(hostport string) bool {
	h := hostport
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.Trim(strings.TrimSuffix(h, "."), "[]")
	if h == "" || strings.EqualFold(h, "localhost") {
		return false
	}
	if net.ParseIP(h) != nil {
		return false
	}
	lh := strings.ToLower(h)
	for _, suf := range []string{".local", ".lan", ".home", ".internal", ".intranet"} {
		if strings.HasSuffix(lh, suf) {
			return false
		}
	}
	return strings.Contains(h, ".")
}

// ---------- dual-protocol demultiplexer ----------

type peekConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekConn) Read(b []byte) (int, error) { return c.r.Read(b) }

type chanListener struct {
	ch   chan net.Conn
	addr net.Addr
	done chan struct{}
	once sync.Once
}

func newChanListener(addr net.Addr) *chanListener {
	return &chanListener{ch: make(chan net.Conn, 16), addr: addr, done: make(chan struct{})}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }

type demux struct {
	ln    net.Listener
	tlsL  *chanListener
	httpL *chanListener
}

func newDemux(ln net.Listener) *demux {
	d := &demux{ln: ln, tlsL: newChanListener(ln.Addr()), httpL: newChanListener(ln.Addr())}
	go d.loop()
	return d
}

func (d *demux) loop() {
	for {
		c, err := d.ln.Accept()
		if err != nil {
			d.tlsL.Close()
			d.httpL.Close()
			return
		}
		go d.route(c)
	}
}

func (d *demux) route(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	b, err := br.Peek(1)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return
	}
	target := d.httpL
	if b[0] == 0x16 { // TLS handshake record type
		target = d.tlsL
	}
	pc := &peekConn{Conn: c, r: br}
	select {
	case target.ch <- pc:
	case <-target.done:
		c.Close()
	}
}

func (a *App) startHTTP() error {
	a.httpMu.Lock()
	defer a.httpMu.Unlock()
	cfg := a.getCfg()
	addr := fmt.Sprintf(":%d", cfg.Web.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv, plain, err := a.serveOn(ln, cfg)
	if err != nil {
		ln.Close()
		return err
	}
	a.srv, a.srvPlain, a.ln = srv, plain, ln
	scheme := "http"
	if plain != nil {
		scheme = "http+https (domains redirect to https)"
	} else if cfg.Web.TLSCert != "" {
		scheme = "https"
	}
	log.Printf("vpsmon %s panel listening on %s://%s", version, scheme, addr)
	return nil
}

// restartHTTP swaps the listener (used after port/TLS changes). The new
// listener is created first; if that fails the old one keeps running.
func (a *App) restartHTTP() error {
	a.httpMu.Lock()
	old, oldPlain, oldLn := a.srv, a.srvPlain, a.ln
	a.httpMu.Unlock()

	cfg := a.getCfg()
	addr := fmt.Sprintf(":%d", cfg.Web.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv, plain, err := a.serveOn(ln, cfg)
	if err != nil {
		ln.Close()
		return err
	}
	a.httpMu.Lock()
	a.srv, a.srvPlain, a.ln = srv, plain, ln
	a.httpMu.Unlock()
	for _, s := range []*http.Server{old, oldPlain} {
		if s != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = s.Shutdown(ctx)
			cancel()
		}
	}
	if oldLn != nil {
		oldLn.Close()
	}
	log.Printf("vpsmon: panel now listening on %s", addr)
	return nil
}

func (a *App) shutdownHTTP() {
	a.httpMu.Lock()
	srv, plain, ln := a.srv, a.srvPlain, a.ln
	a.httpMu.Unlock()
	for _, s := range []*http.Server{srv, plain} {
		if s != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = s.Shutdown(ctx)
			cancel()
		}
	}
	if ln != nil {
		ln.Close()
	}
}
