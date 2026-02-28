package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"

	"guard/internal/firewall"
	"guard/internal/limiter"
	"guard/internal/proxy"
)

type Config struct {
	ListenAddr            string  `json:"listen_addr"`
	BackendAddr           string  `json:"backend_addr"`
	MaxLiveConnsPerIP     int     `json:"max_live_conns_per_ip"`
	AttemptRefillPerSec   float64 `json:"attempt_refill_per_sec"`
	AttemptBurst          float64 `json:"attempt_burst"`
	DeniesBeforeTempBlock int     `json:"denies_before_tempblock"`
	TempBlockSeconds      int     `json:"tempblock_seconds"`
	MaxTotalConns         int     `json:"max_total_conns"`
	IdleTimeoutSeconds    int     `json:"idle_timeout_seconds"`
	StaleAfterSeconds     int     `json:"stale_after_seconds"`
	CleanupEverySeconds   int     `json:"cleanup_every_seconds"`
	EnableFirewallAutoban bool    `json:"enable_firewall_autoban"`
	FirewallBlockSeconds  int     `json:"firewall_block_seconds"`
	LogLevel              string  `json:"log_level"`
	LogFile               string  `json:"log_file"` // Si está vacío, usa stderr/consola. Si no hay consola, usa guard.log
}

func defaultConfig() Config {
	return Config{
		ListenAddr:            "0.0.0.0:7666",
		BackendAddr:           "127.0.0.1:7667",
		MaxLiveConnsPerIP:     3,
		AttemptRefillPerSec:   1.5,
		AttemptBurst:          5,
		DeniesBeforeTempBlock: 15,
		TempBlockSeconds:      60,
		MaxTotalConns:         3000,
		IdleTimeoutSeconds:    20,
		StaleAfterSeconds:     180,
		CleanupEverySeconds:   30,
		EnableFirewallAutoban: true,
		FirewallBlockSeconds:  600,
		LogLevel:              "info",
	}
}

func loadConfig(path string) (Config, error) {
	c := defaultConfig()
	if path == "" {
		// Intentar múltiples ubicaciones para config.json
		possiblePaths := []string{
			"config.json", // Directorio actual
			filepath.Join(filepath.Dir(os.Args[0]), "config.json"), // Mismo directorio que el ejecutable
		}
		// Si estamos en Windows, también intentar rutas comunes
		if exeDir := filepath.Dir(os.Args[0]); exeDir != "" {
			possiblePaths = append(possiblePaths,
				filepath.Join(exeDir, "config.json"),
			)
		}

		var found bool
		for _, p := range possiblePaths {
			data, err := os.ReadFile(p)
			if err == nil {
				if err := json.Unmarshal(data, &c); err != nil {
					return c, fmt.Errorf("error parsing %s: %w", p, err)
				}
				found = true
				break
			}
		}
		if !found {
			// No se encontró config.json, usar valores por defecto
			return c, nil
		}
		return c, nil
	}

	// Ruta específica proporcionada
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	return c, nil
}

// nivel de log: 0=debug, 1=info, 2=warn, 3=error
func logLevel(s string) int {
	switch s {
	case "debug":
		return 0
	case "info":
		return 1
	case "warn":
		return 2
	case "error":
		return 3
	default:
		return 1
	}
}

type ipLogThrottle struct {
	mu     sync.Mutex
	lastAt map[string]time.Time
	window time.Duration
}

func (t *ipLogThrottle) allow(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if t.lastAt == nil {
		t.lastAt = make(map[string]time.Time)
	}
	last, ok := t.lastAt[ip]
	if ok && now.Sub(last) < t.window {
		return false
	}
	// Limpiar entradas antiguas si el mapa crece demasiado (evita memory leak bajo DDoS)
	if len(t.lastAt) > 10000 {
		cutoff := now.Add(-t.window)
		for k, v := range t.lastAt {
			if v.Before(cutoff) {
				delete(t.lastAt, k)
			}
		}
	}
	t.lastAt[ip] = now
	return true
}

var (
	level      int
	throttle   = &ipLogThrottle{window: 2 * time.Second}
	rejectCnt  atomic.Uint64
	lastReject atomic.Uint64
)

