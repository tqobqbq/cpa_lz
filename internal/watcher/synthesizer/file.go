package synthesizer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/geminicli"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// FileSynthesizer generates Auth entries from auth files in the auth directory.
type FileSynthesizer struct{}

// NewFileSynthesizer creates a new FileSynthesizer instance.
func NewFileSynthesizer() *FileSynthesizer { return &FileSynthesizer{} }

// Synthesize generates Auth entries from auth files found in the auth directory.
func (s *FileSynthesizer) Synthesize(ctx *SynthesisContext) ([]*coreauth.Auth, error) {
	if ctx == nil || strings.TrimSpace(ctx.AuthDir) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(ctx.AuthDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	out := make([]*coreauth.Auth, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fullPath := filepath.Join(ctx.AuthDir, entry.Name())
		data, err := os.ReadFile(fullPath)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			continue
		}
		auths := synthesizeFileAuths(ctx, fullPath, data)
		if len(auths) == 0 {
			continue
		}
		out = append(out, auths...)
	}
	return out, nil
}

// SynthesizeAuthFile generates Auth entries for one auth JSON file payload.
// It shares the same mapping behavior as FileSynthesizer.Synthesize.
func SynthesizeAuthFile(ctx *SynthesisContext, fullPath string, data []byte) []*coreauth.Auth {
	return synthesizeFileAuths(ctx, fullPath, data)
}

