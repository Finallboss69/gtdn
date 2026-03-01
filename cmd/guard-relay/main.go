package main

import (
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
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

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

// start begins accepting connections. If already listening, no-op.
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
					// listener closed during swap
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

// setBackend updates the backend address atomically; existing connections
// continue to their old backend until they close naturally.
func (p *proxy) setBackend(addr string) {
	p.backendAddr.Store(addr)
}

// ─── Relay state ──────────────────────────────────────────────────────────────

type relay struct {
	cfg         RelayConfig
	relayID     string
	loginProxy  *proxy
	gameProxy   *proxy
	currentNode atomic.Value // stores NodeConfig
}

func newRelay(cfg RelayConfig) *relay {
	return &relay{
		cfg:        cfg,
		relayID:    newUUID(),
		loginProxy: newProxy(cfg.LoginLocal),
		gameProxy:  newProxy(cfg.GameLocal),
	}
}

func (r *relay) switchNode(n NodeConfig, latency time.Duration) {
	r.currentNode.Store(n)
	r.loginProxy.setBackend(n.LoginAddr)
	r.gameProxy.setBackend(n.GameAddr)
	log.Printf("[GUARD RELAY] Conectado a %s (%s) | latencia: %v", n.Name, n.LoginAddr, latency.Round(time.Millisecond))
	log.Printf("[GUARD RELAY] Login local:  %s", r.cfg.LoginLocal)
	log.Printf("[GUARD RELAY] Juego  local: %s", r.cfg.GameLocal)
	log.Printf("[GUARD RELAY] Presiona Ctrl+C para salir")
}

func (r *relay) currentNodeCfg() NodeConfig {
	v, _ := r.currentNode.Load().(NodeConfig)
	return v
}

// ─── Heartbeat ────────────────────────────────────────────────────────────────

func sendHeartbeat(n NodeConfig, relayID string) {
	body, _ := json.Marshal(map[string]string{"relay_id": relayID})
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
	// send first heartbeat immediately
	sendHeartbeat(r.currentNodeCfg(), r.relayID)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			sendHeartbeat(r.currentNodeCfg(), r.relayID)
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
				continue
			}
			cur := r.currentNodeCfg()
			if best.ID != cur.ID || best.latency > 500*time.Millisecond {
				// re-probe current node
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
	// Search order: directory of exe, then current dir
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
	return RelayConfig{}, fmt.Errorf("no se encontró relay.json (buscado en directorio del exe y directorio actual)")
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
	log.SetFlags(log.Ltime)

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("[GUARD RELAY] Error: %v", err)
	}
	if len(cfg.Nodes) == 0 {
		log.Fatalf("[GUARD RELAY] Error: no hay nodos configurados en relay.json")
	}
	if cfg.LoginLocal == "" {
		cfg.LoginLocal = "127.0.0.1:17666"
	}
	if cfg.GameLocal == "" {
		cfg.GameLocal = "127.0.0.1:17667"
	}

	log.Printf("[GUARD RELAY] Probando %d nodo(s)...", len(cfg.Nodes))
	results := probeAll(cfg.Nodes, 2*time.Second)

	best := results[0]
	if !best.ok {
		log.Fatalf("[GUARD RELAY] Error: ningún nodo está disponible")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	r := newRelay(cfg)

	// Start proxies
	if err := r.loginProxy.start(ctx); err != nil {
		log.Fatalf("[GUARD RELAY] Error iniciando proxy login (%s): %v", cfg.LoginLocal, err)
	}
	if err := r.gameProxy.start(ctx); err != nil {
		log.Fatalf("[GUARD RELAY] Error iniciando proxy juego (%s): %v", cfg.GameLocal, err)
	}

	r.switchNode(best.NodeConfig, best.latency)

	go r.heartbeatLoop(ctx)
	go r.healthLoop(ctx)

	<-ctx.Done()
	log.Printf("[GUARD RELAY] Cerrando...")
}
