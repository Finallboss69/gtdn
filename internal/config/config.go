package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ProfileConfig representa la configuración de un perfil (login o game)
type ProfileConfig struct {
	ListenAddr                string  `json:"listen_addr"`
	BackendAddr               string  `json:"backend_addr"`
	MaxLiveConnsPerIP         int     `json:"max_live_conns_per_ip"`
	AttemptRefillPerSec       float64 `json:"attempt_refill_per_sec"`
	AttemptBurst              float64 `json:"attempt_burst"`
	DeniesBeforeTempBlock     int     `json:"denies_before_tempblock"`
	TempBlockSeconds          int     `json:"tempblock_seconds"`
	MaxTotalConns             int     `json:"max_total_conns"`
	IdleTimeoutSeconds        int     `json:"idle_timeout_seconds"`
	StaleAfterSeconds         int     `json:"stale_after_seconds"`
	CleanupEverySeconds       int     `json:"cleanup_every_seconds"`
	EnableFirewallAutoban     bool    `json:"enable_firewall_autoban"`
	FirewallBlockSeconds      int     `json:"firewall_block_seconds"`
	LogLevel                  string  `json:"log_level"`
	LogFile                   string  `json:"log_file"`
	AdminListenAddr           string  `json:"admin_listen_addr"`
	MaxDrainSeconds           int      `json:"max_drain_seconds"`            // 0=sin límite; default 60 para login
	BackendDialTimeoutSeconds int      `json:"backend_dial_timeout_seconds"` // default 5 login, 10 game
	AdminAllowIPs             []string `json:"admin_allow_ips"`              // IPs adicionales permitidas (panel remoto)
	AdminToken                string   `json:"admin_token"`                  // token Bearer para acceso remoto
}

// Validate verifica que los campos críticos de la configuración sean válidos.
func Validate(cfg ProfileConfig) error {
	if cfg.MaxTotalConns <= 0 {
		return fmt.Errorf("max_total_conns debe ser > 0")
	}
	if cfg.AttemptRefillPerSec <= 0 {
		return fmt.Errorf("attempt_refill_per_sec debe ser > 0")
	}
	if cfg.MaxLiveConnsPerIP <= 0 {
		return fmt.Errorf("max_live_conns_per_ip debe ser > 0")
	}
	return nil
}

// MultiProfileConfig representa el archivo de configuración completo con múltiples perfiles
type MultiProfileConfig struct {
	Login ProfileConfig `json:"login"`
	Game  ProfileConfig `json:"game"`
}

// DefaultLoginConfig retorna valores por defecto para el perfil de login (más agresivo)
func DefaultLoginConfig() ProfileConfig {
	return ProfileConfig{
		ListenAddr:                "0.0.0.0:7666",
		BackendAddr:               "127.0.0.1:7668",
		MaxLiveConnsPerIP:         2,
		AttemptRefillPerSec:       1.0,
		AttemptBurst:              4,
		DeniesBeforeTempBlock:     10,
		TempBlockSeconds:          90,
		MaxTotalConns:             2000,
		IdleTimeoutSeconds:        15,
		StaleAfterSeconds:         180,
		CleanupEverySeconds:       30,
		EnableFirewallAutoban:     true,
		FirewallBlockSeconds:      900,
		LogLevel:                  "info",
		AdminListenAddr:           "127.0.0.1:7771",
		MaxDrainSeconds:           60,
		BackendDialTimeoutSeconds: 5,
	}
}

// DefaultGameConfig retorna valores por defecto para el perfil de game (más suave)
func DefaultGameConfig() ProfileConfig {
	return ProfileConfig{
		ListenAddr:                "0.0.0.0:7667",
		BackendAddr:               "127.0.0.1:7669",
		MaxLiveConnsPerIP:         3,
		AttemptRefillPerSec:       2.0,
		AttemptBurst:              6,
		DeniesBeforeTempBlock:     15,
		TempBlockSeconds:          60,
		MaxTotalConns:             4000,
		IdleTimeoutSeconds:        30,
		StaleAfterSeconds:         180,
		CleanupEverySeconds:       30,
		EnableFirewallAutoban:     true,
		FirewallBlockSeconds:      600,
		LogLevel:                  "info",
		AdminListenAddr:           "127.0.0.1:7772",
		MaxDrainSeconds:           0,
		BackendDialTimeoutSeconds: 10,
	}
}

