package managementasset

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const managementAssetName = "management.html"

// ManagementFileName exposes the control panel asset filename.
const ManagementFileName = managementAssetName

var currentConfigPtr atomic.Pointer[config.Config]

// SetCurrentConfig stores the latest configuration snapshot for management asset decisions.
func SetCurrentConfig(cfg *config.Config) {
	if cfg == nil {
		currentConfigPtr.Store(nil)
		return
	}
	currentConfigPtr.Store(cfg)
}

// StartAutoUpdater is kept as a compatibility no-op now that the panel ships embedded.
func StartAutoUpdater(ctx context.Context, configFilePath string) {
	_ = ctx
	_ = configFilePath
}

// StaticDir resolves the legacy directory used to mirror the embedded control panel asset.
func StaticDir(configFilePath string) string {
	if override := strings.TrimSpace(os.Getenv("MANAGEMENT_STATIC_PATH")); override != "" {
		cleaned := filepath.Clean(override)
		if strings.EqualFold(filepath.Base(cleaned), managementAssetName) {
			return filepath.Dir(cleaned)
		}
		return cleaned
	}

	if writable := util.WritablePath(); writable != "" {
		return filepath.Join(writable, "static")
	}

	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath == "" {
		return ""
	}

	base := filepath.Dir(configFilePath)
	if fileInfo, err := os.Stat(configFilePath); err == nil && fileInfo.IsDir() {
		base = configFilePath
	}

	return filepath.Join(base, "static")
}

// FilePath resolves the absolute path to the legacy mirrored management asset.
func FilePath(configFilePath string) string {
	if override := strings.TrimSpace(os.Getenv("MANAGEMENT_STATIC_PATH")); override != "" {
		cleaned := filepath.Clean(override)
		if strings.EqualFold(filepath.Base(cleaned), managementAssetName) {
			return cleaned
		}
		return filepath.Join(cleaned, ManagementFileName)
	}

	dir := StaticDir(configFilePath)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, ManagementFileName)
}

// EnsureLatestManagementHTML mirrors the embedded panel to disk for legacy callers.
func EnsureLatestManagementHTML(ctx context.Context, staticDir string, proxyURL string, panelRepository string) bool {
	_ = ctx
	_ = proxyURL
	_ = panelRepository

	data, err := EmbeddedHTML()
	if err != nil {
		log.WithError(err).Debug("embedded management asset unavailable")
		return false
	}
	staticDir = strings.TrimSpace(staticDir)
	if staticDir == "" {
		return true
	}
	if errMkdir := os.MkdirAll(staticDir, 0o755); errMkdir != nil {
		log.WithError(errMkdir).Warn("failed to create static directory for embedded management asset")
		return false
	}
	path := filepath.Join(staticDir, ManagementFileName)
	if existing, errRead := os.ReadFile(path); errRead == nil && bytes.Equal(existing, data) {
		return true
	}
	if errWrite := os.WriteFile(path, data, 0o644); errWrite != nil {
		log.WithError(errWrite).Warn("failed to write embedded management asset mirror")
		return false
	}
	return true
}
