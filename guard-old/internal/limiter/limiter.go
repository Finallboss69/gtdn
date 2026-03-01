package limiter

import (
	"sync"
	"time"
)

// IpState mantiene el estado por IP para rate limit y bloqueos.
type IpState struct {
	mu          sync.Mutex
	LiveCount   int       // conexiones activas
	Tokens      float64   // token bucket
	LastTokenTs time.Time // última actualización de tokens
	DenyCount   int       // rechazos consecutivos/contados
	BlockUntil  time.Time // bloqueo temporal hasta
	LastSeen    time.Time // última actividad
}

// Limiter implementa límites por IP y global.
type Limiter struct {
	mu sync.RWMutex
	// por IP
	byIP map[string]*IpState
	// parámetros
	maxLivePerIP  int
	refillPerSec  float64
	burst         float64
	deniesToBlock int
	tempBlockSec  int
	// global
	maxTotalConns int
	sem           chan struct{} // semáforo global (send=acquire, recv=release)
	// cleanup
	staleAfterSec   int
	cleanupEverySec int
	stopCleanup     chan struct{}
}

// New crea un Limiter con la configuración dada.
func New(maxLivePerIP int, refillPerSec, burst float64, deniesToBlock, tempBlockSec int,
	maxTotalConns int, staleAfterSec, cleanupEverySec int) *Limiter {
	l := &Limiter{
		byIP:            make(map[string]*IpState),
		maxLivePerIP:    maxLivePerIP,
		refillPerSec:    refillPerSec,
		burst:           burst,
		deniesToBlock:   deniesToBlock,
		tempBlockSec:    tempBlockSec,
		maxTotalConns:   maxTotalConns,
		sem:             make(chan struct{}, maxTotalConns),
		staleAfterSec:   staleAfterSec,
		cleanupEverySec: cleanupEverySec,
		stopCleanup:     make(chan struct{}),
	}
	go l.cleanupLoop()
	return l
}

// TryAccept devuelve (allowed bool, reason string).
// Si allowed es true, el llamador debe llamar Release() cuando cierre la conexión.
func (l *Limiter) TryAccept(ip string, now time.Time) (allowed bool, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Semáforo global: intentar adquirir
	select {
	case l.sem <- struct{}{}:
		// ok, tenemos slot global
	default:
		return false, "global_limit"
	}

	state := l.getOrCreate(ip, now)
	state.mu.Lock()

	// Bloqueo temporal
	if now.Before(state.BlockUntil) {
		state.mu.Unlock()
		<-l.sem
		return false, "tempblock"
	}

	// Límite de conexiones vivas por IP
	if state.LiveCount >= l.maxLivePerIP {
		state.mu.Unlock()
		<-l.sem
		return false, "live_limit"
	}

	// Token bucket
	state.refill(l.refillPerSec, l.burst, now)
	if state.Tokens < 1 {
		state.DenyCount++
		if state.DenyCount >= l.deniesToBlock {
			state.BlockUntil = now.Add(time.Duration(l.tempBlockSec) * time.Second)
		}
		state.mu.Unlock()
		<-l.sem
		return false, "rate"
	}
	state.Tokens--
	state.DenyCount = 0
	state.LiveCount++
	state.LastSeen = now
	state.mu.Unlock()
	return true, ""
}

// getOrCreate devuelve el IpState para ip; debe llamarse con l.mu mantenido.
func (l *Limiter) getOrCreate(ip string, now time.Time) *IpState {
	s, ok := l.byIP[ip]
	if !ok {
		s = &IpState{
			Tokens:      l.burst,
			LastTokenTs: now,
			LastSeen:    now,
		}
		l.byIP[ip] = s
	}
	return s
}

// refill actualiza el token bucket. Debe llamarse con state.mu.
func (s *IpState) refill(refillPerSec, burst float64, now time.Time) {
	elapsed := now.Sub(s.LastTokenTs).Seconds()
	s.Tokens += elapsed * refillPerSec
	if s.Tokens > burst {
		s.Tokens = burst
	}
	s.LastTokenTs = now
}

// Release libera una conexión (decrementa LiveCount y devuelve slot global).
func (l *Limiter) Release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok := l.byIP[ip]; ok {
		s.mu.Lock()
		if s.LiveCount > 0 {
			s.LiveCount--
		}
		s.LastSeen = time.Now()
		s.mu.Unlock()
	}
	// Devolver slot global
	select {
	case <-l.sem:
	default:
	}
}

// RecordDeny incrementa DenyCount para la IP (p.ej. cuando rechazamos por rate/live_limit).
// No se llama cuando rechazamos por tempblock (no gastar lógica extra).
func (l *Limiter) RecordDeny(ip string) {
	l.mu.RLock()
	s, ok := l.byIP[ip]
	l.mu.RUnlock()
	if !ok {
		return
	}
	s.mu.Lock()
	s.DenyCount++
	if s.DenyCount >= l.deniesToBlock {
		s.BlockUntil = time.Now().Add(time.Duration(l.tempBlockSec) * time.Second)
	}
	s.LastSeen = time.Now()
	s.mu.Unlock()
}

// ShouldFirewallBlock indica si la IP está en tempblock (para decidir firewall ban).
func (l *Limiter) IsTempBlocked(ip string) bool {
	l.mu.RLock()
	s, ok := l.byIP[ip]
	l.mu.RUnlock()
	if !ok {
		return false
	}
	s.mu.Lock()
	blocked := time.Now().Before(s.BlockUntil)
	s.mu.Unlock()
	return blocked
}

// cleanupLoop elimina IPs sin conexiones y sin actividad reciente.
func (l *Limiter) cleanupLoop() {
	tick := time.NewTicker(time.Duration(l.cleanupEverySec) * time.Second)
	defer tick.Stop()
	stale := time.Duration(l.staleAfterSec) * time.Second
	for {
		select {
		case <-l.stopCleanup:
			return
		case <-tick.C:
			l.cleanup(stale)
		}
	}
}

func (l *Limiter) cleanup(stale time.Duration) {
	now := time.Now()
	cutoff := now.Add(-stale)
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, s := range l.byIP {
		s.mu.Lock()
		live := s.LiveCount
		last := s.LastSeen
		s.mu.Unlock()
		if live == 0 && last.Before(cutoff) {
			delete(l.byIP, ip)
		}
	}
}

// Stop detiene el cleanup loop.
func (l *Limiter) Stop() {
	close(l.stopCleanup)
}

// Stats devuelve conexiones activas (slots en uso del semáforo) e IPs en memoria.
func (l *Limiter) Stats() (activeConns int, ipCount int) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	activeConns = len(l.sem)
	ipCount = len(l.byIP)
	return activeConns, ipCount
}