func synthesizeFileAuths(ctx *SynthesisContext, fullPath string, data []byte) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil
	}

	rawType, _ := metadata["type"].(string)
	provider := strings.ToLower(strings.TrimSpace(rawType))
	if provider == "" {
		return nil
	}
	if provider == "gemini" {
		provider = "gemini-cli"
	}

	pathBase := filepath.Base(fullPath)
	id := pathBase
	if provider == "claude" || provider == "codex" || provider == "gemini-cli" || provider == "vertex" {
		id = pathBase
	} else if idGen != nil {
		generated, _ := idGen.Next(provider+":file", fullPath)
		id = generated
	}

	label := strings.TrimSpace(stringValue(metadata["email"]))
	if label == "" {
		label = strings.TrimSpace(stringValue(metadata["label"]))
	}
	if label == "" {
		label = provider
	}

	proxyURL := ""
	if p, ok := metadata["proxy_url"].(string); ok {
		proxyURL = p
	}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.AuthFilesGroup.ProxyURL)
	}

	prefix := ""
	if rawPrefix, ok := metadata["prefix"].(string); ok {
		trimmed := strings.Trim(strings.TrimSpace(rawPrefix), "/")
		if !strings.Contains(trimmed, "/") {
			prefix = trimmed
		}
	}

	disabled := coreauth.DisabledFromMetadata(metadata)
	status := coreauth.StatusActive
	if disabled {
		status = coreauth.StatusDisabled
	}

	a := &coreauth.Auth{
		ID:         id,
		FileName:   pathBase,
		Provider:   provider,
		Label:      label,
		Prefix:     prefix,
		ProxyURL:   proxyURL,
		Status:     status,
		Disabled:   disabled,
		Attributes: map[string]string{"path": fullPath, "source": "file:" + provider},
		Metadata:   cloneMetadataMap(metadata),
		CreatedAt:  metadataCreatedAt(metadata, now),
		UpdatedAt:  metadataUpdatedAt(metadata, now),
	}

	if rawPriority, ok := metadata["priority"]; ok {
		switch v := rawPriority.(type) {
		case float64:
			a.Attributes["priority"] = strconv.Itoa(int(v))
		case string:
			priority := strings.TrimSpace(v)
			if _, errAtoi := strconv.Atoi(priority); errAtoi == nil {
				a.Attributes["priority"] = priority
			}
		}
	}
	if _, ok := a.Attributes["priority"]; !ok {
		a.Attributes["priority"] = strconv.Itoa(config.DefaultRoutingPriority)
	}
	groupPriority := config.DefaultRoutingPriority
	groupEnabled := true
	if cfg != nil {
		groupPriority = config.EffectivePriority(cfg.AuthFilesGroup.Priority)
		groupEnabled = config.EffectiveBool(cfg.AuthFilesGroup.Enabled, true)
	}
	a.Attributes["group_priority"] = strconv.Itoa(groupPriority)
	a.Attributes["group_enabled"] = strconv.FormatBool(groupEnabled)
	if rawMode, ok := metadata["backoff_mode"]; ok {
		if mode, okString := rawMode.(string); okString {
			a.Metadata["backoff_mode"] = normalizeBackoffMode(mode)
		}
	} else if rawMode, ok := metadata["backoff-mode"]; ok {
		if mode, okString := rawMode.(string); okString {
			a.Metadata["backoff_mode"] = normalizeBackoffMode(mode)
		}
	}
	if rawRetry, ok := metadata["request_retry"]; ok {
		a.Metadata["request_retry"] = rawRetry
	} else if rawRetry, ok := metadata["request-retry"]; ok {
		a.Metadata["request_retry"] = rawRetry
	}
	if rawNote, ok := metadata["note"]; ok {
		if note, isStr := rawNote.(string); isStr {
			if trimmed := strings.TrimSpace(note); trimmed != "" {
				a.Attributes["note"] = trimmed
			}
		}
	}
	coreauth.ApplyCustomHeadersFromMetadata(a)
	perAccountExcluded := extractExcludedModelsFromMetadata(metadata)
	ApplyAuthExcludedModelsMeta(a, cfg, perAccountExcluded, "oauth")
	if provider == "codex" {
		if idTokenRaw, ok := metadata["id_token"].(string); ok && strings.TrimSpace(idTokenRaw) != "" {
			if claims, errParse := codex.ParseJWTToken(idTokenRaw); errParse == nil && claims != nil {
				if pt := strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType); pt != "" {
					a.Attributes["plan_type"] = pt
				}
			}
		}
	}
	if provider == "gemini-cli" {
		if virtuals := SynthesizeGeminiVirtualAuths(a, metadata, now); len(virtuals) > 0 {
			for _, v := range virtuals {
				ApplyAuthExcludedModelsMeta(v, cfg, perAccountExcluded, "oauth")
			}
			out := make([]*coreauth.Auth, 0, 1+len(virtuals))
			out = append(out, a)
			out = append(out, virtuals...)
			return out
		}
	}
	return []*coreauth.Auth{a}
}

