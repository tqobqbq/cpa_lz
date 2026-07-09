package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type authFilesGroupRequest struct {
	Enabled  *bool   `json:"enabled"`
	Priority *int    `json:"priority"`
	ProxyURL *string `json:"proxy-url"`
}

func authFilesGroupPayload(cfg *config.Config) gin.H {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return gin.H{
		"enabled":   config.EffectiveBool(cfg.AuthFilesGroup.Enabled, true),
		"priority":  config.EffectivePriority(cfg.AuthFilesGroup.Priority),
		"proxy-url": strings.TrimSpace(cfg.AuthFilesGroup.ProxyURL),
	}
}

// GetAuthFilesGroup returns the group-level Auth Files settings.
func (h *Handler) GetAuthFilesGroup(c *gin.Context) {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	c.JSON(http.StatusOK, authFilesGroupPayload(cfg))
}

// PutAuthFilesGroup replaces the group-level Auth Files settings.
func (h *Handler) PutAuthFilesGroup(c *gin.Context) {
	h.updateAuthFilesGroup(c, true)
}

// PatchAuthFilesGroup partially updates the group-level Auth Files settings.
func (h *Handler) PatchAuthFilesGroup(c *gin.Context) {
	h.updateAuthFilesGroup(c, false)
}

func (h *Handler) updateAuthFilesGroup(c *gin.Context, replace bool) {
	var req authFilesGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.cfg == nil {
		h.cfg = &config.Config{}
	}

	if replace || req.Enabled != nil {
		enabled := config.EffectiveBool(req.Enabled, true)
		h.cfg.AuthFilesGroup.Enabled = config.DefaultBoolPtr(enabled)
	}
	if replace || req.Priority != nil {
		priority := config.EffectivePriority(req.Priority)
		if priority < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "priority must be non-negative"})
			return
		}
		h.cfg.AuthFilesGroup.Priority = config.DefaultIntPtr(priority)
	}
	if replace || req.ProxyURL != nil {
		proxyURL := ""
		if req.ProxyURL != nil {
			proxyURL = strings.TrimSpace(*req.ProxyURL)
		}
		h.cfg.AuthFilesGroup.ProxyURL = proxyURL
	}

	h.persistLocked(c)
}
