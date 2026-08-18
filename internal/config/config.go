// Package config loads panel configuration from environment variables,
// mirroring the Python config.py defaults exactly.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	BaseDir       string
	InstanceDir   string
	SecretKey     []byte
	DatabaseURL   string
	ServersRoot   string
	BackupsRoot   string
	AllowedImages []string
	MaxUploadMB   int
	MaxEditMB     int
	ForceSSL      bool
}

// Load builds a Config rooted at baseDir (the directory containing the binary's
// working data — normally the current working directory, same as Flask's app.py).
func Load(baseDir string) (*Config, error) {
	instanceDir := filepath.Join(baseDir, "instance")
	if err := os.MkdirAll(instanceDir, 0o755); err != nil {
		return nil, err
	}

	secretKey, err := loadOrCreateSecretKey(instanceDir)
	if err != nil {
		return nil, err
	}

	dbURL := getenv("PANEL_DATABASE_URL", filepath.Join(instanceDir, "panel.db"))
	serversRoot := getenv("PANEL_SERVERS_ROOT", filepath.Join(baseDir, "data", "servers"))
	backupsRoot := getenv("PANEL_BACKUPS_ROOT", filepath.Join(baseDir, "data", "backups"))

	rawImages := getenv("PANEL_ALLOWED_IMAGES", "itzg/minecraft-server,itzg/minecraft-bedrock-server,itzg/mc-proxy")
	var allowed []string
	for _, img := range strings.Split(rawImages, ",") {
		img = strings.TrimSpace(img)
		if img != "" {
			allowed = append(allowed, img)
		}
	}

	maxUpload, _ := strconv.Atoi(getenv("PANEL_MAX_UPLOAD_MB", "250"))
	if maxUpload == 0 {
		maxUpload = 250
	}
	maxEdit, _ := strconv.Atoi(getenv("PANEL_MAX_EDIT_MB", "2"))
	if maxEdit == 0 {
		maxEdit = 2
	}

	return &Config{
		BaseDir:       baseDir,
		InstanceDir:   instanceDir,
		SecretKey:     secretKey,
		DatabaseURL:   dbURL,
		ServersRoot:   serversRoot,
		BackupsRoot:   backupsRoot,
		AllowedImages: allowed,
		MaxUploadMB:   maxUpload,
		MaxEditMB:     maxEdit,
		ForceSSL:      getenv("PANEL_FORCE_SSL", "0") == "1",
	}, nil
}

func loadOrCreateSecretKey(instanceDir string) ([]byte, error) {
	if envKey := os.Getenv("PANEL_SECRET_KEY"); envKey != "" {
		return []byte(envKey), nil
	}
	keyFile := filepath.Join(instanceDir, "secret_key")
	if data, err := os.ReadFile(keyFile); err == nil {
		return []byte(strings.TrimSpace(string(data))), nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	key := hex.EncodeToString(raw)
	if err := os.WriteFile(keyFile, []byte(key), 0o600); err != nil {
		return nil, err
	}
	return []byte(key), nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