// SynthesizeGeminiVirtualAuths creates virtual Auth entries for multi-project Gemini credentials.
// It disables the primary auth and creates one virtual auth per project.
func SynthesizeGeminiVirtualAuths(primary *coreauth.Auth, metadata map[string]any, now time.Time) []*coreauth.Auth {
	if primary == nil || metadata == nil {
		return nil
	}
	projects := splitGeminiProjectIDs(metadata)
	if len(projects) <= 1 {
		return nil
	}
	email, _ := metadata["email"].(string)
	shared := geminicli.NewSharedCredential(primary.ID, email, metadata, projects)
	primary.Disabled = true
	primary.Status = coreauth.StatusDisabled
	primary.Runtime = shared
	if primary.Attributes == nil {
		primary.Attributes = make(map[string]string)
	}
	primary.Attributes["gemini_virtual_primary"] = "true"
	primary.Attributes["virtual_children"] = strings.Join(projects, ",")
	source := primary.Attributes["source"]
	authPath := primary.Attributes["path"]
	originalProvider := primary.Provider
	if originalProvider == "" {
		originalProvider = "gemini-cli"
	}
	label := primary.Label
	if label == "" {
		label = originalProvider
	}
	virtuals := make([]*coreauth.Auth, 0, len(projects))
	for _, projectID := range projects {
		attrs := map[string]string{
			"runtime_only":           "true",
			"gemini_virtual_parent":  primary.ID,
			"gemini_virtual_project": projectID,
		}
		if source != "" {
			attrs["source"] = source
		}
		if authPath != "" {
			attrs["path"] = authPath
		}
		if priorityVal, hasPriority := primary.Attributes["priority"]; hasPriority && priorityVal != "" {
			attrs["priority"] = priorityVal
		}
		if groupPriorityVal, hasGroupPriority := primary.Attributes["group_priority"]; hasGroupPriority && groupPriorityVal != "" {
			attrs["group_priority"] = groupPriorityVal
		}
		if groupEnabledVal, hasGroupEnabled := primary.Attributes["group_enabled"]; hasGroupEnabled && groupEnabledVal != "" {
			attrs["group_enabled"] = groupEnabledVal
		}
		if noteVal, hasNote := primary.Attributes["note"]; hasNote {
			if trimmed := strings.TrimSpace(noteVal); trimmed != "" {
				attrs["note"] = trimmed
			}
		}
		for k, v := range primary.Attributes {
			if strings.HasPrefix(k, "header:") && strings.TrimSpace(v) != "" {
				attrs[k] = v
			}
		}
		metadataCopy := map[string]any{
			"email":             email,
			"project_id":        projectID,
			"virtual":           true,
			"virtual_parent_id": primary.ID,
			"type":              metadata["type"],
		}
		if v, ok := metadata["disable_cooling"]; ok {
			metadataCopy["disable_cooling"] = v
		} else if v, ok := metadata["disable-cooling"]; ok {
			metadataCopy["disable_cooling"] = v
		}
		if v, ok := metadata["request_retry"]; ok {
			metadataCopy["request_retry"] = v
		} else if v, ok := metadata["request-retry"]; ok {
			metadataCopy["request_retry"] = v
		}
		if v, ok := metadata["backoff_mode"]; ok {
			if s, okString := v.(string); okString {
				metadataCopy["backoff_mode"] = normalizeBackoffMode(s)
			}
		} else if v, ok := metadata["backoff-mode"]; ok {
			if s, okString := v.(string); okString {
				metadataCopy["backoff_mode"] = normalizeBackoffMode(s)
			}
		}
		proxy := strings.TrimSpace(primary.ProxyURL)
		if proxy != "" {
			metadataCopy["proxy_url"] = proxy
		}
		virtual := &coreauth.Auth{
			ID:         buildGeminiVirtualID(primary.ID, projectID),
			Provider:   originalProvider,
			Label:      fmt.Sprintf("%s [%s]", label, projectID),
			Status:     coreauth.StatusActive,
			Attributes: attrs,
			Metadata:   metadataCopy,
			ProxyURL:   primary.ProxyURL,
			Prefix:     primary.Prefix,
			CreatedAt:  primary.CreatedAt,
			UpdatedAt:  primary.UpdatedAt,
			Runtime:    geminicli.NewVirtualCredential(projectID, shared),
		}
		virtuals = append(virtuals, virtual)
	}
	return virtuals
}

