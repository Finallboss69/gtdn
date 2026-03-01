package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"

	"guard/internal/common"
	"guard/internal/config"
	"guard/internal/firewall"
	"guard/internal/limiter"
	"guard/internal/proxy"
)

var (
	configPath  = flag.String("config", "", "Ruta al archivo de configuración (default: busca config.json)")
	profileName = flag.String("profile", "login", "Perfil a usar: login o game")
	logLevel    = flag.String("log-level", "", "Override del nivel de log (debug|info|warn|error)")
	serviceName = "GuardLogin"
)

// guardService implementa la interfaz de servicio de Windows
type guardService struct {
	cfg config.ProfileConfig
}

func (s *guardService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	// Configurar logging
	initialLogFile := common.SetupInitialLogging("guard-login.log")
	if initialLogFile != nil {
		defer initialLogFile.Close()
	}

	logFile, err := common.SetupLogging(s.cfg, "guard-login.log")
	if err != nil {
		log.Printf("[ERROR] setup logging falló: %v", err)
	} else if logFile != nil {
		if initialLogFile != nil && initialLogFile != logFile {
			initialLogFile.Close()
		}
		defer logFile.Close()
	}

	log.Printf("[INFO] %s iniciando como servicio de Windows...", serviceName)
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
				log.Printf("[INFO] recibido comando de detención del servicio")
				cancel()
				changes <- svc.Status{State: svc.StopPending}
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
			log.Printf("[INFO] guard terminó normalmente")
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

func main() {
	flag.Parse()

	// Detectar si estamos ejecutando como servicio
	isIntSess, err := svc.IsAnInteractiveSession()
	if err != nil {
		log.Fatalf("error detectando sesión interactiva: %v", err)
	}

	if !isIntSess {
		// Ejecutar como servicio de Windows
		elog, err := eventlog.Open(serviceName)
		if err != nil {
			log.Fatalf("error abriendo event log: %v", err)
		}
		defer elog.Close()

		elog.Info(1, fmt.Sprintf("%s iniciando como servicio", serviceName))

		// Cargar configuración
		cfg, err := config.LoadProfile(*configPath, *profileName)
		if err != nil {
			elog.Error(1, fmt.Sprintf("error cargando configuración: %v", err))
			log.Fatalf("config: %v", err)
		}

		// Override log level si se especificó
		if *logLevel != "" {
			cfg.LogLevel = *logLevel
		}

		service := &guardService{cfg: cfg}
		err = svc.Run(serviceName, service)
		if err != nil {
			elog.Error(1, fmt.Sprintf("error ejecutando servicio: %v", err))
			log.Fatalf("servicio falló: %v", err)
		}
		return
	}

	// Ejecutar en modo consola
	runGuardConsole()
}

func runGuardConsole() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[FATAL] panic recuperado: %v", r)
			time.Sleep(1 * time.Second)
			panic(r)
		}
	}()

	// Configurar logging básico
	initialLogFile := common.SetupInitialLogging("guard-login.log")
	if initialLogFile != nil {
		defer initialLogFile.Close()
	}

	// Cargar configuración
	cfg, err := config.LoadProfile(*configPath, *profileName)
	if err != nil {
		log.Printf("[ERROR] error cargando configuración: %v", err)
		log.Fatalf("config: %v", err)
	}

	// Override log level si se especificó
	if *logLevel != "" {
		cfg.LogLevel = *logLevel
	}

	// Reconfigurar logging
	logFile, err := common.SetupLogging(cfg, "guard-login.log")
	if err != nil {
		log.Printf("[ERROR] setup logging falló: %v", err)
	} else if logFile != nil {
		if initialLogFile != nil && initialLogFile != logFile {
			initialLogFile.Close()
		}
		defer logFile.Close()
	}

	log.Printf("[INFO] %s iniciando...", serviceName)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = runGuard(ctx, cfg)
	if err != nil {
		log.Fatalf("[ERROR] guard falló: %v", err)
	}
}