func logMsg(lvl int, ip, msg string, args ...interface{}) {
	if lvl < level {
		return
	}
	if ip != "" && !throttle.allow(ip) {
		return
	}
	pre := "[INFO] "
	switch lvl {
	case 0:
		pre = "[DEBUG] "
	case 2:
		pre = "[WARN] "
	case 3:
		pre = "[ERROR] "
	}
	log.Printf(pre+msg, args...)
}

// isConsolePresent verifica si hay una consola disponible.
// En Windows, cuando se ejecuta como servicio, no hay consola.
func isConsolePresent() bool {
	// Intentar obtener el handle de stdout
	stat, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	// Verificar si es un dispositivo de consola (no un archivo redirigido)
	mode := stat.Mode()
	// En Windows, si es un archivo regular, probablemente está redirigido
	// Si es un dispositivo de caracteres, es una consola
	return (mode & os.ModeCharDevice) != 0
}

// setupInitialLogging configura logging básico antes de cargar la configuración.
// Esto asegura que podamos capturar errores tempranos incluso cuando se ejecuta como servicio.
func setupInitialLogging() *os.File {
	// Si no hay consola, crear un log básico en el directorio del ejecutable
	if !isConsolePresent() {
		exeDir := filepath.Dir(os.Args[0])
		if exeDir == "" {
			exeDir = "."
		}
		logPath := filepath.Join(exeDir, "guard.log")

		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err == nil {
			log.SetOutput(logFile)
			return logFile
		}
		// Si falla, intentar en directorio actual
		logFile, err = os.OpenFile("guard.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err == nil {
			log.SetOutput(logFile)
			return logFile
		}
	}
	// Si hay consola o si falló todo, usar stderr
	return nil
}

// setupLogging configura el sistema de logging según el entorno.
func setupLogging(cfg Config) (*os.File, error) {
	var logFile *os.File
	var err error

	// Si se especificó un archivo de log en la configuración, usarlo
	if cfg.LogFile != "" {
		logFile, err = os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return nil, fmt.Errorf("no se pudo abrir archivo de log %s: %w", cfg.LogFile, err)
		}
		log.SetOutput(logFile)
		return logFile, nil
	}

	// Si no hay consola (ejecutando como servicio), redirigir a guard.log
	if !isConsolePresent() {
		// Usar el directorio del ejecutable, no el directorio de trabajo actual
		// (que puede ser C:\Windows\System32 cuando se ejecuta como servicio)
		exeDir := filepath.Dir(os.Args[0])
		if exeDir == "" {
			exeDir = "." // Fallback al directorio actual
		}
		logPath := filepath.Join(exeDir, "guard.log")

		logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err == nil {
			log.SetOutput(logFile)
			log.Printf("[INFO] ejecutando como servicio, logs redirigidos a %s", logPath)
			return logFile, nil
		}
		// Si falla, intentar en el directorio actual como último recurso
		logFile, err = os.OpenFile("guard.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err == nil {
			log.SetOutput(logFile)
			log.Printf("[INFO] ejecutando como servicio, logs redirigidos a guard.log (directorio actual)")
			return logFile, nil
		}
		// Si todo falla, continuar con stderr (puede que se capture en algún lugar)
		// No retornar error para que el programa pueda continuar
		log.SetOutput(os.Stderr)
		log.Printf("[WARN] no se pudo crear archivo de log, usando stderr: %v", err)
	}

	// Si hay consola, usar stderr (comportamiento por defecto de log)
	return nil, nil
}

// guardService implementa la interfaz de servicio de Windows
type guardService struct {
	cfg Config
}

func (s *guardService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	// Configurar logging
	initialLogFile := setupInitialLogging()
	if initialLogFile != nil {
		defer initialLogFile.Close()
	}

	logFile, err := setupLogging(s.cfg)
	if err != nil {
		log.Printf("[ERROR] setup logging falló: %v", err)
	} else if logFile != nil {
		if initialLogFile != nil && initialLogFile != logFile {
			initialLogFile.Close()
		}
		defer logFile.Close()
	}

	log.Println("[INFO] guard iniciando como servicio de Windows...")
	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

	// Ejecutar el guard en una goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- runGuard(ctx, s.cfg)
	}()
	// Manejar comandos del servicio
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				log.Println("[INFO] recibido comando de detención del servicio")
				cancel()
				changes <- svc.Status{State: svc.StopPending}
				// Esperar a que runGuard termine
				select {
				case err := <-errChan:
					if err != nil {
						log.Printf("[ERROR] error al detener: %v", err)
					}
				case <-time.After(10 * time.Second):
					log.Printf("[WARN] timeout esperando que guard termine")
				}
				return false, 0
			case svc.Interrogate:
				changes <- c.CurrentStatus
			default:
				log.Printf("[WARN] comando de servicio no reconocido: %v", c.Cmd)
			}
		case err := <-errChan:
			if err != nil {
				log.Printf("[ERROR] guard terminó con error: %v", err)
				changes <- svc.Status{State: svc.Stopped}
				return false, 1
			}
			log.Println("[INFO] guard terminó normalmente")
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

