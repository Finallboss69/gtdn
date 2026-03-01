package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"

	"guard/internal/firewall"
	"guard/internal/limiter"
)

// ─── Historial de métricas ────────────────────────────────────────────────────

const maxSamples = 36 // 6 minutos a 10s por muestra

// MetricSample es un punto de datos en el tiempo.
type MetricSample struct {
	T           int64   `json:"t"`            // Unix timestamp
	ActiveConns int     `json:"active_conns"` // Conexiones activas
	RejectRate  float64 `json:"reject_rate"`  // Rechazos por segundo
}

type metricsHistory struct {
	mu      sync.Mutex
	samples []MetricSample
	lastRej uint64
	lastT   time.Time
}

func (h *metricsHistory) record(active int, totalRej uint64) {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()

	rate := 0.0
	if !h.lastT.IsZero() {
		elapsed := now.Sub(h.lastT).Seconds()
		if elapsed > 0 {
			diff := totalRej - h.lastRej
			rate = float64(diff) / elapsed
		}
	}
	h.lastRej = totalRej
	h.lastT = now

	h.samples = append(h.samples, MetricSample{
		T:           now.Unix(),
		ActiveConns: active,
		RejectRate:  rate,
	})
	if len(h.samples) > maxSamples {
		h.samples = h.samples[len(h.samples)-maxSamples:]
	}
}

func (h *metricsHistory) get() []MetricSample {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]MetricSample, len(h.samples))
	copy(out, h.samples)
	return out
}

// ─── EventLog — ring buffer de eventos ────────────────────────────────────────

const maxEvents = 200

