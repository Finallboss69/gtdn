package main

import (
	_ "embed"
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

//go:embed relay.html
var relayHTML []byte

// ─── Config ───────────────────────────────────────────────────────────────────

type NodeConfig struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	LoginAddr string `json:"login_addr"`
	GameAddr  string `json:"game_addr"`
	AdminURL  string `json:"admin_url"`
	Token     string `json:"token"`
}

type RelayConfig struct {
	LoginLocal string       `json:"login_local"`
	GameLocal  string       `json:"game_local"`
	Nodes      []NodeConfig `json:"nodes"`
}

// ─── Probed node ──────────────────────────────────────────────────────────────

type probedNode struct {
	NodeConfig
	latency time.Duration
	ok      bool
}

func probeNode(n NodeConfig, timeout time.Duration) probedNode {
	if n.LoginAddr == "" {
		return probedNode{NodeConfig: n, ok: false}
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", n.LoginAddr, timeout)
	if err != nil {
		return probedNode{NodeConfig: n, ok: false}
	}
	lat := time.Since(start)
	conn.Close()
	return probedNode{NodeConfig: n, latency: lat, ok: true}
}

func probeAll(nodes []NodeConfig, timeout time.Duration) []probedNode {
	results := make([]probedNode, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func(idx int, node NodeConfig) {
			defer wg.Done()
			results[idx] = probeNode(node, timeout)
		}(i, n)
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool {
		if results[i].ok != results[j].ok {
			return results[i].ok
		}
		return results[i].latency < results[j].latency
	})
	return results
}

// ─── TCP forward ──────────────────────────────────────────────────────────────

func pipe(client net.Conn, backendAddr string) {
	srv, err := net.DialTimeout("tcp", backendAddr, 10*time.Second)
	if err != nil {
		client.Close()
		return
	}
	go func() {
		io.Copy(srv, client)
		srv.Close()
	}()
	io.Copy(client, srv)
	client.Close()
	srv.Close()
}

// proxy holds a running listener that can be swapped atomically.
type proxy struct {
	mu          sync.Mutex
	ln          net.Listener
	backendAddr atomic.Value // stores string
	listenAddr  string
}

func newProxy(listenAddr string) *proxy {
	return &proxy{listenAddr: listenAddr}
}

func (p *proxy) start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln != nil {
		return nil
	}
	ln, err := net.Listen("tcp", p.listenAddr)
	if err != nil {
		return err
	}
	p.ln = ln
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
				default:
				}
				return
			}
			backend, _ := p.backendAddr.Load().(string)
			if backend == "" {
				conn.Close()
				continue
			}
			go pipe(conn, backend)
		}
	}()
	return nil
}

func (p *proxy) setBackend(addr string) {
	p.backendAddr.Store(addr)
}

// ─── UI State ─────────────────────────────────────────────────────────────────

type logEntry struct {
	Msg string `json:"msg"`
}

type statusResp struct {
	Status        string     `json:"status"`
	NodeName      string     `json:"node_name"`
	NodeLoginAddr string     `json:"node_login_addr"`
	NodeGameAddr  string     `json:"node_game_addr"`
	LatencyMs     int64      `json:"latency_ms"`
	UptimeS       int64      `json:"uptime_s"`
	LoginLocal    string     `json:"login_local"`
	GameLocal     string     `json:"game_local"`
	Logs          []logEntry `json:"logs"`
}

type relayState struct {
	mu            sync.Mutex
	status        string
	nodeName      string
	nodeLoginAddr string
	nodeGameAddr  string
	latencyMs     int64
	connectedAt   time.Time
	loginLocal    string
	gameLocal     string
	logs          []logEntry
	cancelFn      context.CancelFunc
}

func (s *relayState) setConnected(n NodeConfig, latMs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nodeName != n.Name || s.status != "connected" {
		s.connectedAt = time.Now()
	}
	s.status = "connected"
	s.nodeName = n.Name
	s.nodeLoginAddr = n.LoginAddr
	s.nodeGameAddr = n.GameAddr
	s.latencyMs = latMs
}