func runGuard(ctx context.Context, cfg config.ProfileConfig) error {
	logger := common.NewLogger(common.LogLevel(cfg.LogLevel))
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
		defer fw.Stop() // Asegurar que los workers se detengan correctamente
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Solo manejar señales si estamos en modo consola
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

	// Heartbeat
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				log.Printf("[DEBUG] heartbeat - servicio activo, contexto OK")
			}
		}
	}()

	if fw != nil {
		go fw.RunScheduler(ctx.Done())
	}

	// Sistema de protección contra sobrecarga con auto-recuperación (solo para login)
	var (
		overloadMu          sync.RWMutex
		isOverloaded        bool
		overloadStartTime   time.Time                              // Cuándo empezó la sobrecarga
		overloadThreshold   = uint64(cfg.MaxTotalConns * 80 / 100) // 80% del límite
		criticalThreshold   = uint64(cfg.MaxTotalConns * 90 / 100) // 90% del límite - activación inmediata
		rejectRateThreshold = 50.0                                 // 50 rechazos por 10 segundos
		drainThreshold      = 5 * time.Second                      // Entrar en drain después de 5s de sobrecarga (muy agresivo)
		inDrainMode         bool                                   // Si estamos en modo drain
	)

	// Verificación rápida de sobrecarga crítica cada 2 segundos
	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				active, _ := lim.Stats()
				overloadMu.Lock()
				wasInDrain := inDrainMode
				// Verificar nivel crítico (90%+) - activación inmediata
				isCritical := active >= int(criticalThreshold)
				if isCritical && !inDrainMode {
					inDrainMode = true
					log.Printf("[CRITICAL] NIVEL CRÍTICO DETECTADO: active_conns=%d (90%%+ del límite) - ENTRANDO EN MODO DRAIN INMEDIATAMENTE",
						active)
				}
				// Si estamos en drain y bajó significativamente, salir
				if inDrainMode && active < int(overloadThreshold*60/100) {
					inDrainMode = false
					log.Printf("[INFO] Conexiones bajaron a %d (60%% del umbral) - SALIENDO DE MODO DRAIN", active)
				}
				if inDrainMode && !wasInDrain {
					log.Printf("[WARN] MODO DRAIN ACTIVADO - Listener cerrado, no aceptando nuevas conexiones")
				} else if !inDrainMode && wasInDrain {
					log.Printf("[INFO] MODO DRAIN DESACTIVADO - Listener reabierto, aceptando conexiones")
				}
				overloadMu.Unlock()
			}
		}
	}()

	// Métricas cada 10s con detección de sobrecarga
	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				active, ips := lim.Stats()
				rej := logger.GetRejectCount()
				prev := logger.GetLastReject()
				logger.SetLastReject(rej)
				rate := float64(rej-prev) / 10.0

				// Detectar sobrecarga
				overloadMu.Lock()
				wasOverloaded := isOverloaded
				currentInDrain := inDrainMode

				// Verificar si estamos en nivel crítico (90%+) - activación inmediata
				isCritical := active >= int(criticalThreshold)
				// Sobrecarga si: conexiones activas > 80% del límite O rechazos > 50 por 10s
				isOverloaded = active >= int(overloadThreshold) || rate >= rejectRateThreshold

				// Si estamos en drain, reducir frecuencia de logs para ahorrar CPU
				shouldLog := !currentInDrain || (active%10 == 0) // Log cada 10 conexiones cuando en drain

				if isOverloaded && !wasOverloaded {
					// Empezó la sobrecarga ahora
					overloadStartTime = time.Now()
					log.Printf("[WARN] SOBRECARGA DETECTADA: active_conns=%d (limite=%d) rejects_per_10s=%.1f - Activando protección agresiva",
						active, cfg.MaxTotalConns, rate)
				}

				// La verificación crítica se hace en el ticker de 2s, aquí solo manejamos sobrecarga persistente
				if isOverloaded && wasOverloaded && !isCritical {
					// Sigue en sobrecarga (pero no crítico) - verificar si debemos entrar en drain
					overloadDuration := time.Since(overloadStartTime)
					if !inDrainMode && overloadDuration >= drainThreshold {
						inDrainMode = true
						log.Printf("[WARN] SOBRECARGA PERSISTENTE (%v): active_conns=%d - ENTRANDO EN MODO DRAIN (cerrando listener temporalmente)",
							overloadDuration, active)
					}
				} else if !isOverloaded && wasOverloaded {
					// Sobrecarga resuelta
					overloadStartTime = time.Time{}
					if inDrainMode {
						inDrainMode = false
						log.Printf("[INFO] Sobrecarga resuelta: active_conns=%d rejects_per_10s=%.1f - SALIENDO DE MODO DRAIN (reabriendo listener)",
							active, rate)
					} else {
						log.Printf("[INFO] Sobrecarga resuelta: active_conns=%d rejects_per_10s=%.1f - Protección desactivada",
							active, rate)
					}
				}

				overloadMu.Unlock()

				// Solo loggear métricas si no estamos en drain o cada cierto tiempo
				if shouldLog {
					log.Printf("[INFO] metrics active_conns=%d ips_in_memory=%d rejects_per_10s=%.1f semaphore_used=%d/%d overload=%v",
						active, ips, rate, active, cfg.MaxTotalConns, isOverloaded)
				}
			}
		}
	}()

	tryAccept := func(ip string) (bool, string) {
		// Verificar sobrecarga primero (solo para login)
		overloadMu.RLock()
		overloaded := isOverloaded
		overloadMu.RUnlock()

		if overloaded {
			// En sobrecarga: rechazar inmediatamente sin verificar límites
			// Esto protege el backend de ser saturado
			return false, "overload"
		}

		// Comportamiento normal
		return lim.TryAccept(ip, time.Now())
	}
	onAccept := func(ip string) {
		logger.LogMsg(1, ip, "accept allowed client=%s", ip)
	}
	onReject := func(ip, reason string) {
		logger.IncrementReject()
		switch reason {
		case "overload":
			// Rechazo por sobrecarga: no loggear cada uno para evitar spam
			// Solo contar el rechazo
		case "rate":
			lim.RecordDeny(ip)
			logger.LogMsg(2, ip, "reject rate client=%s", ip)
		case "live_limit":
			lim.RecordDeny(ip)
			logger.LogMsg(2, ip, "reject live_limit client=%s", ip)
		case "global_limit":
			logger.LogMsg(2, ip, "reject global_limit client=%s", ip)
		case "tempblock":
			logger.LogMsg(2, ip, "reject tempblock client=%s", ip)
			if fw != nil && lim.IsTempBlocked(ip) {
				// Bloqueo asíncrono (fire-and-forget) - se procesará en el próximo batch
				go func(ipAddr string) {
					if err := fw.BlockIP(ipAddr); err != nil {
						logger.LogMsg(3, ipAddr, "firewall ban failed client=%s err=%v", ipAddr, err)
					} else {
						logger.LogMsg(2, ipAddr, "firewall ban queued client=%s (se procesará en batch)", ipAddr)
					}
				}(ip)
			}
		case "backend_fail":
			logger.LogMsg(3, ip, "backend connect fail client=%s", ip)
		default:
			logger.LogMsg(2, ip, "reject reason=%s client=%s", reason, ip)
		}
	}
	onRelease := func(ip string) {
		lim.Release(ip)
	}

	log.Printf("[INFO] guard-login listening on %s -> %s", cfg.ListenAddr, cfg.BackendAddr)
	log.Printf("[INFO] directorio de trabajo: %s", func() string {
		wd, err := os.Getwd()
		if err != nil {
			return "error obteniendo directorio"
		}
		return wd
	}())
	log.Printf("[INFO] directorio del ejecutable: %s", filepath.Dir(os.Args[0]))

	// Función para determinar si debemos entrar en modo drain
	shouldDrain := func() bool {
		overloadMu.RLock()
		defer overloadMu.RUnlock()
		return inDrainMode
	}

	log.Printf("[INFO] iniciando proxy.Run...")
	err := proxy.Run(ctx, cfg.ListenAddr, cfg.BackendAddr, idleTimeout, tryAccept, onAccept, onReject, onRelease, shouldDrain)

	log.Printf("[INFO] proxy.Run retornó, error: %v", err)
	log.Printf("[INFO] ctx.Err(): %v", ctx.Err())

	if err != nil {
		if ctx.Err() != nil {
			log.Printf("[INFO] proxy detenido por cancelación de contexto: %v", err)
			return nil
		} else {
			log.Printf("[ERROR] proxy error inesperado: %v", err)
			log.Printf("[ERROR] tipo de error: %T", err)
			return fmt.Errorf("proxy error: %w", err)
		}
	} else {
		if ctx.Err() != nil {
			log.Printf("[INFO] proxy terminó normalmente, contexto cancelado: %v", ctx.Err())
			return nil
		} else {
			log.Printf("[WARN] proxy.Run retornó sin error pero el contexto no está cancelado")
			log.Printf("[WARN] esto puede indicar que el listener se cerró inesperadamente")
			return fmt.Errorf("proxy terminó inesperadamente")
		}
	}
}