// Event representa un evento del sistema.
type Event struct {
	T      int64  `json:"t"`
	Type   string `json:"type"`             // "ban","unblock","unblock_all","drain_on","drain_off","overload_start","overload_end"
	IP     string `json:"ip,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func (e *eventLog) add(typ, ip, detail string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, Event{
		T:      time.Now().Unix(),
		Type:   typ,
		IP:     ip,
		Detail: detail,
	})
	if len(e.events) > maxEvents {
		e.events = e.events[len(e.events)-maxEvents:]
	}
}

func (e *eventLog) get() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Event, len(e.events))
	copy(out, e.events)
	return out
}

// ─── Server ───────────────────────────────────────────────────────────────────

// Server expone una API HTTP de administración para un proceso guard.
type Server struct {
	lim          *limiter.Limiter
	fw           *firewall.Manager
	profile      string
	drainFn      func() bool   // nil si no aplica
	rejectFn     func() uint64 // retorna total de rechazos acumulados
	maxConns     int
	startTime    time.Time
	history      *metricsHistory
	evLog        *eventLog
	drainSince   time.Time
	drainSinceMu sync.Mutex
	loadPctFn    func() float64 // opcional: retorna % de carga actual
	allowedIPs   []string       // IPs adicionales permitidas (además de loopback)
	authToken    string         // si no vacío, requiere Authorization: Bearer <token> para IPs no-loopback
	relayMu      sync.Mutex
	relayRegistry map[string]*relayInfo // relay_id → info
}

// relayInfo almacena el estado completo de un relay activo.
type relayInfo struct {
	RelayID   string
	IP        string
	NodeID    string
	NodeName  string
	LatencyMs int64
	FirstSeen time.Time
	LastSeen  time.Time
}

// New crea un Server de administración.
func New(lim *limiter.Limiter, fw *firewall.Manager, profile string,
	drainFn func() bool, rejectFn func() uint64, maxConns int) *Server {
	return &Server{
		lim:           lim,
		fw:            fw,
		profile:       profile,
		drainFn:       drainFn,
		rejectFn:      rejectFn,
		maxConns:      maxConns,
		startTime:     time.Now(),
		history:       &metricsHistory{},
		evLog:         &eventLog{},
		relayRegistry: make(map[string]*relayInfo),
	}
}

// SetAccessControl configura IPs adicionales y token de autorización para la API admin.
// Si allowedIPs está vacío, solo se permite loopback.
// Si token está vacío, no se requiere autenticación para las IPs adicionales.
func (s *Server) SetAccessControl(allowedIPs []string, token string) {
	s.allowedIPs = allowedIPs
	s.authToken = token
}

// SetLoadPctFn establece una función que retorna el porcentaje de carga actual.
func (s *Server) SetLoadPctFn(fn func() float64) {
	s.loadPctFn = fn
}

// SetDrainSince guarda cuándo entró en modo drain (zero si no está en drain).
func (s *Server) SetDrainSince(t time.Time) {
	s.drainSinceMu.Lock()
	s.drainSince = t
	s.drainSinceMu.Unlock()
}

// AddEvent registra un evento en el log de eventos.
func (s *Server) AddEvent(typ, ip, detail string) {
	s.evLog.add(typ, ip, detail)
}

// Start arranca el servidor HTTP y bloquea hasta que ctx se cancele.
func (s *Server) Start(ctx context.Context, listenAddr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status",      s.handleStatus)
	mux.HandleFunc("/api/ips",         s.handleIPs)
	mux.HandleFunc("/api/blocked",     s.handleBlocked)
	mux.HandleFunc("/api/unblock",     s.handleUnblock)
	mux.HandleFunc("/api/block",       s.handleBlock)
	mux.HandleFunc("/api/sysinfo",     s.handleSysinfo)
	mux.HandleFunc("/api/metrics",     s.handleMetrics)
	mux.HandleFunc("/api/health",      s.handleHealth)
	mux.HandleFunc("/api/unblock-all", s.handleUnblockAll)
	mux.HandleFunc("/api/events",      s.handleEvents)
	mux.HandleFunc("/api/relay/ping",  s.handleRelayPing)
	mux.HandleFunc("/api/relay/list",  s.handleRelayList)

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      s.accessControl(mux),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	// Goroutine que registra métricas cada 10 segundos
	go func() {
		// Primera muestra inmediata
		active, _ := s.lim.Stats()
		s.history.record(active, s.rejectFn())

		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				active, _ := s.lim.Stats()
				s.history.record(active, s.rejectFn())
			}
		}
	}()

	// Cleanup relay registry: remove entries older than 90s
	go func() {
		tick := time.NewTicker(60 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				s.cleanRelayRegistry()
			}
		}
	}()

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("[INFO] admin API [%s] escuchando en %s", s.profile, listenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("admin server [%s]: %w", s.profile, err)
	}
	return nil
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	active, ipCount := s.lim.Stats()
	drain := false
	if s.drainFn != nil {
		drain = s.drainFn()
	}

	s.drainSinceMu.Lock()
	drainSince := s.drainSince
	s.drainSinceMu.Unlock()

	drainSinceUnix := int64(0)
	if !drainSince.IsZero() {
		drainSinceUnix = drainSince.Unix()
	}

	loadPct := 0.0
	if s.loadPctFn != nil {
		loadPct = s.loadPctFn()
	} else if s.maxConns > 0 {
		loadPct = float64(active) * 100.0 / float64(s.maxConns)
	}

	s.relayMu.Lock()
	relayCount := len(s.relayRegistry)
	s.relayMu.Unlock()

	type Resp struct {
		Profile      string  `json:"profile"`
		ActiveConns  int     `json:"active_conns"`
		IPCount      int     `json:"ip_count"`
		TotalRejects uint64  `json:"total_rejects"`
		DrainMode    bool    `json:"drain_mode"`
		DrainSince   int64   `json:"drain_since"`
		MaxConns     int     `json:"max_conns"`
		LoadPct      float64 `json:"load_pct"`
		RelayCount   int     `json:"relay_count"`
	}
	writeJSON(w, Resp{
		Profile:      s.profile,
		ActiveConns:  active,
		IPCount:      ipCount,
		TotalRejects: s.rejectFn(),
		DrainMode:    drain,
		DrainSince:   drainSinceUnix,
		MaxConns:     s.maxConns,
		LoadPct:      loadPct,
		RelayCount:   relayCount,
	})
}

func (s *Server) handleIPs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stats := s.lim.GetAllStats()
	now := time.Now()
	type IPResp struct {
		IP          string `json:"ip"`
		LiveCount   int    `json:"live_count"`
		DenyCount   int    `json:"deny_count"`
		BlockCount  int    `json:"block_count"`
		TempBlocked bool   `json:"temp_blocked"`
		BlockUntil  string `json:"block_until,omitempty"`
		LastSeen    string `json:"last_seen"`
	}
	result := make([]IPResp, 0, len(stats))
	for _, st := range stats {
		blocked := !st.BlockUntil.IsZero() && now.Before(st.BlockUntil)
		blockUntil := ""
		if blocked {
			blockUntil = st.BlockUntil.Format(time.RFC3339)
		}
		result = append(result, IPResp{
			IP:          st.IP,
			LiveCount:   st.LiveCount,
			DenyCount:   st.DenyCount,
			BlockCount:  st.BlockCount,
			TempBlocked: blocked,
			BlockUntil:  blockUntil,
			LastSeen:    st.LastSeen.Format(time.RFC3339),
		})
	}
	writeJSON(w, result)
}

func (s *Server) handleBlocked(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.fw == nil {
		writeJSON(w, []struct{}{})
		return
	}
	scheduled := s.fw.GetScheduledUnblocks()
	type FWResp struct {
		IP               string `json:"ip"`
		UnblockAt        string `json:"unblock_at"`
		RemainingSeconds int    `json:"remaining_seconds"`
	}
	result := make([]FWResp, 0, len(scheduled))
	for ip, unblockAt := range scheduled {
		remaining := int(time.Until(unblockAt).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		result = append(result, FWResp{
			IP:               ip,
			UnblockAt:        unblockAt.Format(time.RFC3339),
			RemainingSeconds: remaining,
		})
	}
	writeJSON(w, result)
}

func (s *Server) handleUnblock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IP == "" {
		http.Error(w, "bad request: se requiere campo ip", http.StatusBadRequest)
		return
	}
	s.lim.UnblockTempIP(req.IP)
	if s.fw != nil {
		_ = s.fw.UnblockIP(req.IP)
	}
	log.Printf("[INFO] admin: unblock IP=%s profile=%s", req.IP, s.profile)
	s.evLog.add("unblock", req.IP, "")
	writeJSON(w, map[string]string{"status": "ok", "ip": req.IP})
}

func (s *Server) handleBlock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IP == "" {
		http.Error(w, "bad request: se requiere campo ip", http.StatusBadRequest)
		return
	}
	if s.fw == nil {
		http.Error(w, "firewall no habilitado", http.StatusServiceUnavailable)
		return
	}
	if err := s.fw.BlockIP(req.IP); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("[INFO] admin: block IP=%s profile=%s", req.IP, s.profile)
	s.evLog.add("ban", req.IP, "manual")
	writeJSON(w, map[string]string{"status": "ok", "ip": req.IP})
}

// handleSysinfo devuelve información del proceso (goroutines, memoria, uptime).
func (s *Server) handleSysinfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	type Resp struct {
		Goroutines    int     `json:"goroutines"`
		HeapMB        float64 `json:"heap_mb"`
		SysMB         float64 `json:"sys_mb"`
		GCCount       uint32  `json:"gc_count"`
		UptimeSeconds int64   `json:"uptime_seconds"`
	}
	writeJSON(w, Resp{
		Goroutines:    runtime.NumGoroutine(),
		HeapMB:        float64(ms.HeapInuse) / 1024 / 1024,
		SysMB:         float64(ms.Sys) / 1024 / 1024,
		GCCount:       ms.NumGC,
		UptimeSeconds: int64(time.Since(s.startTime).Seconds()),
	})
}

// handleMetrics devuelve el historial de muestras de métricas.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.history.get())
}

// handleHealth retorna estado de salud básico del servidor.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]interface{}{
		"status":         "ok",
		"uptime_seconds": int64(time.Since(s.startTime).Seconds()),
	})
}

// handleUnblockAll libera todos los bloqueos temporales del limiter.
func (s *Server) handleUnblockAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	count := s.lim.UnblockAll()
	log.Printf("[INFO] admin: unblock-all liberados=%d profile=%s", count, s.profile)
	s.evLog.add("unblock_all", "", fmt.Sprintf("cleared=%d", count))
	writeJSON(w, map[string]interface{}{"cleared": count})
}

// handleEvents retorna el log de eventos recientes.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.evLog.get())
}

// handleRelayPing registra un heartbeat de un cliente relay.
// Cualquier IP puede llamarlo, pero el token siempre es requerido.
func (s *Server) handleRelayPing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.authToken == "" || r.Header.Get("Authorization") != "Bearer "+s.authToken {
		w.Header().Set("WWW-Authenticate", `Bearer realm="guard-admin"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		RelayID   string `json:"relay_id"`
		NodeID    string `json:"node_id"`
		NodeName  string `json:"node_name"`
		LatencyMs int64  `json:"latency_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RelayID == "" {
		http.Error(w, "bad request: se requiere relay_id", http.StatusBadRequest)
		return
	}
	// Extraer IP del cliente (puede ser la IP pública del relay)
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	now := time.Now()
	s.relayMu.Lock()
	if existing, ok := s.relayRegistry[req.RelayID]; ok {
		existing.LastSeen  = now
		existing.NodeID    = req.NodeID
		existing.NodeName  = req.NodeName
		existing.LatencyMs = req.LatencyMs
		if clientIP != "" {
			existing.IP = clientIP
		}
	} else {
		s.relayRegistry[req.RelayID] = &relayInfo{
			RelayID:   req.RelayID,
			IP:        clientIP,
			NodeID:    req.NodeID,
			NodeName:  req.NodeName,
			LatencyMs: req.LatencyMs,
			FirstSeen: now,
			LastSeen:  now,
		}
	}
	s.relayMu.Unlock()
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleRelayList devuelve la lista de relays activos con toda su información.
func (s *Server) handleRelayList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	now := time.Now()
	type RelayResp struct {
		RelayID       string `json:"relay_id"`
		IP            string `json:"ip"`
		NodeID        string `json:"node_id"`
		NodeName      string `json:"node_name"`
		LatencyMs     int64  `json:"latency_ms"`
		LastSeen      int64  `json:"last_seen"`
		AgeSeconds    int64  `json:"age_seconds"`
		FirstSeen     int64  `json:"first_seen"`
		UptimeSeconds int64  `json:"uptime_seconds"`
	}
	s.relayMu.Lock()
	result := make([]RelayResp, 0, len(s.relayRegistry))
	for _, info := range s.relayRegistry {
		result = append(result, RelayResp{
			RelayID:       info.RelayID,
			IP:            info.IP,
			NodeID:        info.NodeID,
			NodeName:      info.NodeName,
			LatencyMs:     info.LatencyMs,
			LastSeen:      info.LastSeen.Unix(),
			AgeSeconds:    int64(now.Sub(info.LastSeen).Seconds()),
			FirstSeen:     info.FirstSeen.Unix(),
			UptimeSeconds: int64(now.Sub(info.FirstSeen).Seconds()),
		})
	}
	s.relayMu.Unlock()
	writeJSON(w, result)
}

// cleanRelayRegistry elimina entradas con last_seen mayor a 90s.
func (s *Server) cleanRelayRegistry() {
	cutoff := time.Now().Add(-90 * time.Second)
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	for id, info := range s.relayRegistry {
		if info.LastSeen.Before(cutoff) {
			delete(s.relayRegistry, id)
		}
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// accessControl verifica que la conexión sea legítima antes de pasar al handler.
//
// Reglas (en orden):
//  1. /api/relay/ping → siempre pasa (handler hace su propio check de token)
//  2. Loopback         → siempre permitido, sin token
//  3. Token válido     → permitido desde cualquier IP (token ES la seguridad)
//  4. Sin token config → solo permitido si la IP está en allowedIPs
//  5. Todo lo demás    → 403 / 401
func (s *Server) accessControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// relay/ping tiene su propio auth; lo pasamos sin más checks de IP.
		if r.URL.Path == "/api/relay/ping" {
			next.ServeHTTP(w, r)
			return
		}

		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ip := net.ParseIP(host)
		if ip == nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		// Loopback siempre permitido, sin token.
		if ip.IsLoopback() {
			next.ServeHTTP(w, r)
			return
		}

		// Si hay token configurado, token correcto = acceso desde cualquier IP.
		if s.authToken != "" {
			if r.Header.Get("Authorization") == "Bearer "+s.authToken {
				next.ServeHTTP(w, r)
				return
			}
			// Token incorrecto o ausente.
			w.Header().Set("WWW-Authenticate", `Bearer realm="guard-admin"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Sin token configurado: caer a IP allowlist.
		// Usamos ip.Equal(net.ParseIP(aip)) para manejar IPv4-mapped IPv6.
		for _, aip := range s.allowedIPs {
			if ip.Equal(net.ParseIP(aip)) {
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