func (s *relayState) setStatus(st string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = st
}

func (s *relayState) addLog(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, logEntry{Msg: msg})
	if len(s.logs) > 200 {
		s.logs = s.logs[len(s.logs)-200:]
	}
}

func (s *relayState) toJSON() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var uptime int64
	if !s.connectedAt.IsZero() {
		uptime = int64(time.Since(s.connectedAt).Seconds())
	}
	logs := make([]logEntry, len(s.logs))
	copy(logs, s.logs)
	resp := statusResp{
		Status:        s.status,
		NodeName:      s.nodeName,
		NodeLoginAddr: s.nodeLoginAddr,
		NodeGameAddr:  s.nodeGameAddr,
		LatencyMs:     s.latencyMs,
		UptimeS:       uptime,
		LoginLocal:    s.loginLocal,
		GameLocal:     s.gameLocal,
		Logs:          logs,
	}
	b, _ := json.Marshal(resp)
	return b
}

// ─── State writer (io.Writer → UI log) ───────────────────────────────────────

type stateWriter struct {
	state *relayState
}

func (sw *stateWriter) Write(p []byte) (n int, err error) {
	msg := strings.TrimRight(string(p), "\n\r")
	if msg != "" {
		sw.state.addLog(msg)
	}
	return len(p), nil
}

// ─── Local UI server ──────────────────────────────────────────────────────────

func startLocalUI(state *relayState) error {
	ln, err := net.Listen("tcp", "127.0.0.1:17770")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(relayHTML)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(state.toJSON())
	})
	mux.HandleFunc("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
		go func() {
			time.Sleep(300 * time.Millisecond)
			if state.cancelFn != nil {
				state.cancelFn()
			}
		}()
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return nil
}

func openBrowser(url string) {
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func fatalUI(msg string) {
	title, err1 := syscall.UTF16PtrFromString("GUARD RELAY")
	text, err2 := syscall.UTF16PtrFromString(msg)
	if err1 == nil && err2 == nil {
		user32 := syscall.NewLazyDLL("user32.dll")
		msgBox := user32.NewProc("MessageBoxW")
		msgBox.Call(0,
			uintptr(unsafe.Pointer(text)),
			uintptr(unsafe.Pointer(title)),
			0x10) // MB_ICONERROR
	}
	os.Exit(1)
}

// ─── Relay state ──────────────────────────────────────────────────────────────

type relay struct {
	cfg         RelayConfig
	relayID     string
	loginProxy  *proxy
	gameProxy   *proxy
	currentNode atomic.Value // stores NodeConfig
	latencyMs   int64        // atomic
	ui          *relayState
}

func newRelay(cfg RelayConfig, ui *relayState) *relay {
	return &relay{
		cfg:        cfg,
		relayID:    newUUID(),
		loginProxy: newProxy(cfg.LoginLocal),
		gameProxy:  newProxy(cfg.GameLocal),
		ui:         ui,
	}
}

func (r *relay) switchNode(n NodeConfig, latency time.Duration) {
	r.currentNode.Store(n)
	atomic.StoreInt64(&r.latencyMs, latency.Milliseconds())
	r.loginProxy.setBackend(n.LoginAddr)
	r.gameProxy.setBackend(n.GameAddr)
	r.ui.setConnected(n, latency.Milliseconds())
	log.Printf("[GUARD RELAY] Conectado a %s (%s) | latencia: %v", n.Name, n.LoginAddr, latency.Round(time.Millisecond))
}

func (r *relay) currentNodeCfg() NodeConfig {
	v, _ := r.currentNode.Load().(NodeConfig)
	return v
}

// ─── Heartbeat ────────────────────────────────────────────────────────────────

func sendHeartbeat(n NodeConfig, relayID string, latencyMs int64) {
	if n.AdminURL == "" {
		return
	}
	body, _ := json.Marshal(map[string]interface{}{
		"relay_id":   relayID,
		"node_id":    n.ID,
		"node_name":  n.Name,
		"latency_ms": latencyMs,
	})
	req, err := http.NewRequest("POST", n.AdminURL+"/api/relay/ping", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+n.Token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[GUARD RELAY] heartbeat error: %v", err)
		return
	}
	resp.Body.Close()
}

func (r *relay) heartbeatLoop(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	sendHeartbeat(r.currentNodeCfg(), r.relayID, atomic.LoadInt64(&r.latencyMs))
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			sendHeartbeat(r.currentNodeCfg(), r.relayID, atomic.LoadInt64(&r.latencyMs))
		}
	}
}

