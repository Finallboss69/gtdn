package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"

	"guard/internal/admin"
	"guard/internal/common"
	"guard/internal/config"
	"guard/internal/firewall"
	"guard/internal/limiter"
	"guard/internal/proxy"
)

var (
	configPath  = flag.String("config", "", "Ruta al archivo de configuración (default: busca config.json)")
	profileName = flag.String("profile", "game", "Perfil a usar: login o game")
	logLevel    = flag.String("log-level", "", "Override del nivel de log (debug|info|warn|error)")
	serviceName = "GuardGame"
)

// guardService implementa la interfaz de servicio de Windows
type guardService struct {
	cfg config.ProfileConfig
}

func (s *guardService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	// Configurar logging
	initialLogFile := common.SetupInitialLogging("guard-game.log")
	if initialLogFile != nil {
		defer initialLogFile.Close()
	}

	logFile, err := common.SetupLogging(s.cfg, "guard-game.log")
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
	initialLogFile := common.SetupInitialLogging("guard-game.log")
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
	logFile, err := common.SetupLogging(cfg, "guard-game.log")
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
	// Validar configuración
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("config inválida: %w", err)
	}
	log.Printf("[INFO] config validada OK")

	logger := common.NewLogger(common.LogLevel(cfg.LogLevel))
	idleTimeout := time.Duration(cfg.IdleTimeoutSeconds) * time.Second
	if idleTimeout <= 0 {
		idleTimeout = 20 * time.Second
	}
	backendDialTimeout := time.Duration(cfg.BackendDialTimeoutSeconds) * time.Second
	if backendDialTimeout <= 0 {
		backendDialTimeout = 10 * time.Second
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
		defer fw.Stop()
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

	// Servidor de administración
	var adminSrv *admin.Server
	if cfg.AdminListenAddr != "" {
		adminSrv = admin.New(lim, fw, "game", nil, logger.GetRejectCount, cfg.MaxTotalConns)
		adminSrv.SetAccessControl(cfg.AdminAllowIPs, cfg.AdminToken)
		// Función de % de carga para el panel
		adminSrv.SetLoadPctFn(func() float64 {
			active, _ := lim.Stats()
			if cfg.MaxTotalConns <= 0 {
				return 0
			}
			return float64(active) * 100.0 / float64(cfg.MaxTotalConns)
		})
		go func() {
			if err := adminSrv.Start(ctx, cfg.AdminListenAddr); err != nil {
				log.Printf("[WARN] admin server terminó: %v", err)
			}
		}()
	}

	// Métricas cada 10s con detección de carga alta
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
				log.Printf("[INFO] metrics active_conns=%d ips_in_memory=%d rejects_per_10s=%.1f semaphore_used=%d/%d",
					active, ips, rate, active, cfg.MaxTotalConns)

				// Detección de carga alta (≥90%)
				if cfg.MaxTotalConns > 0 {
					pct := float64(active) * 100 / float64(cfg.MaxTotalConns)
					if pct >= 90 {
						log.Printf("[WARN] GAME HIGH LOAD: active=%d (%.0f%% del limite) rejects/10s=%.1f", active, pct, rate)
						if adminSrv != nil {
							adminSrv.AddEvent("overload_start", "", fmt.Sprintf("%.0f%%", pct))
						}
					}
				}
			}
		}
	}()

	tryAccept := func(ip string) (bool, string) {
		return lim.TryAccept(ip, time.Now())
	}
	onAccept := func(ip string) {
		logger.LogMsg(1, ip, "accept allowed client=%s", ip)
	}
	onReject := func(ip, reason string) {
		logger.IncrementReject()
		switch reason {
		case "rate":
			lim.RecordDeny(ip)
			logger.LogMsg(2, ip, "reject rate client=%s", ip)
		case "live_limit":
			logger.LogMsg(2, ip, "reject live_limit client=%s", ip)
		case "global_limit":
			logger.LogMsg(2, ip, "reject global_limit client=%s", ip)
		case "tempblock":
			logger.LogMsg(2, ip, "reject tempblock client=%s", ip)
			if fw != nil && lim.IsTempBlocked(ip) {
				go func(ipAddr string) {
					if err := fw.BlockIP(ipAddr); err != nil {
						logger.LogMsg(3, ipAddr, "firewall ban failed client=%s err=%v", ipAddr, err)
					} else {
						logger.LogMsg(2, ipAddr, "firewall ban queued client=%s", ipAddr)
					}
				}(ip)
			}
			if adminSrv != nil {
				adminSrv.AddEvent("ban", ip, "tempblock")
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

	log.Printf("[INFO] guard-game listening on %s -> %s", cfg.ListenAddr, cfg.BackendAddr)
	log.Printf("[INFO] directorio de trabajo: %s", func() string {
		wd, err := os.Getwd()
		if err != nil {
			return "error obteniendo directorio"
		}
		return wd
	}())
	log.Printf("[INFO] directorio del ejecutable: %s", filepath.Dir(os.Args[0]))

	// No hay modo drain para game
	shouldDrain := func() bool { return false }

	log.Printf("[INFO] iniciando proxy.Run...")
	err := proxy.Run(ctx, cfg.ListenAddr, cfg.BackendAddr, idleTimeout, backendDialTimeout,
		tryAccept, onAccept, onReject, onRelease, shouldDrain)

	log.Printf("[INFO] proxy.Run retornó, error: %v", err)
	log.Printf("[INFO] ctx.Err(): %v", ctx.Err())

	if err != nil {
		if ctx.Err() != nil {
			log.Printf("[INFO] proxy detenido por cancelación de contexto: %v", err)
			return nil
		}
		log.Printf("[ERROR] proxy error inesperado: %v", err)
		log.Printf("[ERROR] tipo de error: %T", err)
		return fmt.Errorf("proxy error: %w", err)
	}
	if ctx.Err() != nil {
		log.Printf("[INFO] proxy terminó normalmente, contexto cancelado: %v", ctx.Err())
		return nil
	}
	log.Printf("[WARN] proxy.Run retornó sin error pero el contexto no está cancelado")
	log.Printf("[WARN] esto puede indicar que el listener se cerró inesperadamente")
	return fmt.Errorf("proxy terminó inesperadamente")
}
