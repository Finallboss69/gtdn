package firewall

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	rulePrefix          = "TDN-AUTOBLOCK-"
	maxConcurrentBlocks = 3                // Reducido para ser más conservador
	netshTimeout        = 3 * time.Second  // netsh es nativo, mucho más rápido que PowerShell
	maxBlockedIPs       = 1000             // Límite máximo de IPs bloqueadas simultáneamente
	batchInterval       = 5 * time.Second // Procesar bloqueos cada 5 segundos
	maxBatchSize        = 50              // Máximo de IPs por batch
)

// Manager gestiona reglas de firewall Windows por IP.
type Manager struct {
	mu           sync.Mutex
	scheduled    map[string]time.Time // IP -> cuándo eliminar la regla
	blockSec     int
	workerSem    chan struct{} // Semáforo para limitar concurrencia de netsh
	unblockQueue chan string
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	// Batching de bloqueos
	pendingBlocksMu sync.Mutex
	pendingBlocks   map[string]bool // IPs pendientes de bloquear
}

// New crea un Manager. blockSeconds es el tiempo que la regla permanece antes de eliminarse.
func New(blockSeconds int) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		scheduled:      make(map[string]time.Time),
		blockSec:       blockSeconds,
		workerSem:      make(chan struct{}, maxConcurrentBlocks),
		unblockQueue:   make(chan string, 200),
		ctx:            ctx,
		cancel:         cancel,
		pendingBlocks: make(map[string]bool),
	}

	// Iniciar worker de batching que procesa bloqueos cada 5 segundos
	m.wg.Add(1)
	go m.batchProcessor()

	m.wg.Add(1)
	go m.unblockWorker()

	return m
}

// BlockIP agrega una IP a la cola de bloqueo por lotes.
// Retorna inmediatamente sin esperar (fire-and-forget).
func (m *Manager) BlockIP(ip string) error {
	if ip == "" || net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid ip: %s", ip)
	}

	m.mu.Lock()
	// Verificar límite de IPs bloqueadas
	if len(m.scheduled) >= maxBlockedIPs {
		m.mu.Unlock()
		log.Printf("[WARN] firewall: límite de %d IPs bloqueadas alcanzado, descartando ban de %s", maxBlockedIPs, ip)
		return nil
	}

	// Verificar si ya está programada o pendiente
	if _, exists := m.scheduled[ip]; exists {
		m.mu.Unlock()
		return nil // ya programada, no duplicar
	}

	// Verificar si ya está pendiente de bloqueo
	m.pendingBlocksMu.Lock()
	if m.pendingBlocks[ip] {
		m.pendingBlocksMu.Unlock()
		m.mu.Unlock()
		return nil // ya pendiente
	}
	m.pendingBlocks[ip] = true
	m.pendingBlocksMu.Unlock()

	// Agregar a scheduled (se procesará en el próximo batch)
	m.scheduled[ip] = time.Now().Add(time.Duration(m.blockSec) * time.Second)
	m.mu.Unlock()

	// Retornar inmediatamente (fire-and-forget)
	return nil
}

// batchProcessor procesa bloqueos en lotes cada 5 segundos
func (m *Manager) batchProcessor() {
	defer m.wg.Done()
	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			// Procesar batch final antes de salir
			m.processBatch()
			return
		case <-ticker.C:
			m.processBatch()
		}
	}
}

// processBatch procesa todas las IPs pendientes en lotes
func (m *Manager) processBatch() {
	m.pendingBlocksMu.Lock()
	if len(m.pendingBlocks) == 0 {
		m.pendingBlocksMu.Unlock()
		return
	}

	// Copiar IPs pendientes a un slice
	ipsToBlock := make([]string, 0, len(m.pendingBlocks))
	for ip := range m.pendingBlocks {
		ipsToBlock = append(ipsToBlock, ip)
	}
	// Limpiar pendientes
	m.pendingBlocks = make(map[string]bool)
	m.pendingBlocksMu.Unlock()

	if len(ipsToBlock) == 0 {
		return
	}

	// Procesar en lotes de maxBatchSize
	for i := 0; i < len(ipsToBlock); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(ipsToBlock) {
			end = len(ipsToBlock)
		}
		batch := ipsToBlock[i:end]
		
		// Procesar batch en goroutine para no bloquear
		go m.executeBatch(batch)
	}
}

