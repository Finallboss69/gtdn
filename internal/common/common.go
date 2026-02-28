package common

import (
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"guard/internal/config"
)

// LogLevel convierte un string de nivel de log a int
func LogLevel(s string) int {
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

// IPLogThrottle limita los logs por IP para evitar spam
type IPLogThrottle struct {
	mu     sync.Mutex
	lastAt map[string]time.Time
	window time.Duration
}

func NewIPLogThrottle(window time.Duration) *IPLogThrottle {
	return &IPLogThrottle{
		window: window,
		lastAt: make(map[string]time.Time),
	}
}

func (t *IPLogThrottle) Allow(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
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

// Logger maneja el logging con throttling por IP
type Logger struct {
	level      int
	throttle   *IPLogThrottle
	rejectCnt  atomic.Uint64
	lastReject atomic.Uint64
}

func NewLogger(level int) *Logger {
	return &Logger{
		level:    level,
		throttle: NewIPLogThrottle(2 * time.Second),
	}
}

func (l *Logger) LogMsg(lvl int, ip, msg string, args ...interface{}) {
	if lvl < l.level {
		return
	}
	if ip != "" && !l.throttle.Allow(ip) {
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

func (l *Logger) IncrementReject() {
	l.rejectCnt.Add(1)
}

func (l *Logger) GetRejectCount() uint64 {
	return l.rejectCnt.Load()
}

func (l *Logger) GetLastReject() uint64 {
	return l.lastReject.Load()
}

func (l *Logger) SetLastReject(v uint64) {
	l.lastReject.Store(v)
}

// IsConsolePresent verifica si hay una consola disponible
func IsConsolePresent() bool {
	stat, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	mode := stat.Mode()
	return (mode & os.ModeCharDevice) != 0
}

// SetupInitialLogging configura logging básico antes de cargar la configuración
func SetupInitialLogging(logName string) *os.File {
	if !IsConsolePresent() {
		exeDir := filepath.Dir(os.Args[0])
		if exeDir == "" {
			exeDir = "."
		}
		logPath := filepath.Join(exeDir, logName)

		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err == nil {
			log.SetOutput(logFile)
			return logFile
		}
		logFile, err = os.OpenFile(logName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err == nil {
			log.SetOutput(logFile)
			return logFile
		}
	}
	return nil
}

// SetupLogging configura el sistema de logging según el entorno
func SetupLogging(cfg config.ProfileConfig, logName string) (*os.File, error) {
	if cfg.LogFile != "" {
		logFile, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return nil, err
		}
		log.SetOutput(logFile)
		return logFile, nil
	}

	if !IsConsolePresent() {
		exeDir := filepath.Dir(os.Args[0])
		if exeDir == "" {
			exeDir = "."
		}
		logPath := filepath.Join(exeDir, logName)

		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err == nil {
			log.SetOutput(logFile)
			log.Printf("[INFO] ejecutando como servicio, logs redirigidos a %s", logPath)
			return logFile, nil
		}
		logFile, err = os.OpenFile(logName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err == nil {
			log.SetOutput(logFile)
			log.Printf("[INFO] ejecutando como servicio, logs redirigidos a %s (directorio actual)", logName)
			return logFile, nil
		}
		log.SetOutput(os.Stderr)
		log.Printf("[WARN] no se pudo crear archivo de log, usando stderr: %v", err)
	}

	return nil, nil
}
