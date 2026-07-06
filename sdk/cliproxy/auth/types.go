package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	baseauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth"
)

// PostAuthHook defines a function that is called after an Auth record is created
// but before it is persisted to storage. This allows for modification of the
// Auth record (e.g., injecting metadata) based on external context.
type PostAuthHook func(context.Context, *Auth) error

// RequestInfo holds information extracted from the HTTP request.
// It is injected into the context passed to PostAuthHook.
type RequestInfo struct {
	Query   url.Values
	Headers http.Header
}

type requestInfoKey struct{}

// WithRequestInfo returns a new context with the given RequestInfo attached.
func WithRequestInfo(ctx context.Context, info *RequestInfo) context.Context {
	return context.WithValue(ctx, requestInfoKey{}, info)
}

// GetRequestInfo retrieves the RequestInfo from the context, if present.
func GetRequestInfo(ctx context.Context) *RequestInfo {
	if val, ok := ctx.Value(requestInfoKey{}).(*RequestInfo); ok {
		return val
	}
	return nil
}

// Auth encapsulates the runtime state and metadata associated with a single credential.
type Auth struct {
	// ID uniquely identifies the auth record across restarts.
	ID string `json:"id"`
	// Index is a stable runtime identifier derived from auth metadata (not persisted).
	Index string `json:"-"`
	// Provider is the upstream provider key (e.g. "gemini", "claude").
	Provider string `json:"provider"`
	// Prefix optionally namespaces models for routing (e.g., "teamA/gemini-3-pro-preview").
	Prefix string `json:"prefix,omitempty"`
	// FileName stores the relative or absolute path of the backing auth file.
	FileName string `json:"-"`
	// Storage holds the token persistence implementation used during login flows.
	Storage baseauth.TokenStorage `json:"-"`
	// Label is an optional human readable label for logging.
	Label string `json:"label,omitempty"`
	// Status is the lifecycle status managed by the AuthManager.
	Status Status `json:"status"`
	// StatusMessage holds a short description for the current status.
	StatusMessage string `json:"status_message,omitempty"`
	// Disabled indicates the auth is intentionally disabled by operator.
	Disabled bool `json:"disabled"`
	// Unavailable flags transient provider unavailability (e.g. quota exceeded).
	Unavailable bool `json:"unavailable"`
	// ProxyURL overrides the global proxy setting for this auth if provided.
	ProxyURL string `json:"proxy_url,omitempty"`
	// Attributes stores provider specific metadata needed by executors (immutable configuration).
	Attributes map[string]string `json:"attributes,omitempty"`
	// Metadata stores runtime mutable provider state (e.g. tokens, cookies).
	Metadata map[string]any `json:"metadata,omitempty"`
	// Quota captures recent quota information for load balancers.
	Quota QuotaState `json:"quota"`
	// LastError stores the last failure encountered while executing or refreshing.
	LastError *Error `json:"last_error,omitempty"`
	// CreatedAt is the creation timestamp in UTC.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the last modification timestamp in UTC.
	UpdatedAt time.Time `json:"updated_at"`
	// LastRefreshedAt records the last successful refresh time in UTC.
	LastRefreshedAt time.Time `json:"last_refreshed_at"`
	// NextRefreshAfter is the earliest time a refresh should retrigger.
	NextRefreshAfter time.Time `json:"next_refresh_after"`
	// NextRetryAfter is the earliest time a retry should retrigger.
	NextRetryAfter time.Time `json:"next_retry_after"`
	// ModelStates tracks per-model runtime availability data.
	ModelStates map[string]*ModelState `json:"model_states,omitempty"`

	// Runtime carries non-serialisable data used during execution (in-memory only).
	Runtime any `json:"-"`

	// RuntimeGeneration invalidates stale in-flight execution results after operator resets.
	RuntimeGeneration uint64 `json:"-"`

	indexAssigned bool `json:"-"`
}

// QuotaState contains limiter tracking data for a credential.
type QuotaState struct {
	// Exceeded indicates the credential recently hit a quota error.
	Exceeded bool `json:"exceeded"`
	// Reason provides an optional provider specific human readable description.
	Reason string `json:"reason,omitempty"`
	// NextRecoverAt is when the credential may become available again.
	NextRecoverAt time.Time `json:"next_recover_at"`
	// BackoffLevel stores the progressive cooldown exponent used for rate limits.
	BackoffLevel int `json:"backoff_level,omitempty"`
}