// splitGeminiProjectIDs extracts and deduplicates project IDs from metadata.
func splitGeminiProjectIDs(metadata map[string]any) []string {
	raw, _ := metadata["project_id"].(string)
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

// buildGeminiVirtualID constructs a virtual auth ID from base ID and project ID.
func buildGeminiVirtualID(baseID, projectID string) string {
	project := strings.TrimSpace(projectID)
	if project == "" {
		project = "project"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_")
	return fmt.Sprintf("%s::%s", baseID, replacer.Replace(project))
}

// extractExcludedModelsFromMetadata reads per-account excluded models from the OAuth JSON metadata.
// Supports both "excluded_models" and "excluded-models" keys, and accepts both []string and []interface{}.
func extractExcludedModelsFromMetadata(metadata map[string]any) []string {
	if metadata == nil {
		return nil
	}
	var raw any
	var ok bool
	if raw, ok = metadata["excluded_models"]; !ok {
		raw, ok = metadata["excluded-models"]
	}
	if !ok || raw == nil {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	add := func(s string) {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			return
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	switch v := raw.(type) {
	case []string:
		for _, item := range v {
			add(item)
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	}
	return out
}

func stringValue(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func intValue(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(n))
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func boolValue(v any) (bool, bool) {
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(b))
		if err == nil {
			return parsed, true
		}
	}
	return false, false
}

func durationValue(v any) (time.Duration, bool) {
	if n, ok := intValue(v); ok {
		return time.Duration(n) * time.Second, true
	}
	return 0, false
}

func authFileType(fullPath string, metadata map[string]any) string {
	if t, ok := metadata["type"].(string); ok {
		return strings.ToLower(strings.TrimSpace(t))
	}
	name := strings.ToLower(filepath.Base(fullPath))
	for _, provider := range []string{"claude", "codex", "gemini", "vertex", "aistudio", "antigravity", "kimi", "qwen", "iflow"} {
		if strings.Contains(name, provider) {
			return provider
		}
	}
	return ""
}

func metadataTimestamp(v any) time.Time {
	s, ok := v.(string)
	if !ok {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func metadataCreatedAt(metadata map[string]any, fallback time.Time) time.Time {
	if metadata == nil {
		return fallback
	}
	for _, key := range []string{"created_at", "createdAt", "created-at"} {
		if ts := metadataTimestamp(metadata[key]); !ts.IsZero() {
			return ts
		}
	}
	return fallback
}

func metadataUpdatedAt(metadata map[string]any, fallback time.Time) time.Time {
	if metadata == nil {
		return fallback
	}
	for _, key := range []string{"updated_at", "updatedAt", "updated-at", "modtime"} {
		if ts := metadataTimestamp(metadata[key]); !ts.IsZero() {
			return ts
		}
	}
	return fallback
}

func normalizeAuthFilePath(authDir, path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed)
	}
	if strings.TrimSpace(authDir) == "" {
		return filepath.Clean(trimmed)
	}
	return filepath.Clean(filepath.Join(authDir, trimmed))
}

func relativeAuthID(authDir, fullPath string) string {
	if strings.TrimSpace(fullPath) == "" {
		return ""
	}
	if strings.TrimSpace(authDir) == "" {
		return filepath.Base(fullPath)
	}
	rel, err := filepath.Rel(authDir, fullPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.Base(fullPath)
	}
	return filepath.ToSlash(rel)
}

func loadMetadataFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, nil
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(trimmed), &metadata); err != nil {
		return nil, err
	}
	return metadata, nil
}

func listAuthFilePaths(authDir string) ([]string, error) {
	if strings.TrimSpace(authDir) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(authDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		paths = append(paths, filepath.Join(authDir, entry.Name()))
	}
	return paths, nil
}

func readPriorityAttr(metadata map[string]any) (string, bool) {
	if metadata == nil {
		return "", false
	}
	if rawPriority, ok := metadata["priority"]; ok {
		switch v := rawPriority.(type) {
		case float64:
			return strconv.Itoa(int(v)), true
		case string:
			priority := strings.TrimSpace(v)
			if _, errAtoi := strconv.Atoi(priority); errAtoi == nil {
				return priority, true
			}
		}
	}
	return "", false
}

func applyCommonAuthMetadata(a *coreauth.Auth, cfg *config.Config, metadata map[string]any) {
	if a == nil {
		return
	}
	if a.Attributes == nil {
		a.Attributes = make(map[string]string)
	}
	if a.Metadata == nil {
		a.Metadata = make(map[string]any)
	}

	if priority, ok := readPriorityAttr(metadata); ok {
		a.Attributes["priority"] = priority
	}
	if _, ok := a.Attributes["priority"]; !ok {
		a.Attributes["priority"] = strconv.Itoa(config.DefaultRoutingPriority)
	}

	groupPriority := config.DefaultRoutingPriority
	groupEnabled := true
	if cfg != nil {
		groupPriority = config.EffectivePriority(cfg.AuthFilesGroup.Priority)
		groupEnabled = config.EffectiveBool(cfg.AuthFilesGroup.Enabled, true)
	}
	a.Attributes["group_priority"] = strconv.Itoa(groupPriority)
	a.Attributes["group_enabled"] = strconv.FormatBool(groupEnabled)

	if rawMode, ok := metadata["backoff_mode"]; ok {
		if mode, okString := rawMode.(string); okString {
			a.Metadata["backoff_mode"] = normalizeBackoffMode(mode)
		}
	} else if rawMode, ok := metadata["backoff-mode"]; ok {
		if mode, okString := rawMode.(string); okString {
			a.Metadata["backoff_mode"] = normalizeBackoffMode(mode)
		}
	}
	if rawRetry, ok := metadata["request_retry"]; ok {
		a.Metadata["request_retry"] = rawRetry
	} else if rawRetry, ok := metadata["request-retry"]; ok {
		a.Metadata["request_retry"] = rawRetry
	}
	if rawNote, ok := metadata["note"]; ok {
		if note, isStr := rawNote.(string); isStr {
			if trimmed := strings.TrimSpace(note); trimmed != "" {
				a.Attributes["note"] = trimmed
			}
		}
	}
}

func makeAuthFromMetadata(ctx *SynthesisContext, fullPath string, metadata map[string]any) []*coreauth.Auth {
	if ctx == nil || metadata == nil {
		return nil
	}
	provider := authFileType(fullPath, metadata)
	if provider == "" {
		return nil
	}
	cfg := ctx.Config
	now := ctx.Now
	if now.IsZero() {
		now = time.Now()
	}
	id := relativeAuthID(ctx.AuthDir, fullPath)
	if id == "" {
		id = filepath.Base(fullPath)
	}
	label := stringValue(metadata["email"])
	if label == "" {
		label = stringValue(metadata["label"])
	}
	if label == "" {
		label = provider
	}
	proxyURL := stringValue(metadata["proxy_url"])
	if proxyURL == "" {
		proxyURL = stringValue(metadata["proxy-url"])
	}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.AuthFilesGroup.ProxyURL)
	}
	prefix := ""
	if rawPrefix, ok := metadata["prefix"].(string); ok {
		trimmed := strings.Trim(strings.TrimSpace(rawPrefix), "/")
		if !strings.Contains(trimmed, "/") {
			prefix = trimmed
		}
	}
	attributes := map[string]string{
		"path":   normalizeAuthFilePath(ctx.AuthDir, fullPath),
		"source": "file:" + provider,
	}
	disabled := coreauth.DisabledFromMetadata(metadata)
	status := coreauth.StatusActive
	if disabled {
		status = coreauth.StatusDisabled
	}
	baseAuth := &coreauth.Auth{
		ID:         id,
		FileName:   filepath.Base(fullPath),
		Provider:   provider,
		Label:      label,
		Prefix:     prefix,
		ProxyURL:   proxyURL,
		Status:     status,
		Disabled:   disabled,
		Attributes: attributes,
		Metadata:   map[string]any{},
		CreatedAt:  metadataCreatedAt(metadata, now),
		UpdatedAt:  metadataUpdatedAt(metadata, now),
	}
	applyCommonAuthMetadata(baseAuth, cfg, metadata)
	coreauth.ApplyCustomHeadersFromMetadata(baseAuth)
	perAccountExcluded := extractExcludedModelsFromMetadata(metadata)
	ApplyAuthExcludedModelsMeta(baseAuth, cfg, perAccountExcluded, "oauth")
	if provider == "codex" {
		if idTokenRaw, ok := metadata["id_token"].(string); ok && strings.TrimSpace(idTokenRaw) != "" {
			if claims, errParse := codex.ParseJWTToken(idTokenRaw); errParse == nil && claims != nil {
				if pt := strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType); pt != "" {
					baseAuth.Attributes["plan_type"] = pt
				}
			}
		}
	}
	if provider == "gemini-cli" {
		if virtuals := SynthesizeGeminiVirtualAuths(baseAuth, metadata, now); len(virtuals) > 0 {
			for _, v := range virtuals {
				ApplyAuthExcludedModelsMeta(v, cfg, perAccountExcluded, "oauth")
			}
			out := make([]*coreauth.Auth, 0, 1+len(virtuals))
			out = append(out, baseAuth)
			out = append(out, virtuals...)
			return out
		}
	}
	return []*coreauth.Auth{baseAuth}
}