// LoadProfile carga un perfil específico desde un archivo de configuración
// Si profileName está vacío, intenta detectarlo del nombre del ejecutable
func LoadProfile(configPath, profileName string) (ProfileConfig, error) {
	// Si no se especificó el perfil, intentar detectarlo del nombre del ejecutable
	if profileName == "" {
		if len(os.Args) > 0 {
			exeName := filepath.Base(os.Args[0])
			if exeName == "guard-login.exe" || exeName == "guard-login" {
				profileName = "login"
			} else if exeName == "guard-game.exe" || exeName == "guard-game" {
				profileName = "game"
			}
		}
	}

	// Si aún no tenemos perfil, usar "login" por defecto
	if profileName == "" {
		profileName = "login"
	}

	// Buscar el archivo de configuración
	if configPath == "" {
		possiblePaths := []string{
			"config.json",
			filepath.Join(filepath.Dir(os.Args[0]), "config.json"),
		}
		if exeDir := filepath.Dir(os.Args[0]); exeDir != "" {
			possiblePaths = append(possiblePaths, filepath.Join(exeDir, "config.json"))
		}

		for _, p := range possiblePaths {
			if _, err := os.Stat(p); err == nil {
				configPath = p
				break
			}
		}
	}

	// Si no se encontró config.json, usar valores por defecto
	if configPath == "" {
		if profileName == "login" {
			return DefaultLoginConfig(), nil
		}
		return DefaultGameConfig(), nil
	}

	// Leer el archivo de configuración
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Archivo no existe, usar valores por defecto
			if profileName == "login" {
				return DefaultLoginConfig(), nil
			}
			return DefaultGameConfig(), nil
		}
		return ProfileConfig{}, fmt.Errorf("error leyendo config: %w", err)
	}

	// Intentar parsear como configuración multi-perfil
	var multiConfig MultiProfileConfig
	if err := json.Unmarshal(data, &multiConfig); err == nil {
		// Es configuración multi-perfil
		if profileName == "login" {
			cfg := multiConfig.Login
			// Aplicar valores por defecto si están vacíos
			cfg = applyDefaults(cfg, DefaultLoginConfig())
			return cfg, nil
		} else if profileName == "game" {
			cfg := multiConfig.Game
			cfg = applyDefaults(cfg, DefaultGameConfig())
			return cfg, nil
		}
		return ProfileConfig{}, fmt.Errorf("perfil desconocido: %s (debe ser 'login' o 'game')", profileName)
	}

	// Intentar parsear como configuración simple (legacy)
	var simpleConfig ProfileConfig
	if err := json.Unmarshal(data, &simpleConfig); err == nil {
		// Es configuración simple, aplicar valores por defecto según el perfil
		if profileName == "login" {
			return applyDefaults(simpleConfig, DefaultLoginConfig()), nil
		}
		return applyDefaults(simpleConfig, DefaultGameConfig()), nil
	}

	return ProfileConfig{}, fmt.Errorf("error parseando config: %w", err)
}

// applyDefaults aplica valores por defecto a una configuración si los campos están vacíos o en cero
func applyDefaults(cfg, defaults ProfileConfig) ProfileConfig {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = defaults.ListenAddr
	}
	if cfg.BackendAddr == "" {
		cfg.BackendAddr = defaults.BackendAddr
	}
	if cfg.MaxLiveConnsPerIP == 0 {
		cfg.MaxLiveConnsPerIP = defaults.MaxLiveConnsPerIP
	}
	if cfg.AttemptRefillPerSec == 0 {
		cfg.AttemptRefillPerSec = defaults.AttemptRefillPerSec
	}
	if cfg.AttemptBurst == 0 {
		cfg.AttemptBurst = defaults.AttemptBurst
	}
	if cfg.DeniesBeforeTempBlock == 0 {
		cfg.DeniesBeforeTempBlock = defaults.DeniesBeforeTempBlock
	}
	if cfg.TempBlockSeconds == 0 {
		cfg.TempBlockSeconds = defaults.TempBlockSeconds
	}
	if cfg.MaxTotalConns == 0 {
		cfg.MaxTotalConns = defaults.MaxTotalConns
	}
	if cfg.IdleTimeoutSeconds == 0 {
		cfg.IdleTimeoutSeconds = defaults.IdleTimeoutSeconds
	}
	if cfg.StaleAfterSeconds == 0 {
		cfg.StaleAfterSeconds = defaults.StaleAfterSeconds
	}
	if cfg.CleanupEverySeconds == 0 {
		cfg.CleanupEverySeconds = defaults.CleanupEverySeconds
	}
	if cfg.FirewallBlockSeconds == 0 {
		cfg.FirewallBlockSeconds = defaults.FirewallBlockSeconds
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = defaults.LogLevel
	}
	if cfg.BackendDialTimeoutSeconds == 0 {
		cfg.BackendDialTimeoutSeconds = defaults.BackendDialTimeoutSeconds
	}
	// MaxDrainSeconds: 0 es válido para game (sin límite), aplicar solo si el default no es 0
	if cfg.MaxDrainSeconds == 0 && defaults.MaxDrainSeconds != 0 {
		cfg.MaxDrainSeconds = defaults.MaxDrainSeconds
	}
	// LogFile puede estar vacío, no aplicar default
	return cfg
}