// ModelState captures the execution state for a specific model under an auth entry.
type ModelState struct {
	// Status reflects the lifecycle status for this model.
	Status Status `json:"status"`
	// StatusMessage provides an optional short description of the status.
	StatusMessage string `json:"status_message,omitempty"`
	// Unavailable mirrors whether the model is temporarily blocked for retries.
	Unavailable bool `json:"unavailable"`
	// NextRetryAfter defines the per-model retry time.
	NextRetryAfter time.Time `json:"next_retry_after"`
	// LastError records the latest error observed for this model.
	LastError *Error `json:"last_error,omitempty"`
	// Quota retains quota information if this model hit rate limits.
	Quota QuotaState `json:"quota"`
	// UpdatedAt tracks the last update timestamp for this model state.
	UpdatedAt time.Time `json:"updated_at"`
}

// DisabledFromMetadata reports whether an auth JSON payload marks the credential disabled.
func DisabledFromMetadata(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	raw, ok := metadata["disabled"]
	if !ok || raw == nil {
		return false
	}
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		return err == nil && parsed
	case float64:
		return value != 0
	case int:
		return value != 0
	case int64:
		return value != 0
	case json.Number:
		parsed, err := strconv.ParseBool(strings.TrimSpace(value.String()))
		if err == nil {
			return parsed
		}
		number, errInt := value.Int64()
		return errInt == nil && number != 0
	default:
		return false
	}
}

// SyncDisabledMetadata mirrors the operator-disabled flag into auth.Metadata before persistence.
func SyncDisabledMetadata(auth *Auth) {
	if auth == nil || auth.Metadata == nil {
		return
	}
	auth.Metadata["disabled"] = auth.Disabled || auth.Status == StatusDisabled
}

type priorityTier struct {
	group int
	entry int
}

func (t priorityTier) betterThan(other priorityTier) bool {
	if t.group != other.group {
		return t.group > other.group
	}
	return t.entry > other.entry
}

// Clone shallow copies the Auth structure, duplicating maps to avoid accidental mutation.
func (a *Auth) Clone() *Auth {
	if a == nil {
		return nil
	}
	copyAuth := *a
	if len(a.Attributes) > 0 {
		copyAuth.Attributes = make(map[string]string, len(a.Attributes))
		for key, value := range a.Attributes {
			copyAuth.Attributes[key] = value
		}
	}
	if len(a.Metadata) > 0 {
		copyAuth.Metadata = make(map[string]any, len(a.Metadata))
		for key, value := range a.Metadata {
			copyAuth.Metadata[key] = value
		}
	}
	if len(a.ModelStates) > 0 {
		copyAuth.ModelStates = make(map[string]*ModelState, len(a.ModelStates))
		for key, state := range a.ModelStates {
			copyAuth.ModelStates[key] = state.Clone()
		}
	}
	copyAuth.Runtime = a.Runtime
	return &copyAuth
}

func stableAuthIndex(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:8])
}

func (a *Auth) indexSeed() string {
	if a == nil {
		return ""
	}

	if fileName := strings.TrimSpace(a.FileName); fileName != "" {
		return "file:" + fileName
	}

	providerKey := strings.ToLower(strings.TrimSpace(a.Provider))
	compatName := ""
	baseURL := ""
	apiKey := ""
	source := ""
	if a.Attributes != nil {
		if value := strings.TrimSpace(a.Attributes["provider_key"]); value != "" {
			providerKey = strings.ToLower(value)
		}
		compatName = strings.ToLower(strings.TrimSpace(a.Attributes["compat_name"]))
		baseURL = strings.TrimSpace(a.Attributes["base_url"])
		apiKey = strings.TrimSpace(a.Attributes["api_key"])
		source = strings.TrimSpace(a.Attributes["source"])
	}

	proxyURL := strings.TrimSpace(a.ProxyURL)
	hasCredentialIdentity := compatName != "" || baseURL != "" || proxyURL != "" || apiKey != "" || source != ""
	if providerKey != "" && hasCredentialIdentity {
		parts := []string{"provider=" + providerKey}
		if compatName != "" {
			parts = append(parts, "compat="+compatName)
		}
		if baseURL != "" {
			parts = append(parts, "base="+baseURL)
		}
		if proxyURL != "" {
			parts = append(parts, "proxy="+proxyURL)
		}
		if apiKey != "" {
			parts = append(parts, "api_key="+apiKey)
		}
		if source != "" {
			parts = append(parts, "source="+source)
		}
		return "config:" + strings.Join(parts, "\x00")
	}

	if id := strings.TrimSpace(a.ID); id != "" {
		return "id:" + id
	}

	return ""
}