func cloneMetadataMap(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for k, v := range metadata {
		out[k] = v
	}
	return out
}

func authFileListEntry(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.FileName != "" {
		return auth.FileName
	}
	return auth.ID
}

func authHasMeaningfulPath(auth *coreauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["path"]) != ""
}

func authIsRuntimeOnly(auth *coreauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["runtime_only"]), "true")
}

func authSupportsGroupRouting(auth *coreauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return true
	}
	return !strings.EqualFold(strings.TrimSpace(auth.Attributes["group_enabled"]), "false")
}

func authHasDisplaySource(auth *coreauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["display_source"]) != ""
}

func authConfigIndex(auth *coreauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["config_index"])
}

func authConfigProvider(auth *coreauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["config_provider"])
}

func authDisplaySource(auth *coreauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["display_source"])
}

func authSourceValue(auth *coreauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["source"])
}

func authPathValue(auth *coreauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["path"])
}

func authNoteValue(auth *coreauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["note"])
}

func authPriorityValue(auth *coreauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["priority"])
}

func authGroupPriorityValue(auth *coreauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["group_priority"])
}

func authGroupEnabledValue(auth *coreauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["group_enabled"])
}

func authHeaderValue(auth *coreauth.Auth, key string) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["header:"+key])
}

func authPlanTypeValue(auth *coreauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["plan_type"])
}

