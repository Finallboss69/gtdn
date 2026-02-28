package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed panel.html
var panelHTML []byte

// NodeCfg describe un nodo guard (VPS con guard-login y guard-game).
type NodeCfg struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	LoginURL string `json:"login_url"`
	GameURL  string `json:"game_url"`
	Token    string `json:"token"` // Bearer token si el guard tiene admin_token configurado
}

// PanelCfg es la configuración del panel con todos los nodos.
type PanelCfg struct {
	Nodes []NodeCfg `json:"nodes"`
}

func loadPanelCfg(path string) PanelCfg {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[INFO] %s no encontrado — usando nodo local por defecto (127.0.0.1:7771/7772)", path)
		return PanelCfg{Nodes: []NodeCfg{{
			ID:       "local",
			Name:     "Local",
			LoginURL: "http://127.0.0.1:7771",
			GameURL:  "http://127.0.0.1:7772",
		}}}
	}
	var cfg PanelCfg
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("[ERROR] nodes.json inválido: %v", err)
	}
	for i := range cfg.Nodes {
		if cfg.Nodes[i].ID == "" {
			cfg.Nodes[i].ID = fmt.Sprintf("node%d", i+1)
		}
		if cfg.Nodes[i].Name == "" {
			cfg.Nodes[i].Name = fmt.Sprintf("Nodo %d", i+1)
		}
	}
	log.Printf("[INFO] %d nodo(s) cargados desde %s", len(cfg.Nodes), path)
	return cfg
}

func main() {
	listenFlag := flag.String("listen", "127.0.0.1:7700", "Dirección del panel web")
	nodesFlag  := flag.String("nodes",  "nodes.json",     "Ruta al archivo de configuración de nodos")
	flag.Parse()

	cfg := loadPanelCfg(*nodesFlag)

	// Mapa de nodos para lookup rápido
	nodeMap := make(map[string]*NodeCfg, len(cfg.Nodes))
	for i := range cfg.Nodes {
		nodeMap[cfg.Nodes[i].ID] = &cfg.Nodes[i]
	}

	client := &http.Client{Timeout: 5 * time.Second}
	mux := http.NewServeMux()

	// ─── Panel HTML ───────────────────────────────────────────────────────────
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(panelHTML)
	})

	// ─── Lista de nodos (id + name, sin tokens ni URLs internas) ─────────────
	type NodeInfo struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	mux.HandleFunc("/api/nodes", func(w http.ResponseWriter, r *http.Request) {
		out := make([]NodeInfo, len(cfg.Nodes))
		for i, n := range cfg.Nodes {
			out[i] = NodeInfo{ID: n.ID, Name: n.Name}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	// ─── Proxy por nodo: /api/node/{id}/{svc}/{endpoint...} ──────────────────
	// svc = "login" | "game"
	// Ejemplo: /api/node/vps1/login/status → http://185.x.x.x:7771/api/status
	mux.HandleFunc("/api/node/", func(w http.ResponseWriter, r *http.Request) {
		// Parsear: /api/node/{id}/{svc}/{rest}
		path := strings.TrimPrefix(r.URL.Path, "/api/node/")
		parts := strings.SplitN(path, "/", 3)
		if len(parts) < 3 {
			http.Error(w, `{"error":"path inválido"}`, http.StatusBadRequest)
			return
		}
		nodeID, svc, rest := parts[0], parts[1], parts[2]

		node, ok := nodeMap[nodeID]
		if !ok {
			http.Error(w, `{"error":"nodo no encontrado"}`, http.StatusNotFound)
			return
		}

		var baseURL string
		switch svc {
		case "login":
			baseURL = node.LoginURL
		case "game":
			baseURL = node.GameURL
		default:
			http.Error(w, `{"error":"servicio inválido (login|game)"}`, http.StatusBadRequest)
			return
		}

		targetURL := baseURL + "/api/" + rest
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}

		req, err := http.NewRequest(r.Method, targetURL, r.Body)
		if err != nil {
			http.Error(w, `{"error":"proxy error"}`, http.StatusBadGateway)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		if node.Token != "" {
			req.Header.Set("Authorization", "Bearer "+node.Token)
		}

		resp, err := client.Do(req)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":"service_offline"}`))
			return
		}
		defer resp.Body.Close()

		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})

	srv := &http.Server{
		Addr:         *listenFlag,
		Handler:      localhostOnly(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	log.Printf("[INFO] GUARD_GO Panel disponible en http://%s  (%d nodo(s))", *listenFlag, len(cfg.Nodes))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("panel: %v", err)
	}
}

// localhostOnly rechaza conexiones que no sean desde 127.0.0.1 / ::1.
func localhostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