// EnsureIndex returns a stable index derived from the auth file name or credential identity.
func (a *Auth) EnsureIndex() string {
	if a == nil {
		return ""
	}
	if a.indexAssigned && a.Index != "" {
		return a.Index
	}

	seed := a.indexSeed()
	if seed == "" {
		return ""
	}

	idx := stableAuthIndex(seed)
	a.Index = idx
	a.indexAssigned = true
	return idx
}

// Clone duplicates a model state including nested error details.
func (m *ModelState) Clone() *ModelState {
	if m == nil {
		return nil
	}
	copyState := *m
	if m.LastError != nil {
		copyState.LastError = &Error{
			Code:       m.LastError.Code,
			Message:    m.LastError.Message,
			Retryable:  m.LastError.Retryable,
			HTTPStatus: m.LastError.HTTPStatus,
		}
	}
	return &copyState
}

func (a *Auth) ProxyInfo() string {
	if a == nil {
		return ""
	}
	proxyStr := strings.TrimSpace(a.ProxyURL)
	if proxyStr == "" {
		return ""
	}
	if idx := strings.Index(proxyStr, "://"); idx > 0 {
		return "via " + proxyStr[:idx] + " proxy"
	}
	return "via proxy"
}

// GroupEnabled reports whether the auth's group participates in routing.
func (a *Auth) GroupEnabled() bool {
	if a == nil || a.Attributes == nil {
		return true
	}
	raw := strings.TrimSpace(a.Attributes["group_enabled"])
	if raw == "" {
		return true
	}
	parsed, ok := parseBoolAny(raw)
	if !ok {
		return true
	}
	return parsed
}

// GroupPriority returns the global group priority for this auth.
func (a *Auth) GroupPriority() int {
	if a == nil || a.Attributes == nil {
		return 10
	}
	raw := strings.TrimSpace(a.Attributes["group_priority"])
	if raw == "" {
		return 10
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 10
	}
	return parsed
}

// BackoffModeOverride returns the normalized per-auth backoff mode override.
func (a *Auth) BackoffModeOverride() string {
	if a == nil || a.Metadata == nil {
		return ""
	}
	for _, key := range []string{"backoff_mode", "backoff-mode"} {
		raw, ok := a.Metadata[key]
		if !ok {
			continue
		}
		value, okString := raw.(string)
		if !okString {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "off":
			return "off"
		case "custom":
			return "custom"
		case "default":
			return "default"
		default:
			return "default"
		}
	}
	return ""
}

// DisableCoolingOverride returns the auth-file scoped disable_cooling override when present.
// The value is read from metadata key "disable_cooling" (or legacy "disable-cooling").
func (a *Auth) DisableCoolingOverride() (bool, bool) {
	if mode := a.BackoffModeOverride(); mode == "off" {
		return true, true
	}
	if a == nil || a.Metadata == nil {
		return false, false
	}
	if val, ok := a.Metadata["disable_cooling"]; ok {
		if parsed, okParse := parseBoolAny(val); okParse {
			return parsed, true
		}
	}
	if val, ok := a.Metadata["disable-cooling"]; ok {
		if parsed, okParse := parseBoolAny(val); okParse {
			return parsed, true
		}
	}
	return false, false
}

// ToolPrefixDisabled returns whether the proxy_ tool name prefix should be
// skipped for this auth. When true, tool names are sent to Anthropic unchanged.
// The value is read from metadata key "tool_prefix_disabled" (or "tool-prefix-disabled").
func (a *Auth) ToolPrefixDisabled() bool {
	if a == nil || a.Metadata == nil {
		return false
	}
	for _, key := range []string{"tool_prefix_disabled", "tool-prefix-disabled"} {
		if val, ok := a.Metadata[key]; ok {
			if parsed, okParse := parseBoolAny(val); okParse {
				return parsed
			}
		}
	}
	return false
}