func authRuntimeValue(auth *coreauth.Auth) any {
	if auth == nil {
		return nil
	}
	return auth.Runtime
}

func authCreatedAtValue(auth *coreauth.Auth) time.Time {
	if auth == nil {
		return time.Time{}
	}
	return auth.CreatedAt
}

func authUpdatedAtValue(auth *coreauth.Auth) time.Time {
	if auth == nil {
		return time.Time{}
	}
	return auth.UpdatedAt
}

func authMetadataValue(auth *coreauth.Auth, key string) any {
	if auth == nil || auth.Metadata == nil {
		return nil
	}
	return auth.Metadata[key]
}

func authProviderValue(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.Provider
}

func authLabelValue(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.Label
}

func authPrefixValue(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.Prefix
}

func authProxyURLValue(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.ProxyURL
}

func authIDValue(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.ID
}

func authFileNameValue(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.FileName
}

func authStatusValue(auth *coreauth.Auth) coreauth.Status {
	if auth == nil {
		return ""
	}
	return auth.Status
}

func authDisabledValue(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	return auth.Disabled
}

func authAttributesMap(auth *coreauth.Auth) map[string]string {
	if auth == nil {
		return nil
	}
	return auth.Attributes
}

func authMetadataMap(auth *coreauth.Auth) map[string]any {
	if auth == nil {
		return nil
	}
	return auth.Metadata
}

func authIndexValue(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.Index
}

func authEnsureIndex(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.EnsureIndex()
}

func authCloneValue(auth *coreauth.Auth) *coreauth.Auth {
	if auth == nil {
		return nil
	}
	return auth.Clone()
}

func authAccountInfoValue(auth *coreauth.Auth) (string, string) {
	if auth == nil {
		return "", ""
	}
	return auth.AccountInfo()
}