func main() {
	// Detectar si estamos ejecutando como servicio
	isIntSess, err := svc.IsAnInteractiveSession()
	if err != nil {
		log.Fatalf("error detectando sesión interactiva: %v", err)
	}

	if !isIntSess {
		// Ejecutar como servicio de Windows
		elog, err := eventlog.Open("GuardProxy")
		if err != nil {
			log.Fatalf("error abriendo event log: %v", err)
		}
		defer elog.Close()

		elog.Info(1, "GuardProxy iniciando como servicio")

		// Cargar configuración antes de crear el servicio
		cfg, err := loadConfig("")
		if err != nil {
			elog.Error(1, fmt.Sprintf("error cargando configuración: %v", err))
			log.Fatalf("config: %v", err)
		}

		service := &guardService{cfg: cfg}
		err = svc.Run("GuardProxy", service)
		if err != nil {
			elog.Error(1, fmt.Sprintf("error ejecutando servicio: %v", err))
			log.Fatalf("servicio falló: %v", err)
		}
		return
	}

	// Ejecutar en modo consola (no como servicio)
	runGuardConsole()
}

func runGuardConsole() {
	// Capturar panics para evitar que el servicio se detenga silenciosamente
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[FATAL] panic recuperado: %v", r)
			// Dar tiempo para que el log se escriba
			time.Sleep(1 * time.Second)
			panic(r) // Re-lanzar para que el servicio vea el error
		}
	}()

	// Configurar logging básico primero (antes de cargar config)
	// Esto asegura que podamos loggear errores incluso si no hay consola
	initialLogFile := setupInitialLogging()
	if initialLogFile != nil {
		defer initialLogFile.Close()
	}

	cfg, err := loadConfig("")
	if err != nil {
		log.Printf("[ERROR] error cargando configuración: %v", err)
		log.Fatalf("config: %v", err)
	}

	// Reconfigurar logging con la configuración cargada (puede sobrescribir el inicial)
	logFile, err := setupLogging(cfg)
	if err != nil {
		log.Printf("[ERROR] setup logging falló: %v", err)
		// No hacer fatal aquí, continuar con el logging inicial si existe
	} else if logFile != nil {
		// Cerrar el logging inicial si se creó uno nuevo
		if initialLogFile != nil && initialLogFile != logFile {
			initialLogFile.Close()
		}
		defer logFile.Close()
	}

	log.Println("[INFO] guard iniciando...")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = runGuard(ctx, cfg)
	if err != nil {
		log.Fatalf("[ERROR] guard falló: %v", err)
	}
}