// executeBatch ejecuta bloqueos de un lote de IPs
func (m *Manager) executeBatch(ips []string) {
	for _, ip := range ips {
		// Adquirir semáforo
		select {
		case m.workerSem <- struct{}{}:
			// Ejecutar bloqueo
			err := m.executeBlock(ip)
			<-m.workerSem // Liberar semáforo
			
			if err != nil {
				// Si falló, quitar de scheduled
				m.mu.Lock()
				delete(m.scheduled, ip)
				m.mu.Unlock()
			}
		case <-m.ctx.Done():
			return
		}
	}
}

// executeBlock ejecuta el comando netsh para bloquear una IP
func (m *Manager) executeBlock(ip string) error {
	ruleName := rulePrefix + strings.ReplaceAll(ip, ".", "-")

	ctx, cancel := context.WithTimeout(m.ctx, netshTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "netsh", "advfirewall", "firewall", "add", "rule",
		"name="+ruleName,
		"dir=in",
		"action=block",
		"remoteip="+ip,
		"enable=yes",
		"profile=any",
		"protocol=any",
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timeout ejecutando netsh para IP %s", ip)
		}
		if ctx.Err() == context.Canceled {
			return fmt.Errorf("comando cancelado para IP %s", ip)
		}
		return fmt.Errorf("firewall block %s: %w", ip, err)
	}
	return nil
}

// UnblockIP elimina la regla para la IP.
// Se ejecuta de forma asíncrona a través de la cola.
func (m *Manager) UnblockIP(ip string) error {
	if ip == "" {
		return nil
	}

	// Enviar a la cola de desbloqueo (no bloqueante)
	select {
	case m.unblockQueue <- ip:
		return nil
	default:
		// Si la cola está llena, ejecutar directamente en goroutine
		go m.executeUnblock(ip)
		m.mu.Lock()
		delete(m.scheduled, ip)
		m.pendingBlocksMu.Lock()
		delete(m.pendingBlocks, ip)
		m.pendingBlocksMu.Unlock()
		m.mu.Unlock()
		return nil
	}
}

// unblockWorker procesa solicitudes de desbloqueo
func (m *Manager) unblockWorker() {
	defer m.wg.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case ip := <-m.unblockQueue:
			m.executeUnblock(ip)
			// Quitar de scheduled y pending
			m.mu.Lock()
			delete(m.scheduled, ip)
			m.mu.Unlock()
			m.pendingBlocksMu.Lock()
			delete(m.pendingBlocks, ip)
			m.pendingBlocksMu.Unlock()
		}
	}
}

// executeUnblock ejecuta el comando netsh para desbloquear una IP
func (m *Manager) executeUnblock(ip string) error {
	ruleName := rulePrefix + strings.ReplaceAll(ip, ".", "-")

	ctx, cancel := context.WithTimeout(m.ctx, netshTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "netsh", "advfirewall", "firewall", "delete", "rule",
		"name="+ruleName,
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	_ = cmd.Run() // Ignorar errores en desbloqueo (puede que la regla ya no exista)
	return nil
}

// RunScheduler debe ejecutarse en una goroutine; elimina reglas cuando expira el tiempo.
func (m *Manager) RunScheduler(ctxDone <-chan struct{}) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctxDone:
			return
		case <-m.ctx.Done():
			return
		case <-tick.C:
			m.removeExpired()
		}
	}
}

func (m *Manager) removeExpired() {
	now := time.Now()
	m.mu.Lock()
	var toRemove []string
	for ip, until := range m.scheduled {
		if now.After(until) {
			toRemove = append(toRemove, ip)
		}
	}
	m.mu.Unlock()

	// Enviar todas las IPs a desbloquear a la cola (no bloqueante)
	for _, ip := range toRemove {
		select {
		case m.unblockQueue <- ip:
		default:
			// Si la cola está llena, ejecutar directamente en goroutine
			go m.executeUnblock(ip)
			m.mu.Lock()
			delete(m.scheduled, ip)
			m.mu.Unlock()
			m.pendingBlocksMu.Lock()
			delete(m.pendingBlocks, ip)
			m.pendingBlocksMu.Unlock()
		}
	}
}

// GetScheduledUnblocks retorna una copia del mapa IP → tiempo de desbloqueo.
func (m *Manager) GetScheduledUnblocks() map[string]time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]time.Time, len(m.scheduled))
	for ip, t := range m.scheduled {
		result[ip] = t
	}
	return result
}

// Stop detiene todos los workers y cierra el manager
func (m *Manager) Stop() {
	m.cancel()
	m.wg.Wait()
}