// RequestRetryOverride returns the auth-file scoped request_retry override when present.
// The value is read from metadata key "request_retry" (or legacy "request-retry").
func (a *Auth) RequestRetryOverride() (int, bool) {
	switch a.BackoffModeOverride() {
	case "off":
		return 0, true
	case "custom":
		if a == nil || a.Metadata == nil {
			return 0, false
		}
		if val, ok := a.Metadata["request_retry"]; ok {
			if parsed, okParse := parseIntAny(val); okParse {
				if parsed < 0 {
					parsed = 0
				}
				return parsed, true
			}
		}
		if val, ok := a.Metadata["request-retry"]; ok {
			if parsed, okParse := parseIntAny(val); okParse {
				if parsed < 0 {
					parsed = 0
				}
				return parsed, true
			}
		}
		return 0, false
	case "default":
		return 0, false
	}
	if a == nil || a.Metadata == nil {
		return 0, false
	}
	if val, ok := a.Metadata["request_retry"]; ok {
		if parsed, okParse := parseIntAny(val); okParse {
			if parsed < 0 {
				parsed = 0
			}
			return parsed, true
		}
	}
	if val, ok := a.Metadata["request-retry"]; ok {
		if parsed, okParse := parseIntAny(val); okParse {
			if parsed < 0 {
				parsed = 0
			}
			return parsed, true
		}
	}
	return 0, false
}

// RetryRoundsOverride returns the auth-scoped whole-request retry rounds override when present.
func (a *Auth) RetryRoundsOverride() (int, bool) {
	return positiveIntMetadataOverride(a, "retry_rounds", "retry-rounds")
}

// RoundBackoffBaseOverride returns the auth-scoped round backoff base override in seconds.
func (a *Auth) RoundBackoffBaseOverride() (float64, bool) {
	return positiveFloatMetadataOverride(a, "round_backoff_base", "round-backoff-base")
}

// RoundBackoffExponentOverride returns the auth-scoped round backoff exponent override.
func (a *Auth) RoundBackoffExponentOverride() (float64, bool) {
	return positiveFloatMetadataOverride(a, "round_backoff_exponent", "round-backoff-exponent")
}

// RoundBackoffMaxOverride returns the auth-scoped round backoff max override in seconds.
func (a *Auth) RoundBackoffMaxOverride() (float64, bool) {
	return positiveFloatMetadataOverride(a, "round_backoff_max", "round-backoff-max")
}

// ProviderCooldownStartOverride returns the auth-scoped failure cooldown start override.
func (a *Auth) ProviderCooldownStartOverride() (int, bool) {
	return positiveIntMetadataOverride(a, "cooldown_start", "cooldown-start")
}

// ProviderCooldownExponentOverride returns the auth-scoped failure cooldown exponent override.
func (a *Auth) ProviderCooldownExponentOverride() (float64, bool) {
	return positiveFloatMetadataOverride(a, "cooldown_exponent", "cooldown-exponent")
}

// ProviderCooldownMaxOverride returns the auth-scoped failure cooldown max override.
func (a *Auth) ProviderCooldownMaxOverride() (int, bool) {
	return positiveIntMetadataOverride(a, "cooldown_max", "cooldown-max")
}

func positiveIntMetadataOverride(a *Auth, keys ...string) (int, bool) {
	if a == nil || a.Metadata == nil {
		return 0, false
	}
	for _, key := range keys {
		if val, ok := a.Metadata[key]; ok {
			if parsed, okParse := parseIntAny(val); okParse {
				if parsed < 1 {
					parsed = 1
				}
				return parsed, true
			}
		}
	}
	return 0, false
}

func nonNegativeIntMetadataOverride(a *Auth, keys ...string) (int, bool) {
	if a == nil || a.Metadata == nil {
		return 0, false
	}
	for _, key := range keys {
		if val, ok := a.Metadata[key]; ok {
			if parsed, okParse := parseIntAny(val); okParse {
				if parsed < 0 {
					parsed = 0
				}
				return parsed, true
			}
		}
	}
	return 0, false
}

func positiveFloatMetadataOverride(a *Auth, keys ...string) (float64, bool) {
	if a == nil || a.Metadata == nil {
		return 0, false
	}
	for _, key := range keys {
		if val, ok := a.Metadata[key]; ok {
			if parsed, okParse := parseFloatAny(val); okParse {
				if parsed <= 0 {
					continue
				}
				return parsed, true
			}
		}
	}
	return 0, false
}

func parseBoolAny(val any) (bool, bool) {
	switch typed := val.(type) {
	case bool:
		return typed, true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return false, false
		}
		parsed, err := strconv.ParseBool(trimmed)
		if err != nil {
			return false, false
		}
		return parsed, true
	case float64:
		return typed != 0, true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return false, false
		}
		return parsed != 0, true
	default:
		return false, false
	}
}