// ─── Health monitor ───────────────────────────────────────────────────────────

func (r *relay) healthLoop(ctx context.Context) {
	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			results := probeAll(r.cfg.Nodes, 2*time.Second)
			best := results[0]
			if !best.ok {
				log.Printf("[GUARD RELAY] ADVERTENCIA: ningún nodo responde")
				r.ui.setStatus("warning")
				continue
			}
			cur := r.currentNodeCfg()
			if best.ID != cur.ID || best.latency > 500*time.Millisecond {
				cur2 := probeNode(cur, 2*time.Second)
				if !cur2.ok || cur2.latency > 500*time.Millisecond {
					log.Printf("[GUARD RELAY] Cambiando de %s a %s (latencia: %v)", cur.Name, best.Name, best.latency.Round(time.Millisecond))
					r.switchNode(best.NodeConfig, best.latency)
				}
			}
		}
	}
}

// ─── Config loading ───────────────────────────────────────────────────────────

func loadConfig() (RelayConfig, error) {
	exePath, _ := os.Executable()
	candidates := []string{
		filepath.Join(filepath.Dir(exePath), "relay.json"),
		"relay.json",
	}
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var cfg RelayConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return RelayConfig{}, fmt.Errorf("relay.json inválido (%s): %w", p, err)
		}
		return cfg, nil
	}
	return RelayConfig{}, fmt.Errorf("no se encontró relay.json junto al ejecutable")
}

// ─── UUID ─────────────────────────────────────────────────────────────────────

func newUUID() string {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fatalUI("Error al cargar configuración:\n\n" + err.Error())
		return
	}
	if len(cfg.Nodes) == 0 {
		fatalUI("No hay nodos configurados en relay.json.")
		return
	}
	if cfg.LoginLocal == "" {
		cfg.LoginLocal = "127.0.0.1:17666"
	}
	if cfg.GameLocal == "" {
		cfg.GameLocal = "127.0.0.1:17667"
	}

	state := &relayState{
		status:     "connecting",
		loginLocal: cfg.LoginLocal,
		gameLocal:  cfg.GameLocal,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	state.cancelFn = cancel
	defer cancel()

	log.SetFlags(log.Ltime)
	log.SetOutput(&stateWriter{state: state})

	if err := startLocalUI(state); err != nil {
		// Port already in use — likely another relay instance is running.
		// Re-open its UI in the browser instead of showing an error.
		openBrowser("http://127.0.0.1:17770")
		return
	}

	// Give the HTTP server a moment to start, then open the browser.
	time.Sleep(80 * time.Millisecond)
	openBrowser("http://127.0.0.1:17770")

	r := newRelay(cfg, state)

	if err := r.loginProxy.start(ctx); err != nil {
		log.Printf("[GUARD RELAY] Error proxy login (%s): %v", cfg.LoginLocal, err)
	}
	if err := r.gameProxy.start(ctx); err != nil {
		log.Printf("[GUARD RELAY] Error proxy juego (%s): %v", cfg.GameLocal, err)
	}

	log.Printf("[GUARD RELAY] Probando %d nodo(s)...", len(cfg.Nodes))
	results := probeAll(cfg.Nodes, 2*time.Second)
	best := results[0]
	if !best.ok {
		log.Printf("[GUARD RELAY] ADVERTENCIA: ningún nodo disponible, reintentando en 60s...")
		state.setStatus("warning")
	} else {
		r.switchNode(best.NodeConfig, best.latency)
	}

	go r.heartbeatLoop(ctx)
	go r.healthLoop(ctx)

	<-ctx.Done()
	log.Printf("[GUARD RELAY] Relay detenido.")
}