func runGuard(ctx context.Context, cfg Config) error {
	level = logLevel(cfg.LogLevel)
	idleTimeout := time.Duration(cfg.IdleTimeoutSeconds) * time.Second
	if idleTimeout <= 0 {
		idleTimeout = 20 * time.Second
	}

	lim := limiter.New(
		cfg.MaxLiveConnsPerIP,
		cfg.AttemptRefillPerSec,
		cfg.AttemptBurst,
		cfg.DeniesBeforeTempBlock,
		cfg.TempBlockSeconds,
		cfg.MaxTotalConns,
		cfg.StaleAfterSeconds,
		cfg.CleanupEverySeconds,
	)
	defer lim.Stop()

	var fw *firewall.Manager
	if cfg.EnableFirewallAutoban {
		fw = firewall.New(cfg.FirewallBlockSeconds)
	}

	// Crear un contexto cancelable desde el contexto recibido
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Solo manejar señales si estamos en modo consola (no como servicio)
	// Los servicios de Windows manejan las señales a través del SCM
	isIntSess, _ := svc.IsAnInteractiveSession()
	if isIntSess {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sig
			log.Println("[INFO] shutdown signal received")
			cancel()
		}()
	}

	// Heartbeat para mantener el servicio activo y detectar si se detiene inesperadamente
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				// Heartbeat periódico para mantener el servicio activo
				// Esto ayuda a detectar si el servicio se detiene inesperadamente
				log.Printf("[DEBUG] heartbeat - servicio activo, contexto OK")
			}
		}
	}()

	if fw != nil {
		go fw.RunScheduler(ctx.Done())
	}

	// Métricas cada 10s
	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				active, ips := lim.Stats()
				rej := rejectCnt.Load()
				prev := lastReject.Load()
				lastReject.Store(rej)
				rate := float64(rej-prev) / 10.0
				log.Printf("[INFO] metrics active_conns=%d ips_in_memory=%d rejects_per_10s=%.1f semaphore_used=%d/%d",
					active, ips, rate, active, cfg.MaxTotalConns)
			}
		}
	}()

	tryAccept := func(ip string) (bool, string) {
		return lim.TryAccept(ip, time.Now())
	}
	onAccept := func(ip string) {
		logMsg(1, ip, "accept allowed client=%s", ip)
	}
	onReject := func(ip, reason string) {
		rejectCnt.Add(1)
		switch reason {
		case "rate":
			lim.RecordDeny(ip)
			logMsg(2, ip, "reject rate client=%s", ip)
		case "live_limit":
			logMsg(2, ip, "reject live_limit client=%s", ip)
		case "global_limit":
			logMsg(2, ip, "reject global_limit client=%s", ip)
		case "tempblock":
			logMsg(2, ip, "reject tempblock client=%s", ip)
			if fw != nil && lim.IsTempBlocked(ip) {
				if err := fw.BlockIP(ip); err != nil {
					logMsg(3, ip, "firewall ban failed client=%s err=%v", ip, err)
				} else {
					logMsg(2, ip, "firewall ban client=%s", ip)
				}
			}
		case "backend_fail":
			logMsg(3, ip, "backend connect fail client=%s", ip)
		default:
			logMsg(2, ip, "reject reason=%s client=%s", reason, ip)
		}
	}
	onRelease := func(ip string) {
		lim.Release(ip)
	}

	log.Printf("[INFO] guard listening on %s -> %s", cfg.ListenAddr, cfg.BackendAddr)
	log.Printf("[INFO] directorio de trabajo: %s", func() string {
		wd, err := os.Getwd()
		if err != nil {
			return "error obteniendo directorio"
		}
		return wd
	}())
	log.Printf("[INFO] directorio del ejecutable: %s", filepath.Dir(os.Args[0]))

	// Ejecutar proxy.Run y capturar cualquier error o terminación inesperada
	log.Printf("[INFO] iniciando proxy.Run...")
	err := proxy.Run(ctx, cfg.ListenAddr, cfg.BackendAddr, idleTimeout, 0, tryAccept, onAccept, onReject, onRelease, nil)

	// Loggear información de diagnóstico
	log.Printf("[INFO] proxy.Run retornó, error: %v", err)
	log.Printf("[INFO] ctx.Err(): %v", ctx.Err())

	if err != nil {
		if ctx.Err() != nil {
			// El contexto fue cancelado, probablemente por señal de shutdown
			log.Printf("[INFO] proxy detenido por cancelación de contexto: %v", err)
			return nil // Cancelación normal, no es un error
		} else {
			// Error inesperado - esto es lo que probablemente está pasando
			log.Printf("[ERROR] proxy error inesperado: %v", err)
			log.Printf("[ERROR] tipo de error: %T", err)
			return fmt.Errorf("proxy error: %w", err)
		}
	} else {
		// proxy.Run retornó sin error
		if ctx.Err() != nil {
			log.Printf("[INFO] proxy terminó normalmente, contexto cancelado: %v", ctx.Err())
			return nil
		} else {
			// Esto es anormal - proxy.Run no debería retornar sin error si el contexto no está cancelado
			log.Printf("[WARN] proxy.Run retornó sin error pero el contexto no está cancelado")
			log.Printf("[WARN] esto puede indicar que el listener se cerró inesperadamente")
			return fmt.Errorf("proxy terminó inesperadamente")
		}
	}
}