func parseIntAny(val any) (int, bool) {
	switch typed := val.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func parseFloatAny(val any) (float64, bool) {
	switch typed := val.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func (a *Auth) AccountInfo() (string, string) {
	if a == nil {
		return "", ""
	}
	// For Gemini CLI, include project ID in the OAuth account info if present.
	if strings.ToLower(a.Provider) == "gemini-cli" {
		if a.Metadata != nil {
			email, _ := a.Metadata["email"].(string)
			email = strings.TrimSpace(email)
			if email != "" {
				if p, ok := a.Metadata["project_id"].(string); ok {
					p = strings.TrimSpace(p)
					if p != "" {
						return "oauth", email + " (" + p + ")"
					}
				}
				return "oauth", email
			}
		}
	}

	// Check metadata for email first (OAuth-style auth)
	if a.Metadata != nil {
		if v, ok := a.Metadata["email"].(string); ok {
			email := strings.TrimSpace(v)
			if email != "" {
				return "oauth", email
			}
		}
	}
	// Fall back to API key (API-key auth)
	if a.Attributes != nil {
		if v := a.Attributes["api_key"]; v != "" {
			return "api_key", v
		}
	}
	return "", ""
}

// ExpirationTime attempts to extract the credential expiration timestamp from metadata.
// It inspects common keys such as "expired", "expire", "expires_at", and also
// nested "token" objects to remain compatible with legacy auth file formats.
func (a *Auth) ExpirationTime() (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	if ts, ok := expirationFromMap(a.Metadata); ok {
		return ts, true
	}
	return time.Time{}, false
}

var (
	refreshLeadMu        sync.RWMutex
	refreshLeadFactories = make(map[string]func() *time.Duration)
)

func RegisterRefreshLeadProvider(provider string, factory func() *time.Duration) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || factory == nil {
		return
	}
	refreshLeadMu.Lock()
	refreshLeadFactories[provider] = factory
	refreshLeadMu.Unlock()
}

var expireKeys = [...]string{"expired", "expire", "expires_at", "expiresAt", "expiry", "expires"}

func expirationFromMap(meta map[string]any) (time.Time, bool) {
	if meta == nil {
		return time.Time{}, false
	}
	for _, key := range expireKeys {
		if v, ok := meta[key]; ok {
			if ts, ok1 := parseTimeValue(v); ok1 {
				return ts, true
			}
		}
	}
	for _, nestedKey := range []string{"token", "Token"} {
		if nested, ok := meta[nestedKey]; ok {
			switch val := nested.(type) {
			case map[string]any:
				if ts, ok1 := expirationFromMap(val); ok1 {
					return ts, true
				}
			case map[string]string:
				temp := make(map[string]any, len(val))
				for k, v := range val {
					temp[k] = v
				}
				if ts, ok1 := expirationFromMap(temp); ok1 {
					return ts, true
				}
			}
		}
	}
	return time.Time{}, false
}

func ProviderRefreshLead(provider string, runtime any) *time.Duration {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if runtime != nil {
		if eval, ok := runtime.(interface{ RefreshLead() *time.Duration }); ok {
			if lead := eval.RefreshLead(); lead != nil && *lead > 0 {
				return lead
			}
		}
	}
	refreshLeadMu.RLock()
	factory := refreshLeadFactories[provider]
	refreshLeadMu.RUnlock()
	if factory == nil {
		return nil
	}
	if lead := factory(); lead != nil && *lead > 0 {
		return lead
	}
	return nil
}

func parseTimeValue(v any) (time.Time, bool) {
	switch value := v.(type) {
	case string:
		s := strings.TrimSpace(value)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{
			time.RFC3339,
			time.RFC3339Nano,
			"2006-01-02 15:04:05",
			"2006-01-02 15:04",
			"2006-01-02T15:04:05Z07:00",
		}
		for _, layout := range layouts {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts, true
			}
		}
		if unix, err := strconv.ParseInt(s, 10, 64); err == nil {
			return normaliseUnix(unix), true
		}
	case float64:
		return normaliseUnix(int64(value)), true
	case int64:
		return normaliseUnix(value), true
	case json.Number:
		if i, err := value.Int64(); err == nil {
			return normaliseUnix(i), true
		}
		if f, err := value.Float64(); err == nil {
			return normaliseUnix(int64(f)), true
		}
	}
	return time.Time{}, false
}

func normaliseUnix(raw int64) time.Time {
	if raw <= 0 {
		return time.Time{}
	}
	// Heuristic: treat values with millisecond precision (>1e12) accordingly.
	if raw > 1_000_000_000_000 {
		return time.UnixMilli(raw)
	}
	return time.Unix(raw, 0)
}
