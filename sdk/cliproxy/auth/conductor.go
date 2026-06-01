package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/downstreamtext"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// ProviderExecutor defines the contract required by Manager to execute provider calls.
type ProviderExecutor interface {
	// Identifier returns the provider key handled by this executor.
	Identifier() string
	// Execute handles non-streaming execution and returns the provider response payload.
	Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	// ExecuteStream handles streaming execution and returns a StreamResult containing
	// upstream headers and a channel of provider chunks.
	ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
	// Refresh attempts to refresh provider credentials and returns the updated auth state.
	Refresh(ctx context.Context, auth *Auth) (*Auth, error)
	// CountTokens returns the token count for the given request.
	CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	// HttpRequest injects provider credentials into the supplied HTTP request and executes it.
	// Callers must close the response body when non-nil.
	HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error)
}

// ExecutionSessionCloser allows executors to release per-session runtime resources.
type ExecutionSessionCloser interface {
	CloseExecutionSession(sessionID string)
}

const (
	// CloseAllExecutionSessionsID asks an executor to release all active execution sessions.
	// Executors that do not support this marker may ignore it.
	CloseAllExecutionSessionsID = "__all_execution_sessions__"
)

// RefreshEvaluator allows runtime state to override refresh decisions.
type RefreshEvaluator interface {
	ShouldRefresh(now time.Time, auth *Auth) bool
}

const (
	refreshCheckInterval  = 5 * time.Second
	refreshMaxConcurrency = 16
	refreshPendingBackoff = time.Minute
	refreshFailureBackoff = 5 * time.Minute
	// refreshIneffectiveBackoff throttles refresh attempts when an executor returns
	// success but the auth still evaluates as needing refresh (e.g. token expiry
	// wasn't updated). Without this guard, the auto-refresh loop can tight-loop and
	// burn CPU at idle.
	refreshIneffectiveBackoff = 30 * time.Second
	quotaBackoffBase          = time.Second
	quotaBackoffMax           = 30 * time.Minute
)

var quotaCooldownDisabled atomic.Bool

var shuffleRetryCandidates = func(candidates []retryCandidate) {
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
}

// SetQuotaCooldownDisabled toggles quota cooldown scheduling globally.
func SetQuotaCooldownDisabled(disable bool) {
	quotaCooldownDisabled.Store(disable)
}

func quotaCooldownDisabledForAuth(auth *Auth) bool {
	if auth != nil {
		if override, ok := auth.DisableCoolingOverride(); ok {
			return override
		}
	}
	return quotaCooldownDisabled.Load()
}

// Result captures execution outcome used to adjust auth state.
type Result struct {
	// AuthID references the auth that produced this result.
	AuthID string
	// AuthGeneration is the runtime generation observed by the request.
	AuthGeneration uint64
	// Provider is copied for convenience when emitting hooks.
	Provider string
	// Model is the upstream model identifier used for the request.
	Model string
	// Success marks whether the execution succeeded.
	Success bool
	// RetryAfter carries a provider supplied retry hint (e.g. 429 retryDelay).
	RetryAfter *time.Duration
	// Error describes the failure when Success is false.
	Error *Error
}

// Selector chooses an auth candidate for execution.
type Selector interface {
	Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error)
}

// StoppableSelector is an optional interface for selectors that hold resources.
// Selectors that implement this interface will have Stop called during shutdown.
type StoppableSelector interface {
	Selector
	Stop()
}

// Hook captures lifecycle callbacks for observing auth changes.
type Hook interface {
	// OnAuthRegistered fires when a new auth is registered.
	OnAuthRegistered(ctx context.Context, auth *Auth)
	// OnAuthUpdated fires when an existing auth changes state.
	OnAuthUpdated(ctx context.Context, auth *Auth)
	// OnResult fires when execution result is recorded.
	OnResult(ctx context.Context, result Result)
}

// NoopHook provides optional hook defaults.
type NoopHook struct{}

// OnAuthRegistered implements Hook.
func (NoopHook) OnAuthRegistered(context.Context, *Auth) {}

// OnAuthUpdated implements Hook.
func (NoopHook) OnAuthUpdated(context.Context, *Auth) {}

// OnResult implements Hook.
func (NoopHook) OnResult(context.Context, Result) {}

type executionResetSignal struct {
	generation uint64
	ch         <-chan struct{}
}

var errExecutionReset = errors.New("execution reset requested")

// Manager orchestrates auth lifecycle, selection, execution, and persistence.
type Manager struct {
	store     Store
	executors map[string]ProviderExecutor
	selector  Selector
	hook      Hook
	mu        sync.RWMutex
	auths     map[string]*Auth
	scheduler *authScheduler
	// providerOffsets tracks per-model provider rotation state for multi-provider routing.
	providerOffsets map[string]int

	// Retry controls request retry behavior.
	requestRetry     atomic.Int32
	maxRetryInterval atomic.Int64
	errorControl     atomic.Value

	// oauthModelAlias stores global OAuth model alias mappings (alias -> upstream name) keyed by channel.
	oauthModelAlias atomic.Value

	// apiKeyModelAlias caches resolved model alias mappings for API-key auths.
	// Keyed by auth.ID, value is alias(lower) -> upstream model (including suffix).
	apiKeyModelAlias atomic.Value

	// modelPoolOffsets tracks per-auth alias pool rotation state.
	modelPoolOffsets map[string]int

	resetMu                  sync.Mutex
	executionResetGeneration uint64
	executionResetCh         chan struct{}

	// runtimeConfig stores the latest application config for request-time decisions.
	// It is initialized in NewManager; never Load() before first Store().
	runtimeConfig atomic.Value

	// Optional HTTP RoundTripper provider injected by host.
	rtProvider RoundTripperProvider

	// Auto refresh state
	refreshCancel context.CancelFunc
	refreshLoop   *authAutoRefreshLoop
}

// NewManager constructs a manager with optional custom selector and hook.
func NewManager(store Store, selector Selector, hook Hook) *Manager {
	if selector == nil {
		selector = &RoundRobinSelector{}
	}
	if hook == nil {
		hook = NoopHook{}
	}
	manager := &Manager{
		store:            store,
		executors:        make(map[string]ProviderExecutor),
		selector:         selector,
		hook:             hook,
		auths:            make(map[string]*Auth),
		providerOffsets:  make(map[string]int),
		modelPoolOffsets: make(map[string]int),
		executionResetCh: make(chan struct{}),
	}
	// atomic.Value requires non-nil initial value.
	manager.runtimeConfig.Store(&internalconfig.Config{})
	manager.apiKeyModelAlias.Store(apiKeyModelAliasTable(nil))
	manager.errorControl.Store(internalconfig.ErrorControlConfig{})
	manager.scheduler = newAuthScheduler(selector)
	return manager
}

func isBuiltInSelector(selector Selector) bool {
	switch selector.(type) {
	case *RoundRobinSelector, *FillFirstSelector:
		return true
	default:
		return false
	}
}

func (m *Manager) syncSchedulerFromSnapshot(auths []*Auth) {
	if m == nil || m.scheduler == nil {
		return
	}
	m.scheduler.rebuild(auths)
}

func (m *Manager) syncScheduler() {
	if m == nil || m.scheduler == nil {
		return
	}
	m.syncSchedulerFromSnapshot(m.snapshotAuths())
}

func (m *Manager) snapshotAuths() []*Auth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Auth, 0, len(m.auths))
	for _, a := range m.auths {
		out = append(out, a.Clone())
	}
	return out
}

// RefreshSchedulerEntry re-upserts a single auth into the scheduler so that its
// supportedModelSet is rebuilt from the current global model registry state.
// This must be called after models have been registered for a newly added auth,
// because the initial scheduler.upsertAuth during Register/Update runs before
// registerModelsForAuth and therefore snapshots an empty model set.
func (m *Manager) RefreshSchedulerEntry(authID string) {
	if m == nil || m.scheduler == nil || authID == "" {
		return
	}
	m.mu.RLock()
	auth, ok := m.auths[authID]
	if !ok || auth == nil {
		m.mu.RUnlock()
		return
	}
	snapshot := auth.Clone()
	m.mu.RUnlock()
	m.scheduler.upsertAuth(snapshot)
}

// ReconcileRegistryModelStates aligns per-model runtime state with the current
// registry snapshot for one auth.
//
// Supported models are reset to a clean state because re-registration already
// cleared the registry-side cooldown/suspension snapshot. ModelStates for
// models that are no longer present in the registry are pruned entirely so
// renamed/removed models cannot keep auth-level status stale.
func (m *Manager) ReconcileRegistryModelStates(ctx context.Context, authID string) {
	if m == nil || authID == "" {
		return
	}

	supportedModels := registry.GetGlobalRegistry().GetModelsForClient(authID)
	supported := make(map[string]struct{}, len(supportedModels))
	for _, model := range supportedModels {
		if model == nil {
			continue
		}
		modelKey := canonicalModelKey(model.ID)
		if modelKey == "" {
			continue
		}
		supported[modelKey] = struct{}{}
	}

	var snapshot *Auth
	now := time.Now()

	m.mu.Lock()
	auth, ok := m.auths[authID]
	if ok && auth != nil && len(auth.ModelStates) > 0 {
		changed := false
		for modelKey, state := range auth.ModelStates {
			baseModel := canonicalModelKey(modelKey)
			if baseModel == "" {
				baseModel = strings.TrimSpace(modelKey)
			}
			if _, supportedModel := supported[baseModel]; !supportedModel {
				// Drop state for models that disappeared from the current registry
				// snapshot. Keeping them around leaks stale errors into auth-level
				// status, management output, and websocket fallback checks.
				delete(auth.ModelStates, modelKey)
				changed = true
				continue
			}
			if state == nil {
				continue
			}
			if modelStateIsClean(state) {
				continue
			}
			resetModelState(state, now)
			changed = true
		}
		if len(auth.ModelStates) == 0 {
			auth.ModelStates = nil
		}
		if changed {
			updateAggregatedAvailability(auth, now)
			if !hasModelError(auth, now) {
				auth.LastError = nil
				auth.StatusMessage = ""
				auth.Status = StatusActive
			}
			auth.UpdatedAt = now
			if errPersist := m.persist(ctx, auth); errPersist != nil {
				logEntryWithRequestID(ctx).WithField("auth_id", auth.ID).Warnf("failed to persist auth changes during model state reconciliation: %v", errPersist)
			}
			snapshot = auth.Clone()
		}
	}
	m.mu.Unlock()

	if m.scheduler != nil && snapshot != nil {
		m.scheduler.upsertAuth(snapshot)
	}
}

func (m *Manager) SetSelector(selector Selector) {
	if m == nil {
		return
	}
	if selector == nil {
		selector = &RoundRobinSelector{}
	}
	m.mu.Lock()
	m.selector = selector
	m.mu.Unlock()
	if m.scheduler != nil {
		m.scheduler.setSelector(selector)
		m.syncScheduler()
	}
}

// SetStore swaps the underlying persistence store.
func (m *Manager) SetStore(store Store) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = store
}

// SetRoundTripperProvider register a provider that returns a per-auth RoundTripper.
func (m *Manager) SetRoundTripperProvider(p RoundTripperProvider) {
	m.mu.Lock()
	m.rtProvider = p
	m.mu.Unlock()
}

// SetConfig updates the runtime config snapshot used by request-time helpers.
// Callers should provide the latest config on reload so per-credential alias mapping stays in sync.
func (m *Manager) SetConfig(cfg *internalconfig.Config) {
	if m == nil {
		return
	}
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	m.runtimeConfig.Store(cfg)
	m.rebuildAPIKeyModelAliasFromRuntimeConfig()
}

func (m *Manager) lookupAPIKeyUpstreamModel(authID, requestedModel string) string {
	if m == nil {
		return ""
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return ""
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return ""
	}
	table, _ := m.apiKeyModelAlias.Load().(apiKeyModelAliasTable)
	if table == nil {
		return ""
	}
	byAlias := table[authID]
	if len(byAlias) == 0 {
		return ""
	}
	key := strings.ToLower(thinking.ParseSuffix(requestedModel).ModelName)
	if key == "" {
		key = strings.ToLower(requestedModel)
	}
	resolved := strings.TrimSpace(byAlias[key])
	if resolved == "" {
		return ""
	}
	return preserveRequestedModelSuffix(requestedModel, resolved)
}

func isAPIKeyAuth(auth *Auth) bool {
	if auth == nil {
		return false
	}
	kind, _ := auth.AccountInfo()
	return strings.EqualFold(strings.TrimSpace(kind), "api_key")
}

func isOpenAICompatAPIKeyAuth(auth *Auth) bool {
	if !isAPIKeyAuth(auth) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		return true
	}
	if auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["compat_name"]) != ""
}

func openAICompatProviderKey(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if providerKey := strings.TrimSpace(auth.Attributes["provider_key"]); providerKey != "" {
			return strings.ToLower(providerKey)
		}
		if compatName := strings.TrimSpace(auth.Attributes["compat_name"]); compatName != "" {
			return strings.ToLower(compatName)
		}
	}
	return strings.ToLower(strings.TrimSpace(auth.Provider))
}

func openAICompatModelPoolKey(auth *Auth, requestedModel string) string {
	base := strings.TrimSpace(thinking.ParseSuffix(requestedModel).ModelName)
	if base == "" {
		base = strings.TrimSpace(requestedModel)
	}
	return strings.ToLower(strings.TrimSpace(auth.ID)) + "|" + openAICompatProviderKey(auth) + "|" + strings.ToLower(base)
}

func (m *Manager) nextModelPoolOffset(key string, size int) int {
	if m == nil || size <= 1 {
		return 0
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.modelPoolOffsets == nil {
		m.modelPoolOffsets = make(map[string]int)
	}
	offset := m.modelPoolOffsets[key]
	if offset >= 2_147_483_640 {
		offset = 0
	}
	m.modelPoolOffsets[key] = offset + 1
	if size <= 0 {
		return 0
	}
	return offset % size
}

func rotateStrings(values []string, offset int) []string {
	if len(values) <= 1 {
		return values
	}
	if offset <= 0 {
		out := make([]string, len(values))
		copy(out, values)
		return out
	}
	offset = offset % len(values)
	out := make([]string, 0, len(values))
	out = append(out, values[offset:]...)
	out = append(out, values[:offset]...)
	return out
}

func (m *Manager) resolveOpenAICompatUpstreamModelPool(auth *Auth, requestedModel string) []string {
	if m == nil || !isOpenAICompatAPIKeyAuth(auth) {
		return nil
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return nil
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	providerKey := ""
	compatName := ""
	if auth.Attributes != nil {
		providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, auth.Provider)
	if entry == nil {
		return nil
	}
	return resolveModelAliasPoolFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func preserveRequestedModelSuffix(requestedModel, resolved string) string {
	return preserveResolvedModelSuffix(resolved, thinking.ParseSuffix(requestedModel))
}

func (m *Manager) executionModelCandidates(auth *Auth, routeModel string) []string {
	requestedModel := rewriteModelForAuth(routeModel, auth)
	requestedModel = m.applyOAuthModelAlias(auth, requestedModel)
	if pool := m.resolveOpenAICompatUpstreamModelPool(auth, requestedModel); len(pool) > 0 {
		if len(pool) == 1 {
			return pool
		}
		offset := m.nextModelPoolOffset(openAICompatModelPoolKey(auth, requestedModel), len(pool))
		return rotateStrings(pool, offset)
	}
	resolved := m.applyAPIKeyModelAlias(auth, requestedModel)
	if strings.TrimSpace(resolved) == "" {
		resolved = requestedModel
	}
	return []string{resolved}
}

func (m *Manager) selectionModelForAuth(auth *Auth, routeModel string) string {
	requestedModel := rewriteModelForAuth(routeModel, auth)
	if strings.TrimSpace(requestedModel) == "" {
		requestedModel = strings.TrimSpace(routeModel)
	}
	resolvedModel := m.applyOAuthModelAlias(auth, requestedModel)
	if strings.TrimSpace(resolvedModel) == "" {
		resolvedModel = requestedModel
	}
	return resolvedModel
}

func (m *Manager) selectionModelKeyForAuth(auth *Auth, routeModel string) string {
	return canonicalModelKey(m.selectionModelForAuth(auth, routeModel))
}

func (m *Manager) stateModelForExecution(auth *Auth, routeModel, upstreamModel string, pooled bool) string {
	stateModel := executionResultModel(routeModel, upstreamModel, pooled)
	selectionModel := m.selectionModelForAuth(auth, routeModel)
	if canonicalModelKey(selectionModel) == canonicalModelKey(upstreamModel) && strings.TrimSpace(selectionModel) != "" {
		return strings.TrimSpace(upstreamModel)
	}
	return stateModel
}

func executionResultModel(routeModel, upstreamModel string, pooled bool) string {
	if pooled {
		if resolved := strings.TrimSpace(upstreamModel); resolved != "" {
			return resolved
		}
	}
	if requested := strings.TrimSpace(routeModel); requested != "" {
		return requested
	}
	return strings.TrimSpace(upstreamModel)
}

func (m *Manager) filterExecutionModels(auth *Auth, routeModel string, candidates []string, pooled bool) []string {
	if len(candidates) == 0 {
		return nil
	}
	now := time.Now()
	out := make([]string, 0, len(candidates))
	for _, upstreamModel := range candidates {
		stateModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)
		blocked, _, _ := isAuthBlockedForModel(auth, stateModel, now)
		if blocked {
			continue
		}
		out = append(out, upstreamModel)
	}
	return out
}

func (m *Manager) preparedExecutionModels(auth *Auth, routeModel string) ([]string, bool) {
	candidates := m.executionModelCandidates(auth, routeModel)
	pooled := len(candidates) > 1
	return m.filterExecutionModels(auth, routeModel, candidates, pooled), pooled
}

func (m *Manager) prepareExecutionModels(auth *Auth, routeModel string) []string {
	models, _ := m.preparedExecutionModels(auth, routeModel)
	return models
}

func (m *Manager) availableAuthsForRouteModel(auths []*Auth, provider, routeModel string, now time.Time) ([]*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}

	availableByPriority := make(map[priorityTier][]*Auth)
	cooldownCount := 0
	var earliest time.Time
	for _, candidate := range auths {
		if !candidate.GroupEnabled() {
			continue
		}
		checkModel := m.selectionModelForAuth(candidate, routeModel)
		blocked, reason, next := isAuthBlockedForModel(candidate, checkModel, now)
		if !blocked {
			tier := authPriorityTier(candidate)
			availableByPriority[tier] = append(availableByPriority[tier], candidate)
			continue
		}
		if reason == blockReasonCooldown {
			cooldownCount++
			if !next.IsZero() && (earliest.IsZero() || next.Before(earliest)) {
				earliest = next
			}
		}
	}

	if len(availableByPriority) == 0 {
		if cooldownCount == len(auths) && !earliest.IsZero() {
			providerForError := provider
			if providerForError == "mixed" {
				providerForError = ""
			}
			resetIn := earliest.Sub(now)
			if resetIn < 0 {
				resetIn = 0
			}
			return nil, newModelCooldownError(routeModel, providerForError, resetIn)
		}
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
	}

	bestPriority := priorityTier{}
	found := false
	for priority := range availableByPriority {
		if !found || priority.betterThan(bestPriority) {
			bestPriority = priority
			found = true
		}
	}

	available := availableByPriority[bestPriority]
	if len(available) > 1 {
		sort.Slice(available, func(i, j int) bool { return available[i].ID < available[j].ID })
	}
	return available, nil
}

func selectionArgForSelector(selector Selector, routeModel string) string {
	if isBuiltInSelector(selector) {
		return ""
	}
	return routeModel
}

func (m *Manager) authSupportsRouteModel(registryRef *registry.ModelRegistry, auth *Auth, routeModel string) bool {
	if registryRef == nil || auth == nil {
		return true
	}
	routeKey := canonicalModelKey(routeModel)
	if routeKey == "" {
		return true
	}
	if registryRef.ClientSupportsModel(auth.ID, routeKey) {
		return true
	}
	selectionKey := m.selectionModelKeyForAuth(auth, routeModel)
	return selectionKey != "" && selectionKey != routeKey && registryRef.ClientSupportsModel(auth.ID, selectionKey)
}

func discardStreamChunks(ch <-chan cliproxyexecutor.StreamChunk) {
	if ch == nil {
		return
	}
	go func() {
		for range ch {
		}
	}()
}

type streamBootstrapError struct {
	cause   error
	headers http.Header
}

func cloneHTTPHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	return headers.Clone()
}

func newStreamBootstrapError(err error, headers http.Header) error {
	if err == nil {
		return nil
	}
	return &streamBootstrapError{
		cause:   err,
		headers: cloneHTTPHeader(headers),
	}
}

func (e *streamBootstrapError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *streamBootstrapError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *streamBootstrapError) Headers() http.Header {
	if e == nil {
		return nil
	}
	return cloneHTTPHeader(e.headers)
}

func streamErrorResult(headers http.Header, err error) *cliproxyexecutor.StreamResult {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Err: err}
	close(ch)
	return &cliproxyexecutor.StreamResult{
		Headers: cloneHTTPHeader(headers),
		Chunks:  ch,
	}
}

func readStreamBootstrap(ctx context.Context, ch <-chan cliproxyexecutor.StreamChunk, format sdktranslator.Format, waitForText bool) ([]cliproxyexecutor.StreamChunk, bool, error) {
	if ch == nil {
		return nil, true, nil
	}
	buffered := make([]cliproxyexecutor.StreamChunk, 0, 1)
	for {
		var (
			chunk cliproxyexecutor.StreamChunk
			ok    bool
		)
		if ctx != nil {
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case chunk, ok = <-ch:
			}
		} else {
			chunk, ok = <-ch
		}
		if !ok {
			return buffered, true, nil
		}
		if chunk.Err != nil {
			return nil, false, chunk.Err
		}
		buffered = append(buffered, chunk)
		if len(chunk.Payload) > 0 && streamBootstrapPayloadAllowsStart(format, chunk.Payload, waitForText) {
			return buffered, false, nil
		}
	}
}

func streamBootstrapPayloadAllowsStart(format sdktranslator.Format, payload []byte, waitForText bool) bool {
	if len(bytes.TrimSpace(payload)) == 0 {
		return false
	}
	if !waitForText {
		return true
	}

	dataPayloads, sawData, sawSSE := streamBootstrapSSEDataPayloads(payload)
	if sawSSE {
		if !sawData {
			return false
		}
		for _, data := range dataPayloads {
			if streamBootstrapJSONPayloadHasText(format, data) {
				return true
			}
		}
		return false
	}

	return streamBootstrapJSONPayloadHasText(format, payload)
}

func streamBootstrapSSEDataPayloads(payload []byte) ([][]byte, bool, bool) {
	lines := bytes.Split(payload, []byte("\n"))
	dataPayloads := make([][]byte, 0, len(lines))
	var sawData bool
	var sawSSE bool
	for _, rawLine := range lines {
		line := bytes.TrimSpace(rawLine)
		if len(line) == 0 {
			continue
		}
		if line[0] == ':' {
			sawSSE = true
			continue
		}
		switch {
		case bytes.HasPrefix(line, []byte("data:")):
			sawSSE = true
			sawData = true
			dataPayloads = append(dataPayloads, bytes.TrimSpace(line[len("data:"):]))
		case bytes.HasPrefix(line, []byte("event:")),
			bytes.HasPrefix(line, []byte("id:")),
			bytes.HasPrefix(line, []byte("retry:")):
			sawSSE = true
		}
	}
	return dataPayloads, sawData, sawSSE
}

func streamBootstrapJSONPayloadHasText(format sdktranslator.Format, payload []byte) bool {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return false
	}
	if !json.Valid(trimmed) {
		return true
	}
	_, ok := downstreamtext.Extract(format, trimmed)
	return ok
}

func (m *Manager) shouldWaitForStreamBootstrapText() bool {
	if m == nil {
		return false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		return false
	}
	return cfg.OutputFilter.HasActiveRules()
}

func (m *Manager) wrapStreamResult(ctx context.Context, auth *Auth, provider, resultModel string, headers http.Header, buffered []cliproxyexecutor.StreamChunk, remaining <-chan cliproxyexecutor.StreamChunk) *cliproxyexecutor.StreamResult {
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		var failed bool
		forward := true
		emit := func(chunk cliproxyexecutor.StreamChunk) bool {
			if chunk.Err != nil && !failed {
				failed = true
				rerr := &Error{Message: chunk.Err.Error()}
				if se, ok := errors.AsType[cliproxyexecutor.StatusError](chunk.Err); ok && se != nil {
					rerr.HTTPStatus = se.StatusCode()
				}
				m.MarkResult(ctx, Result{AuthID: auth.ID, AuthGeneration: auth.RuntimeGeneration, Provider: provider, Model: resultModel, Success: false, Error: rerr})
			}
			if !forward {
				return false
			}
			if ctx == nil {
				out <- chunk
				return true
			}
			select {
			case <-ctx.Done():
				forward = false
				return false
			case out <- chunk:
				return true
			}
		}
		for _, chunk := range buffered {
			if ok := emit(chunk); !ok {
				discardStreamChunks(remaining)
				return
			}
		}
		for chunk := range remaining {
			if ok := emit(chunk); !ok {
				discardStreamChunks(remaining)
				return
			}
		}
		if !failed {
			m.MarkResult(ctx, Result{AuthID: auth.ID, AuthGeneration: auth.RuntimeGeneration, Provider: provider, Model: resultModel, Success: true})
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}
}

func (m *Manager) executeStreamWithModelPool(ctx context.Context, executor ProviderExecutor, auth *Auth, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, routeModel string, execModels []string, pooled bool, signal executionResetSignal) (*cliproxyexecutor.StreamResult, error) {
	if executor == nil {
		return nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	var lastErr error
	for idx, execModel := range execModels {
		if m.executionResetChanged(signal) {
			return nil, errExecutionReset
		}
		resultModel := m.stateModelForExecution(auth, routeModel, execModel, pooled)
		execReq := req
		execReq.Model = execModel
		streamResult, errStream := executor.ExecuteStream(ctx, auth, execReq, opts)
		if errStream != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				return nil, errCtx
			}
			rerr := &Error{Message: errStream.Error()}
			if se, ok := errors.AsType[cliproxyexecutor.StatusError](errStream); ok && se != nil {
				rerr.HTTPStatus = se.StatusCode()
			}
			result := Result{AuthID: auth.ID, AuthGeneration: auth.RuntimeGeneration, Provider: provider, Model: resultModel, Success: false, Error: rerr}
			result.RetryAfter = retryAfterFromError(errStream)
			m.MarkResult(ctx, result)
			lastErr = errStream
			if m.executionResetChanged(signal) {
				return nil, errExecutionReset
			}
			continue
		}

		buffered, closed, bootstrapErr := readStreamBootstrap(ctx, streamResult.Chunks, opts.SourceFormat, m.shouldWaitForStreamBootstrapText())
		if bootstrapErr != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				discardStreamChunks(streamResult.Chunks)
				return nil, errCtx
			}
			if idx < len(execModels)-1 {
				rerr := &Error{Message: bootstrapErr.Error()}
				if se, ok := errors.AsType[cliproxyexecutor.StatusError](bootstrapErr); ok && se != nil {
					rerr.HTTPStatus = se.StatusCode()
				}
				result := Result{AuthID: auth.ID, AuthGeneration: auth.RuntimeGeneration, Provider: provider, Model: resultModel, Success: false, Error: rerr}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				m.MarkResult(ctx, result)
				discardStreamChunks(streamResult.Chunks)
				lastErr = bootstrapErr
				if m.executionResetChanged(signal) {
					return nil, errExecutionReset
				}
				continue
			}
			rerr := &Error{Message: bootstrapErr.Error()}
			if se, ok := errors.AsType[cliproxyexecutor.StatusError](bootstrapErr); ok && se != nil {
				rerr.HTTPStatus = se.StatusCode()
			}
			result := Result{AuthID: auth.ID, AuthGeneration: auth.RuntimeGeneration, Provider: provider, Model: resultModel, Success: false, Error: rerr}
			result.RetryAfter = retryAfterFromError(bootstrapErr)
			m.MarkResult(ctx, result)
			discardStreamChunks(streamResult.Chunks)
			if m.executionResetChanged(signal) {
				return nil, errExecutionReset
			}
			return nil, newStreamBootstrapError(bootstrapErr, streamResult.Headers)
		}

		if closed && len(buffered) == 0 {
			emptyErr := &Error{Code: "empty_stream", Message: "upstream stream closed before first payload", Retryable: true}
			result := Result{AuthID: auth.ID, AuthGeneration: auth.RuntimeGeneration, Provider: provider, Model: resultModel, Success: false, Error: emptyErr}
			m.MarkResult(ctx, result)
			if m.executionResetChanged(signal) {
				return nil, errExecutionReset
			}
			if idx < len(execModels)-1 {
				lastErr = emptyErr
				continue
			}
			return nil, newStreamBootstrapError(emptyErr, streamResult.Headers)
		}

		remaining := streamResult.Chunks
		if closed {
			closedCh := make(chan cliproxyexecutor.StreamChunk)
			close(closedCh)
			remaining = closedCh
		}
		return m.wrapStreamResult(ctx, auth.Clone(), provider, resultModel, streamResult.Headers, buffered, remaining), nil
	}
	if lastErr == nil {
		lastErr = &Error{Code: "auth_not_found", Message: "no upstream model available"}
	}
	return nil, lastErr
}

func (m *Manager) rebuildAPIKeyModelAliasFromRuntimeConfig() {
	if m == nil {
		return
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rebuildAPIKeyModelAliasLocked(cfg)
}

func (m *Manager) rebuildAPIKeyModelAliasLocked(cfg *internalconfig.Config) {
	if m == nil {
		return
	}
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}

	out := make(apiKeyModelAliasTable)
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		if strings.TrimSpace(auth.ID) == "" {
			continue
		}
		kind, _ := auth.AccountInfo()
		if !strings.EqualFold(strings.TrimSpace(kind), "api_key") {
			continue
		}

		byAlias := make(map[string]string)
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		switch provider {
		case "gemini":
			if entry := resolveGeminiAPIKeyConfig(cfg, auth); entry != nil {
				compileAPIKeyModelAliasForModels(byAlias, entry.Models)
			}
		case "claude":
			if entry := resolveClaudeAPIKeyConfig(cfg, auth); entry != nil {
				compileAPIKeyModelAliasForModels(byAlias, entry.Models)
			}
		case "codex":
			if entry := resolveCodexAPIKeyConfig(cfg, auth); entry != nil {
				compileAPIKeyModelAliasForModels(byAlias, entry.Models)
			}
		case "vertex":
			if entry := resolveVertexAPIKeyConfig(cfg, auth); entry != nil {
				compileAPIKeyModelAliasForModels(byAlias, entry.Models)
			}
		default:
			// OpenAI-compat uses config selection from auth.Attributes.
			providerKey := ""
			compatName := ""
			if auth.Attributes != nil {
				providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
				compatName = strings.TrimSpace(auth.Attributes["compat_name"])
			}
			if compatName != "" || strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
				if entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, auth.Provider); entry != nil {
					compileAPIKeyModelAliasForModels(byAlias, entry.Models)
				}
			}
		}

		if len(byAlias) > 0 {
			out[auth.ID] = byAlias
		}
	}

	m.apiKeyModelAlias.Store(out)
}

func compileAPIKeyModelAliasForModels[T interface {
	GetName() string
	GetAlias() string
}](out map[string]string, models []T) {
	if out == nil {
		return
	}
	for i := range models {
		alias := strings.TrimSpace(models[i].GetAlias())
		name := strings.TrimSpace(models[i].GetName())
		if alias == "" || name == "" {
			continue
		}
		aliasKey := strings.ToLower(thinking.ParseSuffix(alias).ModelName)
		if aliasKey == "" {
			aliasKey = strings.ToLower(alias)
		}
		// Config priority: first alias wins.
		if _, exists := out[aliasKey]; exists {
			continue
		}
		out[aliasKey] = name
		// Also allow direct lookup by upstream name (case-insensitive), so lookups on already-upstream
		// models remain a cheap no-op.
		nameKey := strings.ToLower(thinking.ParseSuffix(name).ModelName)
		if nameKey == "" {
			nameKey = strings.ToLower(name)
		}
		if nameKey != "" {
			if _, exists := out[nameKey]; !exists {
				out[nameKey] = name
			}
		}
		// Preserve config suffix priority by seeding a base-name lookup when name already has suffix.
		nameResult := thinking.ParseSuffix(name)
		if nameResult.HasSuffix {
			baseKey := strings.ToLower(strings.TrimSpace(nameResult.ModelName))
			if baseKey != "" {
				if _, exists := out[baseKey]; !exists {
					out[baseKey] = name
				}
			}
		}
	}
}

// SetRetryConfig updates retry attempts and cooldown wait interval.
func (m *Manager) SetRetryConfig(retry int, maxRetryInterval time.Duration) {
	if m == nil {
		return
	}
	if retry < 0 {
		retry = 0
	}
	if maxRetryInterval < 0 {
		maxRetryInterval = 0
	}
	m.requestRetry.Store(int32(retry))
	m.maxRetryInterval.Store(maxRetryInterval.Nanoseconds())
}

// SetErrorControlConfig updates provider-local retry and whole-request round policy.
func (m *Manager) SetErrorControlConfig(cfg internalconfig.ErrorControlConfig) {
	if m == nil {
		return
	}
	normalized := internalconfig.ErrorControlConfig{
		Default: sanitizeErrorControlPolicy(cfg.Default),
	}
	if len(cfg.Providers) > 0 {
		normalized.Providers = make(map[string]internalconfig.ErrorControlPolicy, len(cfg.Providers))
		for provider, policy := range cfg.Providers {
			key := strings.ToLower(strings.TrimSpace(provider))
			if key == "" {
				continue
			}
			normalized.Providers[key] = sanitizeErrorControlPolicy(policy)
		}
	}
	m.errorControl.Store(normalized)
}

func sanitizeErrorControlPolicy(policy internalconfig.ErrorControlPolicy) internalconfig.ErrorControlPolicy {
	if policy.ProviderRetries != nil {
		value := *policy.ProviderRetries
		if value < 1 {
			value = 1
		}
		policy.ProviderRetries = internalconfig.DefaultIntPtr(value)
	}
	if policy.RetryRounds != nil {
		value := *policy.RetryRounds
		if value < 1 {
			value = 1
		}
		policy.RetryRounds = internalconfig.DefaultIntPtr(value)
	}
	return policy
}

func authEnabled(auth *Auth) bool {
	return auth != nil && !auth.Disabled && auth.Status != StatusDisabled
}

func shouldResetAuthRuntimeOnUpdate(existing, next *Auth) bool {
	if existing == nil || next == nil {
		return false
	}
	return authConfigFingerprint(existing) != authConfigFingerprint(next)
}

func authConfigMetadataFingerprint(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	keys := map[string]struct{}{
		"backoff-mode":           {},
		"backoff_mode":           {},
		"disable-cooling":        {},
		"disable_cooling":        {},
		"headers":                {},
		"priority":               {},
		"provider-retries":       {},
		"provider_retries":       {},
		"proxy-url":              {},
		"proxy_url":              {},
		"request-retry":          {},
		"request_retry":          {},
		"retry-rounds":           {},
		"retry_rounds":           {},
		"round-backoff-base":     {},
		"round-backoff-exponent": {},
		"round-backoff-max":      {},
		"round_backoff_base":     {},
		"round_backoff_exponent": {},
		"round_backoff_max":      {},
		"tool-prefix-disabled":   {},
		"tool_prefix_disabled":   {},
		"websockets":             {},
	}
	filtered := make(map[string]any)
	for key, value := range metadata {
		if _, ok := keys[key]; ok {
			filtered[key] = value
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func authConfigAttributesFingerprint(attrs map[string]string) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	filtered := make(map[string]string, len(attrs))
	for key, value := range attrs {
		if key == "note" {
			continue
		}
		filtered[key] = value
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func authConfigFingerprint(auth *Auth) string {
	if auth == nil {
		return ""
	}
	type comparableAuthConfig struct {
		Provider   string            `json:"provider,omitempty"`
		Prefix     string            `json:"prefix,omitempty"`
		FileName   string            `json:"file_name,omitempty"`
		Label      string            `json:"label,omitempty"`
		Disabled   bool              `json:"disabled,omitempty"`
		ProxyURL   string            `json:"proxy_url,omitempty"`
		Attributes map[string]string `json:"attributes,omitempty"`
		Metadata   map[string]any    `json:"metadata,omitempty"`
	}
	payload := comparableAuthConfig{
		Provider:   strings.TrimSpace(auth.Provider),
		Prefix:     strings.TrimSpace(auth.Prefix),
		FileName:   strings.TrimSpace(auth.FileName),
		Label:      strings.TrimSpace(auth.Label),
		Disabled:   auth.Disabled || auth.Status == StatusDisabled,
		ProxyURL:   strings.TrimSpace(auth.ProxyURL),
		Attributes: authConfigAttributesFingerprint(auth.Attributes),
		Metadata:   authConfigMetadataFingerprint(auth.Metadata),
	}
	data, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return ""
	}
	return string(data)
}

func quotaStateIsZero(state QuotaState) bool {
	return !state.Exceeded && state.Reason == "" && state.NextRecoverAt.IsZero() && state.BackoffLevel == 0
}

func authRuntimeStateIsClean(auth *Auth) bool {
	if auth == nil {
		return true
	}
	return auth.StatusMessage == "" &&
		!auth.Unavailable &&
		auth.NextRetryAfter.IsZero() &&
		quotaStateIsZero(auth.Quota) &&
		auth.LastError == nil &&
		len(auth.ModelStates) == 0
}

func cloneModelStates(states map[string]*ModelState) map[string]*ModelState {
	if len(states) == 0 {
		return nil
	}
	cloned := make(map[string]*ModelState, len(states))
	for model, state := range states {
		cloned[model] = state.Clone()
	}
	return cloned
}

func authMarkedRemoved(auth *Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	value := strings.ToLower(strings.TrimSpace(auth.Attributes["runtime_removed"]))
	return value == "true" || value == "1" || value == "yes"
}

func preserveRuntimeMetadataOnSourceRefresh(existing, next *Auth) {
	if existing == nil || next == nil || len(existing.Metadata) == 0 {
		return
	}
	keys := [...]string{
		"access_token",
		"id_token",
		"refresh_token",
		"token",
		"token_type",
		"expired",
		"expires_in",
		"timestamp",
		"last_refresh",
		"lastRefresh",
		"account_id",
		"credit_balance",
		"creditBalance",
	}
	for _, key := range keys {
		value, ok := existing.Metadata[key]
		if !ok {
			continue
		}
		if next.Metadata != nil {
			if _, exists := next.Metadata[key]; exists {
				continue
			}
		} else {
			next.Metadata = make(map[string]any)
		}
		next.Metadata[key] = value
	}
}

func preserveRuntimeStateOnSourceRefresh(existing, next *Auth) {
	if existing == nil || next == nil || !authRuntimeStateIsClean(next) {
		return
	}
	if authMarkedRemoved(existing) {
		return
	}
	if existing.Status != "" && existing.Status != StatusDisabled && next.Status != StatusDisabled {
		next.Status = existing.Status
	}
	next.StatusMessage = existing.StatusMessage
	next.Unavailable = existing.Unavailable
	next.NextRetryAfter = existing.NextRetryAfter
	next.Quota = existing.Quota
	next.LastError = cloneError(existing.LastError)
	next.ModelStates = cloneModelStates(existing.ModelStates)
	preserveRuntimeMetadataOnSourceRefresh(existing, next)
}

func resetAuthRuntimeState(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	disabled := auth.Disabled || auth.Status == StatusDisabled
	statusMessage := auth.StatusMessage
	auth.Unavailable = false
	if disabled {
		auth.Status = StatusDisabled
		auth.StatusMessage = statusMessage
	} else {
		auth.Status = StatusActive
		auth.StatusMessage = ""
	}
	auth.NextRetryAfter = time.Time{}
	auth.Quota = QuotaState{}
	auth.LastError = nil
	auth.ModelStates = nil
	auth.UpdatedAt = now
}

func (m *Manager) currentExecutionResetSignal() executionResetSignal {
	if m == nil {
		return executionResetSignal{}
	}
	m.resetMu.Lock()
	defer m.resetMu.Unlock()
	if m.executionResetCh == nil {
		m.executionResetCh = make(chan struct{})
	}
	return executionResetSignal{
		generation: m.executionResetGeneration,
		ch:         m.executionResetCh,
	}
}

func (m *Manager) executionResetChanged(signal executionResetSignal) bool {
	if m == nil {
		return false
	}
	m.resetMu.Lock()
	defer m.resetMu.Unlock()
	return m.executionResetGeneration != signal.generation
}

func (m *Manager) broadcastExecutionReset() {
	if m == nil {
		return
	}
	m.resetMu.Lock()
	oldCh := m.executionResetCh
	m.executionResetGeneration++
	m.executionResetCh = make(chan struct{})
	m.resetMu.Unlock()
	if oldCh != nil {
		close(oldCh)
	}
}

func (m *Manager) clearRuntimeErrorsForProviders(providers []string) {
	if m == nil {
		return
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key != "" {
			providerSet[key] = struct{}{}
		}
	}
	now := time.Now()
	m.mu.Lock()
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if len(providerSet) > 0 {
			if _, ok := providerSet[provider]; !ok {
				continue
			}
		}
		resetAuthRuntimeState(auth, now)
	}
	m.mu.Unlock()
	m.syncScheduler()
}

func (m *Manager) resetRotationState(provider, authID string) {
	if m == nil {
		return
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	authID = strings.TrimSpace(authID)
	m.mu.Lock()
	for key := range m.providerOffsets {
		if provider == "" || strings.Contains(key, provider) {
			delete(m.providerOffsets, key)
		}
	}
	for key := range m.modelPoolOffsets {
		if authID == "" || strings.HasPrefix(key, authID+"|") {
			delete(m.modelPoolOffsets, key)
		}
	}
	m.mu.Unlock()
	m.syncScheduler()
}

// RegisterExecutor registers a provider executor with the manager.
func (m *Manager) RegisterExecutor(executor ProviderExecutor) {
	if executor == nil {
		return
	}
	provider := strings.TrimSpace(executor.Identifier())
	if provider == "" {
		return
	}

	var replaced ProviderExecutor
	m.mu.Lock()
	replaced = m.executors[provider]
	m.executors[provider] = executor
	m.mu.Unlock()

	if replaced == nil || replaced == executor {
		return
	}
	if closer, ok := replaced.(ExecutionSessionCloser); ok && closer != nil {
		closer.CloseExecutionSession(CloseAllExecutionSessionsID)
	}
}

// UnregisterExecutor removes the executor associated with the provider key.
func (m *Manager) UnregisterExecutor(provider string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return
	}
	m.mu.Lock()
	delete(m.executors, provider)
	m.mu.Unlock()
}

// Register inserts a new auth entry into the manager.
func (m *Manager) Register(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil {
		return nil, nil
	}
	if auth.ID == "" {
		auth.ID = uuid.NewString()
	}
	auth.EnsureIndex()
	authClone := auth.Clone()
	if authClone.RuntimeGeneration == 0 {
		authClone.RuntimeGeneration = 1
		auth.RuntimeGeneration = authClone.RuntimeGeneration
	}
	m.mu.Lock()
	m.auths[auth.ID] = authClone
	m.mu.Unlock()
	m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	if m.scheduler != nil {
		m.scheduler.upsertAuth(authClone)
	}
	m.broadcastExecutionReset()
	m.queueRefreshReschedule(auth.ID)
	_ = m.persist(ctx, auth)
	m.hook.OnAuthRegistered(ctx, auth.Clone())
	return auth.Clone(), nil
}

// Update replaces an existing auth entry and notifies hooks.
func (m *Manager) Update(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil || auth.ID == "" {
		return nil, nil
	}
	resetRuntimeState := shouldResetRuntimeState(ctx)
	restartExecution := resetRuntimeState
	restartProvider := ""
	restartAuthID := auth.ID
	m.mu.Lock()
	if existing, ok := m.auths[auth.ID]; ok && existing != nil {
		if !auth.indexAssigned && auth.Index == "" {
			auth.Index = existing.Index
			auth.indexAssigned = existing.indexAssigned
		}
		configChanged := shouldResetAuthRuntimeOnUpdate(existing, auth)
		restartExecution = resetRuntimeState || configChanged
		if !resetRuntimeState {
			preserveRuntimeStateOnSourceRefresh(existing, auth)
		}
		if auth.RuntimeGeneration == 0 {
			auth.RuntimeGeneration = existing.RuntimeGeneration
		}
	}
	auth.EnsureIndex()
	authClone := auth.Clone()
	if authClone.RuntimeGeneration == 0 {
		authClone.RuntimeGeneration = 1
	}
	if restartExecution {
		authClone.RuntimeGeneration++
	}
	if resetRuntimeState {
		resetAuthRuntimeState(authClone, time.Now())
	}
	auth.RuntimeGeneration = authClone.RuntimeGeneration
	restartProvider = strings.ToLower(strings.TrimSpace(authClone.Provider))
	m.auths[auth.ID] = authClone
	m.mu.Unlock()
	m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	if restartExecution {
		m.resetRotationState(restartProvider, restartAuthID)
		m.broadcastExecutionReset()
	} else if m.scheduler != nil {
		m.scheduler.upsertAuth(authClone)
	}
	m.queueRefreshReschedule(authClone.ID)
	_ = m.persist(ctx, authClone)
	m.hook.OnAuthUpdated(ctx, authClone.Clone())
	return authClone.Clone(), nil
}

// Load resets manager state from the backing store.
func (m *Manager) Load(ctx context.Context) error {
	m.mu.Lock()
	if m.store == nil {
		m.mu.Unlock()
		return nil
	}
	items, err := m.store.List(ctx)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.auths = make(map[string]*Auth, len(items))
	for _, auth := range items {
		if auth == nil || auth.ID == "" {
			continue
		}
		auth.EnsureIndex()
		m.auths[auth.ID] = auth.Clone()
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	m.rebuildAPIKeyModelAliasLocked(cfg)
	m.mu.Unlock()
	m.syncScheduler()
	return nil
}

// Execute performs a non-streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) Execute(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	opts = ensureRequestedModelMetadata(opts, req.Model)

	for {
		signal := m.currentExecutionResetSignal()
		candidates, errCandidates := m.retryCandidates(ctx, normalized, req.Model, opts)
		if errCandidates != nil {
			if m.executionResetChanged(signal) {
				m.clearRuntimeErrorsForProviders(normalized)
				continue
			}
			return cliproxyexecutor.Response{}, errCandidates
		}
		maxRounds := m.maxRetryRounds(normalized)
		roundBase, roundExp, roundMax := m.roundBackoffPolicy(normalized)

		var lastErr error
		restartExecution := false
		for round := 0; round < maxRounds; round++ {
			if m.executionResetChanged(signal) {
				restartExecution = true
				break
			}
			roundStart := time.Now()
			activeCandidates := roundCandidates(candidates, round)
			if len(activeCandidates) == 0 {
				break
			}
			for _, candidate := range activeCandidates {
				if candidate.auth == nil {
					continue
				}
				resp, errExec := m.executeRetryCandidate(ctx, candidate, req, opts, signal)
				if errExec == nil {
					return resp, nil
				}
				if errors.Is(errExec, errExecutionReset) {
					restartExecution = true
					break
				}
				lastErr = errExec
			}
			if restartExecution {
				break
			}
			if round < maxRounds-1 {
				target := computeRoundBackoff(round+1, roundBase, roundExp, roundMax)
				if wait := retryRoundWait(roundStart, target); wait > 0 {
					reset, errWait := waitForCooldown(ctx, wait, signal)
					if errWait != nil {
						return cliproxyexecutor.Response{}, errWait
					}
					if reset {
						restartExecution = true
						break
					}
				}
			}
		}
		if restartExecution {
			m.clearRuntimeErrorsForProviders(normalized)
			continue
		}
		if lastErr != nil {
			return cliproxyexecutor.Response{}, newUpstreamExhaustedError(lastErr)
		}
		return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
}

// ExecuteCount performs a non-streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) ExecuteCount(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	opts = ensureRequestedModelMetadata(opts, req.Model)

	for {
		signal := m.currentExecutionResetSignal()
		candidates, errCandidates := m.retryCandidates(ctx, normalized, req.Model, opts)
		if errCandidates != nil {
			if m.executionResetChanged(signal) {
				m.clearRuntimeErrorsForProviders(normalized)
				continue
			}
			return cliproxyexecutor.Response{}, errCandidates
		}
		maxRounds := m.maxRetryRounds(normalized)
		roundBase, roundExp, roundMax := m.roundBackoffPolicy(normalized)

		var lastErr error
		restartExecution := false
		for round := 0; round < maxRounds; round++ {
			if m.executionResetChanged(signal) {
				restartExecution = true
				break
			}
			roundStart := time.Now()
			activeCandidates := roundCandidates(candidates, round)
			if len(activeCandidates) == 0 {
				break
			}
			for _, candidate := range activeCandidates {
				if candidate.auth == nil {
					continue
				}
				resp, errExec := m.executeCountRetryCandidate(ctx, candidate, req, opts, signal)
				if errExec == nil {
					return resp, nil
				}
				if errors.Is(errExec, errExecutionReset) {
					restartExecution = true
					break
				}
				lastErr = errExec
			}
			if restartExecution {
				break
			}
			if round < maxRounds-1 {
				target := computeRoundBackoff(round+1, roundBase, roundExp, roundMax)
				if wait := retryRoundWait(roundStart, target); wait > 0 {
					reset, errWait := waitForCooldown(ctx, wait, signal)
					if errWait != nil {
						return cliproxyexecutor.Response{}, errWait
					}
					if reset {
						restartExecution = true
						break
					}
				}
			}
		}
		if restartExecution {
			m.clearRuntimeErrorsForProviders(normalized)
			continue
		}
		if lastErr != nil {
			return cliproxyexecutor.Response{}, newUpstreamExhaustedError(lastErr)
		}
		return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
}

// ExecuteStream performs a streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) ExecuteStream(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	opts = ensureRequestedModelMetadata(opts, req.Model)

	for {
		signal := m.currentExecutionResetSignal()
		candidates, errCandidates := m.retryCandidates(ctx, normalized, req.Model, opts)
		if errCandidates != nil {
			if m.executionResetChanged(signal) {
				m.clearRuntimeErrorsForProviders(normalized)
				continue
			}
			return nil, errCandidates
		}
		maxRounds := m.maxRetryRounds(normalized)
		roundBase, roundExp, roundMax := m.roundBackoffPolicy(normalized)

		var lastErr error
		restartExecution := false
		for round := 0; round < maxRounds; round++ {
			if m.executionResetChanged(signal) {
				restartExecution = true
				break
			}
			roundStart := time.Now()
			activeCandidates := roundCandidates(candidates, round)
			if len(activeCandidates) == 0 {
				break
			}
			for _, candidate := range activeCandidates {
				if candidate.auth == nil {
					continue
				}
				result, errStream := m.executeStreamRetryCandidate(ctx, candidate, req, opts, signal)
				if errStream == nil {
					return result, nil
				}
				if errors.Is(errStream, errExecutionReset) {
					restartExecution = true
					break
				}
				lastErr = errStream
			}
			if restartExecution {
				break
			}
			if round < maxRounds-1 {
				target := computeRoundBackoff(round+1, roundBase, roundExp, roundMax)
				if wait := retryRoundWait(roundStart, target); wait > 0 {
					reset, errWait := waitForCooldown(ctx, wait, signal)
					if errWait != nil {
						return nil, errWait
					}
					if reset {
						restartExecution = true
						break
					}
				}
			}
		}
		if restartExecution {
			m.clearRuntimeErrorsForProviders(normalized)
			continue
		}
		if lastErr != nil {
			var bootstrapErr *streamBootstrapError
			if errors.As(lastErr, &bootstrapErr) && bootstrapErr != nil {
				return streamErrorResult(bootstrapErr.Headers(), bootstrapErr.cause), nil
			}
			return nil, newUpstreamExhaustedError(lastErr)
		}
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
}

func (m *Manager) executeRetryCandidate(ctx context.Context, candidate retryCandidate, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, signal executionResetSignal) (cliproxyexecutor.Response, error) {
	if candidate.executor == nil || candidate.auth == nil {
		return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	entry := logEntryWithRequestID(ctx)
	debugLogAuthSelection(entry, candidate.auth, candidate.provider, req.Model)
	publishSelectedAuthMetadata(opts.Metadata, candidate.auth.ID)

	execCtx := m.executionContextForAuth(ctx, candidate.auth)
	routeModel := req.Model
	models, pooled := m.preparedRetryExecutionModels(candidate.auth, routeModel)
	if len(models) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "auth_unavailable", Message: "no upstream model available"}
	}

	var lastErr error
	providerRetries := m.providerRetriesForCandidate(candidate)
	for _, upstreamModel := range models {
		if m.executionResetChanged(signal) {
			return cliproxyexecutor.Response{}, errExecutionReset
		}
		resultModel := m.stateModelForExecution(candidate.auth, routeModel, upstreamModel, pooled)
		execReq := req
		execReq.Model = upstreamModel
		for attempt := 0; attempt < providerRetries; attempt++ {
			if m.executionResetChanged(signal) {
				return cliproxyexecutor.Response{}, errExecutionReset
			}
			resp, errExec := candidate.executor.Execute(execCtx, candidate.auth, execReq, opts)
			result := Result{AuthID: candidate.auth.ID, AuthGeneration: candidate.auth.RuntimeGeneration, Provider: candidate.provider, Model: resultModel, Success: errExec == nil}
			if errExec != nil {
				if errCtx := execCtx.Err(); errCtx != nil {
					return cliproxyexecutor.Response{}, errCtx
				}
				result.Error = &Error{Message: errExec.Error()}
				if se, ok := errors.AsType[cliproxyexecutor.StatusError](errExec); ok && se != nil {
					result.Error.HTTPStatus = se.StatusCode()
				}
				result.RetryAfter = retryAfterFromError(errExec)
				m.MarkResult(execCtx, result)
				lastErr = errExec
				if m.executionResetChanged(signal) {
					return cliproxyexecutor.Response{}, errExecutionReset
				}
				if !isRetryableUpstreamError(errExec) {
					break
				}
				continue
			}
			m.MarkResult(execCtx, result)
			return resp, nil
		}
	}
	if lastErr == nil {
		lastErr = &Error{Code: "auth_not_found", Message: "no upstream model available"}
	}
	return cliproxyexecutor.Response{}, lastErr
}

func (m *Manager) executeCountRetryCandidate(ctx context.Context, candidate retryCandidate, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, signal executionResetSignal) (cliproxyexecutor.Response, error) {
	if candidate.executor == nil || candidate.auth == nil {
		return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	entry := logEntryWithRequestID(ctx)
	debugLogAuthSelection(entry, candidate.auth, candidate.provider, req.Model)
	publishSelectedAuthMetadata(opts.Metadata, candidate.auth.ID)

	execCtx := m.executionContextForAuth(ctx, candidate.auth)
	routeModel := req.Model
	models, pooled := m.preparedRetryExecutionModels(candidate.auth, routeModel)
	if len(models) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "auth_unavailable", Message: "no upstream model available"}
	}

	var lastErr error
	providerRetries := m.providerRetriesForCandidate(candidate)
	for _, upstreamModel := range models {
		if m.executionResetChanged(signal) {
			return cliproxyexecutor.Response{}, errExecutionReset
		}
		resultModel := m.stateModelForExecution(candidate.auth, routeModel, upstreamModel, pooled)
		execReq := req
		execReq.Model = upstreamModel
		for attempt := 0; attempt < providerRetries; attempt++ {
			if m.executionResetChanged(signal) {
				return cliproxyexecutor.Response{}, errExecutionReset
			}
			resp, errExec := candidate.executor.CountTokens(execCtx, candidate.auth, execReq, opts)
			result := Result{AuthID: candidate.auth.ID, AuthGeneration: candidate.auth.RuntimeGeneration, Provider: candidate.provider, Model: resultModel, Success: errExec == nil}
			if errExec != nil {
				if errCtx := execCtx.Err(); errCtx != nil {
					return cliproxyexecutor.Response{}, errCtx
				}
				result.Error = &Error{Message: errExec.Error()}
				if se, ok := errors.AsType[cliproxyexecutor.StatusError](errExec); ok && se != nil {
					result.Error.HTTPStatus = se.StatusCode()
				}
				result.RetryAfter = retryAfterFromError(errExec)
				m.MarkResult(execCtx, result)
				lastErr = errExec
				if m.executionResetChanged(signal) {
					return cliproxyexecutor.Response{}, errExecutionReset
				}
				if !isRetryableUpstreamError(errExec) {
					break
				}
				continue
			}
			m.MarkResult(execCtx, result)
			return resp, nil
		}
	}
	if lastErr == nil {
		lastErr = &Error{Code: "auth_not_found", Message: "no upstream model available"}
	}
	return cliproxyexecutor.Response{}, lastErr
}

func (m *Manager) executeStreamRetryCandidate(ctx context.Context, candidate retryCandidate, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, signal executionResetSignal) (*cliproxyexecutor.StreamResult, error) {
	if candidate.executor == nil || candidate.auth == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	entry := logEntryWithRequestID(ctx)
	debugLogAuthSelection(entry, candidate.auth, candidate.provider, req.Model)
	publishSelectedAuthMetadata(opts.Metadata, candidate.auth.ID)

	execCtx := m.executionContextForAuth(ctx, candidate.auth)
	routeModel := req.Model
	models, pooled := m.preparedRetryExecutionModels(candidate.auth, routeModel)
	if len(models) == 0 {
		return nil, &Error{Code: "auth_unavailable", Message: "no upstream model available"}
	}

	var streamResult *cliproxyexecutor.StreamResult
	var errStream error
	providerRetries := m.providerRetriesForCandidate(candidate)
	for attempt := 0; attempt < providerRetries; attempt++ {
		if m.executionResetChanged(signal) {
			return nil, errExecutionReset
		}
		streamResult, errStream = m.executeStreamWithModelPool(execCtx, candidate.executor, candidate.auth, candidate.provider, req, opts, routeModel, models, pooled, signal)
		if errors.Is(errStream, errExecutionReset) {
			return nil, errExecutionReset
		}
		if errStream == nil || !isRetryableUpstreamError(errStream) {
			break
		}
	}
	if errStream != nil {
		if errCtx := execCtx.Err(); errCtx != nil {
			return nil, errCtx
		}
		return nil, errStream
	}
	return streamResult, nil
}

func (m *Manager) executionContextForAuth(ctx context.Context, auth *Auth) context.Context {
	execCtx := ctx
	if rt := m.roundTripperFor(auth); rt != nil {
		execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
		execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
	}
	return execCtx
}

func (m *Manager) preparedRetryExecutionModels(auth *Auth, routeModel string) ([]string, bool) {
	candidates := m.executionModelCandidates(auth, routeModel)
	pooled := len(candidates) > 1
	if isConfigProviderRetryCandidate(auth) {
		return candidates, pooled
	}
	models := m.filterExecutionModels(auth, routeModel, candidates, pooled)
	if len(models) > 1 {
		models = models[:1]
	}
	return models, pooled
}

func (m *Manager) providerRetriesForCandidate(candidate retryCandidate) int {
	if candidate.authFile {
		return 1
	}
	policy := m.effectiveErrorControlPolicy(candidate.provider, candidate.auth)
	if policy.providerRetries < 1 {
		return 1
	}
	return policy.providerRetries
}

func ensureRequestedModelMetadata(opts cliproxyexecutor.Options, requestedModel string) cliproxyexecutor.Options {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return opts
	}
	if hasRequestedModelMetadata(opts.Metadata) {
		return opts
	}
	if len(opts.Metadata) == 0 {
		opts.Metadata = map[string]any{cliproxyexecutor.RequestedModelMetadataKey: requestedModel}
		return opts
	}
	meta := make(map[string]any, len(opts.Metadata)+1)
	for k, v := range opts.Metadata {
		meta[k] = v
	}
	meta[cliproxyexecutor.RequestedModelMetadataKey] = requestedModel
	opts.Metadata = meta
	return opts
}

func hasRequestedModelMetadata(meta map[string]any) bool {
	if len(meta) == 0 {
		return false
	}
	raw, ok := meta[cliproxyexecutor.RequestedModelMetadataKey]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v) != ""
	case []byte:
		return strings.TrimSpace(string(v)) != ""
	default:
		return false
	}
}

func cloneTriedMap(tried map[string]struct{}) map[string]struct{} {
	if len(tried) == 0 {
		return make(map[string]struct{})
	}
	cloned := make(map[string]struct{}, len(tried))
	for authID := range tried {
		cloned[authID] = struct{}{}
	}
	return cloned
}

func shouldBypassCooldownForSelection(auth *Auth, routeModel, selectionModel string) bool {
	if auth == nil {
		return false
	}
	checkModel := strings.TrimSpace(selectionModel)
	if checkModel == "" {
		checkModel = strings.TrimSpace(routeModel)
	}
	if checkModel == "" {
		return false
	}
	blocked, reason, next := isAuthBlockedForModel(auth, checkModel, time.Now())
	return blocked && reason != blockReasonDisabled && !next.IsZero()
}

func (m *Manager) shouldExcludeAuthForRequest(auth *Auth, routeModel string) bool {
	if auth == nil {
		return false
	}
	if isAPIKeyAuth(auth) {
		return false
	}
	authToCheck := auth
	if m != nil {
		m.mu.RLock()
		if current := m.auths[auth.ID]; current != nil {
			authToCheck = current
		}
		m.mu.RUnlock()
	}
	checkModel := strings.TrimSpace(routeModel)
	if m != nil {
		checkModel = strings.TrimSpace(m.selectionModelForAuth(authToCheck, routeModel))
	}
	if checkModel == "" {
		checkModel = strings.TrimSpace(routeModel)
	}
	blocked, reason, next := isAuthBlockedForModel(authToCheck, checkModel, time.Now())
	return blocked && reason != blockReasonDisabled && !next.IsZero()
}

func pinnedAuthIDFromMetadata(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	raw, ok := meta[cliproxyexecutor.PinnedAuthMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch val := raw.(type) {
	case string:
		return strings.TrimSpace(val)
	case []byte:
		return strings.TrimSpace(string(val))
	default:
		return ""
	}
}

func publishSelectedAuthMetadata(meta map[string]any, authID string) {
	if len(meta) == 0 {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	meta[cliproxyexecutor.SelectedAuthMetadataKey] = authID
	if callback, ok := meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey].(func(string)); ok && callback != nil {
		callback(authID)
	}
}

func rewriteModelForAuth(model string, auth *Auth) string {
	if auth == nil || model == "" {
		return model
	}
	prefix := strings.TrimSpace(auth.Prefix)
	if prefix == "" {
		return model
	}
	needle := prefix + "/"
	if !strings.HasPrefix(model, needle) {
		return model
	}
	return strings.TrimPrefix(model, needle)
}

func (m *Manager) applyAPIKeyModelAlias(auth *Auth, requestedModel string) string {
	if m == nil || auth == nil {
		return requestedModel
	}

	kind, _ := auth.AccountInfo()
	if !strings.EqualFold(strings.TrimSpace(kind), "api_key") {
		return requestedModel
	}

	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return requestedModel
	}

	// Fast path: lookup per-auth mapping table (keyed by auth.ID).
	if resolved := m.lookupAPIKeyUpstreamModel(auth.ID, requestedModel); resolved != "" {
		return resolved
	}

	// Slow path: scan config for the matching credential entry and resolve alias.
	// This acts as a safety net if mappings are stale or auth.ID is missing.
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}

	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	upstreamModel := ""
	switch provider {
	case "gemini":
		upstreamModel = resolveUpstreamModelForGeminiAPIKey(cfg, auth, requestedModel)
	case "claude":
		upstreamModel = resolveUpstreamModelForClaudeAPIKey(cfg, auth, requestedModel)
	case "codex":
		upstreamModel = resolveUpstreamModelForCodexAPIKey(cfg, auth, requestedModel)
	case "vertex":
		upstreamModel = resolveUpstreamModelForVertexAPIKey(cfg, auth, requestedModel)
	default:
		upstreamModel = resolveUpstreamModelForOpenAICompatAPIKey(cfg, auth, requestedModel)
	}

	// Return upstream model if found, otherwise return requested model.
	if upstreamModel != "" {
		return upstreamModel
	}
	return requestedModel
}

// APIKeyConfigEntry is a generic interface for API key configurations.
type APIKeyConfigEntry interface {
	GetAPIKey() string
	GetBaseURL() string
}

func resolveAPIKeyConfig[T APIKeyConfigEntry](entries []T, auth *Auth) *T {
	if auth == nil || len(entries) == 0 {
		return nil
	}
	attrKey, attrBase := "", ""
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range entries {
		entry := &entries[i]
		cfgKey := strings.TrimSpace((*entry).GetAPIKey())
		cfgBase := strings.TrimSpace((*entry).GetBaseURL())
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range entries {
			entry := &entries[i]
			if strings.EqualFold(strings.TrimSpace((*entry).GetAPIKey()), attrKey) {
				return entry
			}
		}
	}
	return nil
}

func resolveGeminiAPIKeyConfig(cfg *internalconfig.Config, auth *Auth) *internalconfig.GeminiKey {
	if cfg == nil {
		return nil
	}
	return resolveAPIKeyConfig(cfg.GeminiKey, auth)
}

func resolveClaudeAPIKeyConfig(cfg *internalconfig.Config, auth *Auth) *internalconfig.ClaudeKey {
	if cfg == nil {
		return nil
	}
	return resolveAPIKeyConfig(cfg.ClaudeKey, auth)
}

func resolveCodexAPIKeyConfig(cfg *internalconfig.Config, auth *Auth) *internalconfig.CodexKey {
	if cfg == nil {
		return nil
	}
	return resolveAPIKeyConfig(cfg.CodexKey, auth)
}

func resolveVertexAPIKeyConfig(cfg *internalconfig.Config, auth *Auth) *internalconfig.VertexCompatKey {
	if cfg == nil {
		return nil
	}
	return resolveAPIKeyConfig(cfg.VertexCompatAPIKey, auth)
}

func resolveUpstreamModelForGeminiAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	entry := resolveGeminiAPIKeyConfig(cfg, auth)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelForClaudeAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	entry := resolveClaudeAPIKeyConfig(cfg, auth)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelForCodexAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	entry := resolveCodexAPIKeyConfig(cfg, auth)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelForVertexAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	entry := resolveVertexAPIKeyConfig(cfg, auth)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelForOpenAICompatAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	providerKey := ""
	compatName := ""
	if auth != nil && len(auth.Attributes) > 0 {
		providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	if compatName == "" && !strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		return ""
	}
	entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, auth.Provider)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

type apiKeyModelAliasTable map[string]map[string]string

func resolveOpenAICompatConfig(cfg *internalconfig.Config, providerKey, compatName, authProvider string) *internalconfig.OpenAICompatibility {
	if cfg == nil {
		return nil
	}
	candidates := make([]string, 0, 3)
	if v := strings.TrimSpace(compatName); v != "" {
		candidates = append(candidates, v)
	}
	if v := strings.TrimSpace(providerKey); v != "" {
		candidates = append(candidates, v)
	}
	if v := strings.TrimSpace(authProvider); v != "" {
		candidates = append(candidates, v)
	}
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

func asModelAliasEntries[T interface {
	GetName() string
	GetAlias() string
}](models []T) []modelAliasEntry {
	if len(models) == 0 {
		return nil
	}
	out := make([]modelAliasEntry, 0, len(models))
	for i := range models {
		out = append(out, models[i])
	}
	return out
}

func (m *Manager) normalizeProviders(providers []string) []string {
	if len(providers) == 0 {
		return nil
	}
	result := make([]string, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		p := strings.TrimSpace(strings.ToLower(provider))
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		result = append(result, p)
	}
	return result
}

type retryCandidate struct {
	auth     *Auth
	executor ProviderExecutor
	provider string
	authFile bool
	priority int
}

func isAuthFileRetryCandidate(auth *Auth) bool {
	if auth == nil {
		return false
	}
	return !isConfigProviderRetryCandidate(auth)
}

func isConfigProviderRetryCandidate(auth *Auth) bool {
	if auth == nil {
		return false
	}
	source := ""
	if len(auth.Attributes) > 0 {
		source = strings.ToLower(strings.TrimSpace(auth.Attributes["source"]))
	}
	if strings.TrimSpace(auth.FileName) != "" || strings.HasPrefix(source, "file:") {
		return false
	}
	if strings.HasPrefix(source, "config:") {
		return true
	}
	if auth.Attributes != nil && strings.TrimSpace(auth.Attributes["config_provider"]) != "" {
		return true
	}
	return isAPIKeyAuth(auth)
}

func (m *Manager) retryCandidates(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options) ([]retryCandidate, error) {
	if m == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key != "" {
			providerSet[key] = struct{}{}
		}
	}
	if len(providerSet) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	preferredProvider := ""
	if pinnedAuthID == "" {
		preferredProvider = m.preferredProviderForRequest(providers, opts.Metadata)
	}

	now := time.Now()
	registryRef := registry.GetGlobalRegistry()
	candidates := make([]retryCandidate, 0)
	totalAuthFiles := 0
	blockedAuthFiles := 0
	cooldownAuthFiles := 0
	earliestAuthFileRetry := time.Time{}

	m.mu.RLock()
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled || !auth.GroupEnabled() {
			continue
		}
		if pinnedAuthID != "" && auth.ID != pinnedAuthID {
			continue
		}
		providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		executor, okExecutor := m.executors[providerKey]
		if !okExecutor || executor == nil {
			continue
		}
		if strings.TrimSpace(model) != "" && !m.authSupportsRouteModel(registryRef, auth, model) {
			continue
		}

		authFile := isAuthFileRetryCandidate(auth)
		if authFile {
			totalAuthFiles++
			checkModel := m.selectionModelForAuth(auth, model)
			blocked, reason, next := isAuthBlockedForModel(auth, checkModel, now)
			if blocked {
				if reason == blockReasonCooldown {
					cooldownAuthFiles++
					if !next.IsZero() && (earliestAuthFileRetry.IsZero() || next.Before(earliestAuthFileRetry)) {
						earliestAuthFileRetry = next
					}
				} else if reason != blockReasonDisabled {
					blockedAuthFiles++
				}
				continue
			}
		}

		authCopy := auth.Clone()
		if !authCopy.indexAssigned {
			authCopy.EnsureIndex()
		}
		candidates = append(candidates, retryCandidate{
			auth:     authCopy,
			executor: executor,
			provider: providerKey,
			authFile: authFile,
			priority: authPriority(auth),
		})
	}
	m.mu.RUnlock()

	if len(candidates) == 0 {
		if totalAuthFiles > 0 && cooldownAuthFiles == totalAuthFiles && !earliestAuthFileRetry.IsZero() {
			resetIn := earliestAuthFileRetry.Sub(now)
			if resetIn < 0 {
				resetIn = 0
			}
			return nil, newModelCooldownError(model, "", resetIn)
		}
		if totalAuthFiles > 0 && cooldownAuthFiles+blockedAuthFiles == totalAuthFiles {
			return nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
		}
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}

	shuffleRetryCandidates(candidates)
	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		if preferredProvider != "" {
			leftPreferred := left.provider == preferredProvider
			rightPreferred := right.provider == preferredProvider
			if leftPreferred != rightPreferred {
				return leftPreferred
			}
		}
		if left.priority != right.priority {
			return left.priority > right.priority
		}
		return false
	})

	if cliproxyexecutor.DownstreamWebsocket(ctx) {
		sort.SliceStable(candidates, func(i, j int) bool {
			left := candidates[i]
			right := candidates[j]
			return authWebsocketsEnabled(left.auth) && !authWebsocketsEnabled(right.auth)
		})
	}

	return candidates, nil
}

func roundCandidates(candidates []retryCandidate, round int) []retryCandidate {
	if round <= 0 {
		return candidates
	}
	out := make([]retryCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.authFile {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func retryRoundWait(roundStart time.Time, targetInterval time.Duration) time.Duration {
	if targetInterval <= 0 {
		return 0
	}
	elapsed := time.Since(roundStart)
	if elapsed >= targetInterval {
		return 0
	}
	return targetInterval - elapsed
}

func (m *Manager) retrySettings() (int, time.Duration) {
	if m == nil {
		return 0, 0
	}
	return int(m.requestRetry.Load()), time.Duration(m.maxRetryInterval.Load())
}

type errorControlPolicy struct {
	providerRetries      int
	retryRounds          int
	roundBackoffBase     float64
	roundBackoffExponent float64
	roundBackoffMax      float64
}

func (m *Manager) effectiveErrorControlPolicy(provider string, auth *Auth) errorControlPolicy {
	policy := errorControlPolicy{
		providerRetries:      1,
		retryRounds:          1,
		roundBackoffBase:     1.0,
		roundBackoffExponent: 2.0,
		roundBackoffMax:      60.0,
	}
	if m != nil {
		if raw := m.errorControl.Load(); raw != nil {
			if cfg, ok := raw.(internalconfig.ErrorControlConfig); ok {
				applyErrorControlPolicy(&policy, cfg.Default)
				providerKey := strings.ToLower(strings.TrimSpace(provider))
				if providerKey == "" && auth != nil {
					providerKey = strings.ToLower(strings.TrimSpace(auth.Provider))
				}
				if providerKey != "" && len(cfg.Providers) > 0 {
					if providerPolicy, okProvider := cfg.Providers[providerKey]; okProvider {
						applyErrorControlPolicy(&policy, providerPolicy)
					}
				}
			}
		}
	}
	if auth != nil {
		if retries, ok := auth.ProviderRetriesOverride(); ok {
			policy.providerRetries = retries
		}
		if rounds, ok := auth.RetryRoundsOverride(); ok {
			policy.retryRounds = rounds
		}
		if v, ok := auth.RoundBackoffBaseOverride(); ok {
			policy.roundBackoffBase = v
		}
		if v, ok := auth.RoundBackoffExponentOverride(); ok {
			policy.roundBackoffExponent = v
		}
		if v, ok := auth.RoundBackoffMaxOverride(); ok {
			policy.roundBackoffMax = v
		}
	}
	if policy.providerRetries < 1 {
		policy.providerRetries = 1
	}
	if policy.retryRounds < 1 {
		policy.retryRounds = 1
	}
	return policy
}

func (m *Manager) maxRetryRounds(providers []string) int {
	rounds := 1
	if m == nil {
		return rounds
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key != "" {
			providerSet[key] = struct{}{}
		}
	}
	if raw := m.errorControl.Load(); raw != nil {
		if cfg, ok := raw.(internalconfig.ErrorControlConfig); ok {
			if cfg.Default.RetryRounds != nil && *cfg.Default.RetryRounds > rounds {
				rounds = *cfg.Default.RetryRounds
			}
			for provider, policy := range cfg.Providers {
				key := strings.ToLower(strings.TrimSpace(provider))
				if key == "" {
					continue
				}
				if len(providerSet) > 0 {
					if _, okProvider := providerSet[key]; !okProvider {
						continue
					}
				}
				if policy.RetryRounds != nil && *policy.RetryRounds > rounds {
					rounds = *policy.RetryRounds
				}
			}
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
		if len(providerSet) > 0 {
			if _, okProvider := providerSet[providerKey]; !okProvider {
				continue
			}
		}
		if authRounds, ok := auth.RetryRoundsOverride(); ok && authRounds > rounds {
			rounds = authRounds
		}
	}
	if rounds < 1 {
		rounds = 1
	}
	return rounds
}

func applyErrorControlPolicy(target *errorControlPolicy, policy internalconfig.ErrorControlPolicy) {
	if target == nil {
		return
	}
	if policy.ProviderRetries != nil {
		target.providerRetries = *policy.ProviderRetries
	}
	if policy.RetryRounds != nil {
		target.retryRounds = *policy.RetryRounds
	}
	if policy.RoundBackoffBase != nil {
		target.roundBackoffBase = *policy.RoundBackoffBase
	}
	if policy.RoundBackoffExponent != nil {
		target.roundBackoffExponent = *policy.RoundBackoffExponent
	}
	if policy.RoundBackoffMax != nil {
		target.roundBackoffMax = *policy.RoundBackoffMax
	}
}

// computeRoundBackoff calculates the exponential backoff duration for a given retry round.
// Formula: min(base * exponent^(round-1), max)
func computeRoundBackoff(round int, base, exponent, max float64) time.Duration {
	if round <= 0 || base <= 0 {
		return 0
	}
	wait := base * math.Pow(exponent, float64(round-1))
	if wait > max {
		wait = max
	}
	return time.Duration(wait * float64(time.Second))
}

// roundBackoffPolicy returns the effective round backoff parameters from the global error-control config.
func (m *Manager) roundBackoffPolicy(providers []string) (base, exponent, max float64) {
	base, exponent, max = 1.0, 2.0, 60.0
	if m == nil {
		return
	}
	if raw := m.errorControl.Load(); raw != nil {
		if cfg, ok := raw.(internalconfig.ErrorControlConfig); ok {
			if cfg.Default.RoundBackoffBase != nil {
				base = *cfg.Default.RoundBackoffBase
			}
			if cfg.Default.RoundBackoffExponent != nil {
				exponent = *cfg.Default.RoundBackoffExponent
			}
			if cfg.Default.RoundBackoffMax != nil {
				max = *cfg.Default.RoundBackoffMax
			}
			providerSet := make(map[string]struct{}, len(providers))
			for _, p := range providers {
				key := strings.ToLower(strings.TrimSpace(p))
				if key != "" {
					providerSet[key] = struct{}{}
				}
			}
			for provider, policy := range cfg.Providers {
				key := strings.ToLower(strings.TrimSpace(provider))
				if _, ok := providerSet[key]; !ok {
					continue
				}
				if policy.RoundBackoffBase != nil {
					base = *policy.RoundBackoffBase
				}
				if policy.RoundBackoffExponent != nil {
					exponent = *policy.RoundBackoffExponent
				}
				if policy.RoundBackoffMax != nil {
					max = *policy.RoundBackoffMax
				}
			}
		}
	}
	return
}

func (m *Manager) closestCooldownWait(providers []string, model string, attempt int) (time.Duration, bool) {
	return m.closestCooldownWaitWithTried(providers, model, attempt, nil)
}

func (m *Manager) closestCooldownWaitWithTried(providers []string, model string, attempt int, tried map[string]struct{}) (time.Duration, bool) {
	if m == nil || len(providers) == 0 {
		return 0, false
	}
	now := time.Now()
	defaultRetry := int(m.requestRetry.Load())
	if defaultRetry < 0 {
		defaultRetry = 0
	}
	providerSet := make(map[string]struct{}, len(providers))
	for i := range providers {
		key := strings.TrimSpace(strings.ToLower(providers[i]))
		if key == "" {
			continue
		}
		providerSet[key] = struct{}{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var (
		found   bool
		minWait time.Duration
	)
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		if _, alreadyTried := tried[auth.ID]; alreadyTried {
			continue
		}
		providerKey := strings.TrimSpace(strings.ToLower(auth.Provider))
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		effectiveRetry := defaultRetry
		if override, ok := auth.RequestRetryOverride(); ok {
			effectiveRetry = override
		}
		if effectiveRetry < 0 {
			effectiveRetry = 0
		}
		if attempt >= effectiveRetry {
			continue
		}
		checkModel := model
		if strings.TrimSpace(model) != "" {
			checkModel = m.selectionModelForAuth(auth, model)
		}
		blocked, reason, next := isAuthBlockedForModel(auth, checkModel, now)
		if !blocked || next.IsZero() || reason == blockReasonDisabled {
			continue
		}
		wait := next.Sub(now)
		if wait < 0 {
			continue
		}
		if !found || wait < minWait {
			minWait = wait
			found = true
		}
	}
	return minWait, found
}

func (m *Manager) retryAllowed(attempt int, providers []string) bool {
	return m.retryAllowedWithTried(attempt, providers, nil)
}

func (m *Manager) retryAllowedWithTried(attempt int, providers []string, tried map[string]struct{}) bool {
	if m == nil || len(providers) == 0 {
		return false
	}
	providerSet := make(map[string]struct{}, len(providers))
	for i := range providers {
		key := strings.TrimSpace(strings.ToLower(providers[i]))
		if key == "" {
			continue
		}
		providerSet[key] = struct{}{}
	}
	if len(providerSet) == 0 {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		if _, alreadyTried := tried[auth.ID]; alreadyTried {
			continue
		}
		providerKey := strings.TrimSpace(strings.ToLower(auth.Provider))
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		if !isAPIKeyAuth(auth) {
			continue
		}
		return true
	}
	return false
}

func (m *Manager) shouldRetryAfterError(err error, attempt int, providers []string, model string, maxWait time.Duration) (time.Duration, bool) {
	return m.shouldRetryAfterErrorWithTried(err, attempt, providers, model, maxWait, nil)
}

func (m *Manager) shouldRetryAfterErrorWithTried(err error, attempt int, providers []string, model string, maxWait time.Duration, tried map[string]struct{}) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	if m.hasUntriedAPIKeyAuth(providers, model, tried) {
		return 0, true
	}
	if maxWait <= 0 {
		return 0, false
	}
	wait, found := m.closestCooldownWaitWithTried(providers, model, attempt, tried)
	if found && wait <= maxWait {
		return wait, true
	}
	return 0, false
}

func (m *Manager) hasUntriedAPIKeyAuth(providers []string, model string, excluded map[string]struct{}) bool {
	if m == nil || len(providers) == 0 {
		return false
	}
	providerSet := make(map[string]struct{}, len(providers))
	for i := range providers {
		key := strings.TrimSpace(strings.ToLower(providers[i]))
		if key != "" {
			providerSet[key] = struct{}{}
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		if _, alreadyTried := excluded[auth.ID]; alreadyTried {
			continue
		}
		providerKey := strings.TrimSpace(strings.ToLower(auth.Provider))
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		if !isAPIKeyAuth(auth) {
			continue
		}
		if strings.TrimSpace(model) != "" && !m.authSupportsRouteModel(registry.GetGlobalRegistry(), auth, model) {
			continue
		}
		checkModel := strings.TrimSpace(model)
		if checkModel != "" {
			checkModel = strings.TrimSpace(m.selectionModelForAuth(auth, model))
		}
		blocked, reason, _ := isAuthBlockedForModel(auth, checkModel, time.Now())
		if blocked || reason == blockReasonDisabled {
			continue
		}
		return true
	}
	return false
}

func mergeTriedMaps(a, b map[string]struct{}) map[string]struct{} {
	merged := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		merged[k] = struct{}{}
	}
	for k := range b {
		merged[k] = struct{}{}
	}
	return merged
}

func waitForCooldown(ctx context.Context, wait time.Duration, signal executionResetSignal) (bool, error) {
	if wait <= 0 {
		return false, nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-signal.ch:
		return true, nil
	case <-timer.C:
		return false, nil
	}
}

// MarkResult records an execution result and notifies hooks.
func (m *Manager) MarkResult(ctx context.Context, result Result) {
	if result.AuthID == "" {
		return
	}

	shouldResumeModel := false
	shouldSuspendModel := false
	suspendReason := ""
	clearModelQuota := false
	setModelQuota := false
	var authSnapshot *Auth

	m.mu.Lock()
	if auth, ok := m.auths[result.AuthID]; ok && auth != nil {
		if result.AuthGeneration != 0 && auth.RuntimeGeneration != 0 && result.AuthGeneration != auth.RuntimeGeneration {
			m.mu.Unlock()
			return
		}
		now := time.Now()

		if result.Success {
			if result.Model != "" {
				if state := auth.ModelStates[result.Model]; state != nil {
					resetModelState(state, now)
					if modelStateIsClean(state) {
						delete(auth.ModelStates, result.Model)
					}
				}
				if len(auth.ModelStates) == 0 {
					auth.ModelStates = nil
				}
				updateAggregatedAvailability(auth, now)
				if !hasModelError(auth, now) {
					auth.LastError = nil
					auth.StatusMessage = ""
					auth.Status = StatusActive
				}
				auth.UpdatedAt = now
				shouldResumeModel = true
				clearModelQuota = true
			} else {
				clearAuthStateOnSuccess(auth, now)
			}
		} else {
			if result.Model != "" {
				if !isRequestScopedNotFoundResultError(result.Error) {
					state := ensureModelState(auth, result.Model)
					if isConfigManagedAIProvider(auth) {
						applyConfigProviderModelFailureState(auth, state, result.Error, now)
					} else {
						disableCooling := quotaCooldownDisabledForAuth(auth)
						state.Unavailable = true
						state.Status = StatusError
						state.UpdatedAt = now
						if result.Error != nil {
							state.LastError = cloneError(result.Error)
							state.StatusMessage = result.Error.Message
							auth.LastError = cloneError(result.Error)
							auth.StatusMessage = result.Error.Message
						}

						statusCode := statusCodeFromResult(result.Error)
						if isModelSupportResultError(result.Error) {
							next := now.Add(12 * time.Hour)
							state.NextRetryAfter = next
							suspendReason = "model_not_supported"
							shouldSuspendModel = true
						} else {
							switch statusCode {
							case 401:
								if disableCooling {
									state.NextRetryAfter = time.Time{}
								} else {
									next := now.Add(30 * time.Minute)
									state.NextRetryAfter = next
									suspendReason = "unauthorized"
									shouldSuspendModel = true
								}
							case 402, 403:
								if disableCooling {
									state.NextRetryAfter = time.Time{}
								} else {
									next := now.Add(30 * time.Minute)
									state.NextRetryAfter = next
									suspendReason = "payment_required"
									shouldSuspendModel = true
								}
							case 404:
								if disableCooling {
									state.NextRetryAfter = time.Time{}
								} else {
									next := now.Add(12 * time.Hour)
									state.NextRetryAfter = next
									suspendReason = "not_found"
									shouldSuspendModel = true
								}
							case 429:
								var next time.Time
								backoffLevel := state.Quota.BackoffLevel
								if !disableCooling {
									if result.RetryAfter != nil {
										next = now.Add(*result.RetryAfter)
									} else {
										cooldown, nextLevel := nextQuotaCooldown(backoffLevel, disableCooling)
										if cooldown > 0 {
											next = now.Add(cooldown)
										}
										backoffLevel = nextLevel
									}
								}
								state.NextRetryAfter = next
								state.Quota = QuotaState{
									Exceeded:      true,
									Reason:        "quota",
									NextRecoverAt: next,
									BackoffLevel:  backoffLevel,
								}
								if !disableCooling {
									suspendReason = "quota"
									shouldSuspendModel = true
									setModelQuota = true
								}
							case 408, 500, 502, 503, 504:
								if disableCooling {
									state.NextRetryAfter = time.Time{}
								} else {
									next := now.Add(1 * time.Minute)
									state.NextRetryAfter = next
								}
							default:
								state.NextRetryAfter = time.Time{}
							}
						}
					}

					auth.Status = StatusError
					auth.UpdatedAt = now
					updateAggregatedAvailability(auth, now)
				}
			} else {
				applyAuthFailureState(auth, result.Error, result.RetryAfter, now)
			}
		}

		_ = m.persist(ctx, auth)
		authSnapshot = auth.Clone()
	}
	m.mu.Unlock()
	if m.scheduler != nil && authSnapshot != nil {
		m.scheduler.upsertAuth(authSnapshot)
	}

	if clearModelQuota && result.Model != "" {
		registry.GetGlobalRegistry().ClearModelQuotaExceeded(result.AuthID, result.Model)
	}
	if setModelQuota && result.Model != "" {
		registry.GetGlobalRegistry().SetModelQuotaExceeded(result.AuthID, result.Model)
	}
	if shouldResumeModel {
		registry.GetGlobalRegistry().ResumeClientModel(result.AuthID, result.Model)
	} else if shouldSuspendModel {
		registry.GetGlobalRegistry().SuspendClientModel(result.AuthID, result.Model, suspendReason)
	}

	m.hook.OnResult(ctx, result)
}

func ensureModelState(auth *Auth, model string) *ModelState {
	if auth == nil || model == "" {
		return nil
	}
	if auth.ModelStates == nil {
		auth.ModelStates = make(map[string]*ModelState)
	}
	if state, ok := auth.ModelStates[model]; ok && state != nil {
		return state
	}
	state := &ModelState{Status: StatusActive}
	auth.ModelStates[model] = state
	return state
}

func resetModelState(state *ModelState, now time.Time) {
	if state == nil {
		return
	}
	state.Unavailable = false
	state.Status = StatusActive
	state.StatusMessage = ""
	state.NextRetryAfter = time.Time{}
	state.LastError = nil
	state.Quota = QuotaState{}
	state.UpdatedAt = now
}

func modelStateIsClean(state *ModelState) bool {
	if state == nil {
		return true
	}
	if state.Status != StatusActive {
		return false
	}
	if state.Unavailable || state.StatusMessage != "" || !state.NextRetryAfter.IsZero() || state.LastError != nil {
		return false
	}
	if state.Quota.Exceeded || state.Quota.Reason != "" || !state.Quota.NextRecoverAt.IsZero() || state.Quota.BackoffLevel != 0 {
		return false
	}
	return true
}

func updateAggregatedAvailability(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	if len(auth.ModelStates) == 0 {
		clearAggregatedAvailability(auth)
		return
	}
	allUnavailable := true
	earliestRetry := time.Time{}
	quotaExceeded := false
	quotaRecover := time.Time{}
	maxBackoffLevel := 0
	hasState := false
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		hasState = true
		stateUnavailable := false
		if state.Status == StatusDisabled {
			stateUnavailable = true
		} else if state.Unavailable {
			if state.NextRetryAfter.IsZero() {
				stateUnavailable = false
			} else if state.NextRetryAfter.After(now) {
				stateUnavailable = true
				if earliestRetry.IsZero() || state.NextRetryAfter.Before(earliestRetry) {
					earliestRetry = state.NextRetryAfter
				}
			} else {
				state.Unavailable = false
				state.NextRetryAfter = time.Time{}
			}
		}
		if !stateUnavailable {
			allUnavailable = false
		}
		if state.Quota.Exceeded {
			quotaExceeded = true
			if quotaRecover.IsZero() || (!state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.Before(quotaRecover)) {
				quotaRecover = state.Quota.NextRecoverAt
			}
			if state.Quota.BackoffLevel > maxBackoffLevel {
				maxBackoffLevel = state.Quota.BackoffLevel
			}
		}
	}
	if !hasState {
		clearAggregatedAvailability(auth)
		return
	}
	auth.Unavailable = allUnavailable
	if allUnavailable {
		auth.NextRetryAfter = earliestRetry
	} else {
		auth.NextRetryAfter = time.Time{}
	}
	if quotaExceeded {
		auth.Quota.Exceeded = true
		auth.Quota.Reason = "quota"
		auth.Quota.NextRecoverAt = quotaRecover
		auth.Quota.BackoffLevel = maxBackoffLevel
	} else {
		auth.Quota.Exceeded = false
		auth.Quota.Reason = ""
		auth.Quota.NextRecoverAt = time.Time{}
		auth.Quota.BackoffLevel = 0
	}
}

func clearAggregatedAvailability(auth *Auth) {
	if auth == nil {
		return
	}
	auth.Unavailable = false
	auth.NextRetryAfter = time.Time{}
	auth.Quota = QuotaState{}
}

func hasModelError(auth *Auth, now time.Time) bool {
	if auth == nil || len(auth.ModelStates) == 0 {
		return false
	}
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.LastError != nil {
			return true
		}
		if state.Status == StatusError {
			if state.Unavailable && (state.NextRetryAfter.IsZero() || state.NextRetryAfter.After(now)) {
				return true
			}
		}
	}
	return false
}

func clearAuthStateOnSuccess(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	auth.Unavailable = false
	auth.Status = StatusActive
	auth.StatusMessage = ""
	auth.Quota.Exceeded = false
	auth.Quota.Reason = ""
	auth.Quota.NextRecoverAt = time.Time{}
	auth.Quota.BackoffLevel = 0
	auth.LastError = nil
	auth.NextRetryAfter = time.Time{}
	auth.UpdatedAt = now
}

func cloneError(err *Error) *Error {
	if err == nil {
		return nil
	}
	return &Error{
		Code:       err.Code,
		Message:    err.Message,
		Retryable:  err.Retryable,
		HTTPStatus: err.HTTPStatus,
	}
}

func statusCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	type statusCoder interface {
		StatusCode() int
	}
	var sc statusCoder
	if errors.As(err, &sc) && sc != nil {
		return sc.StatusCode()
	}
	return 0
}

func retryAfterFromError(err error) *time.Duration {
	if err == nil {
		return nil
	}
	type retryAfterProvider interface {
		RetryAfter() *time.Duration
	}
	rap, ok := err.(retryAfterProvider)
	if !ok || rap == nil {
		return nil
	}
	retryAfter := rap.RetryAfter()
	if retryAfter == nil {
		return nil
	}
	return new(*retryAfter)
}

func statusCodeFromResult(err *Error) int {
	if err == nil {
		return 0
	}
	return err.StatusCode()
}

func newUpstreamExhaustedError(lastErr error) error {
	var authErr *Error
	if errors.As(lastErr, &authErr) && authErr != nil {
		switch authErr.Code {
		case "auth_not_found", "auth_unavailable", "provider_not_found", "executor_not_found":
			return lastErr
		}
	}
	status := statusCodeFromError(lastErr)
	if status <= 0 {
		status = http.StatusBadGateway
	}
	return &Error{
		Code:       "upstream_exhausted",
		Message:    "Upstream providers exhausted after retries",
		Retryable:  true,
		HTTPStatus: status,
	}
}

func isRetryableUpstreamError(err error) bool {
	if err == nil {
		return false
	}
	if isRequestInvalidError(err) {
		return false
	}
	status := statusCodeFromError(err)
	if status == 0 {
		return true
	}
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func isModelSupportErrorMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	patterns := [...]string{
		"model_not_supported",
		"requested model is not supported",
		"requested model is unsupported",
		"requested model is unavailable",
		"model is not supported",
		"model not supported",
		"unsupported model",
		"model unavailable",
		"not available for your plan",
		"not available for your account",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func isModelSupportError(err error) bool {
	if err == nil {
		return false
	}
	status := statusCodeFromError(err)
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return false
	}
	return isModelSupportErrorMessage(err.Error())
}

func isModelSupportResultError(err *Error) bool {
	if err == nil {
		return false
	}
	status := statusCodeFromResult(err)
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return false
	}
	return isModelSupportErrorMessage(err.Message)
}

func isRequestScopedNotFoundMessage(message string) bool {
	if message == "" {
		return false
	}
	lower := strings.ToLower(message)
	return strings.Contains(lower, "item with id") &&
		strings.Contains(lower, "not found") &&
		strings.Contains(lower, "items are not persisted when `store` is set to false")
}

func isRequestScopedNotFoundResultError(err *Error) bool {
	if err == nil || statusCodeFromResult(err) != http.StatusNotFound {
		return false
	}
	return isRequestScopedNotFoundMessage(err.Message)
}

// isRequestInvalidError returns true if the error represents a client request
// error that should not be retried. Specifically, it treats 400 responses with
// "invalid_request_error", request-scoped 404 item misses caused by `store=false`,
// and all 422 responses as request-shape failures, where switching auths or
// pooled upstream models will not help. Model-support errors are excluded so
// routing can fall through to another auth or upstream.
func isRequestInvalidError(err error) bool {
	if err == nil {
		return false
	}
	if isModelSupportError(err) {
		return false
	}
	status := statusCodeFromError(err)
	switch status {
	case http.StatusBadRequest:
		return strings.Contains(err.Error(), "invalid_request_error")
	case http.StatusNotFound:
		return isRequestScopedNotFoundMessage(err.Error())
	case http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

func applyAuthFailureState(auth *Auth, resultErr *Error, retryAfter *time.Duration, now time.Time) {
	if auth == nil {
		return
	}
	if isRequestScopedNotFoundResultError(resultErr) {
		return
	}
	if isConfigManagedAIProvider(auth) {
		applyConfigProviderAuthFailureState(auth, resultErr, now)
		return
	}
	disableCooling := quotaCooldownDisabledForAuth(auth)
	auth.Unavailable = true
	auth.Status = StatusError
	auth.UpdatedAt = now
	if resultErr != nil {
		auth.LastError = cloneError(resultErr)
		if resultErr.Message != "" {
			auth.StatusMessage = resultErr.Message
		}
	}
	statusCode := statusCodeFromResult(resultErr)
	switch statusCode {
	case 401:
		auth.StatusMessage = "unauthorized"
		if disableCooling {
			auth.NextRetryAfter = time.Time{}
		} else {
			auth.NextRetryAfter = now.Add(30 * time.Minute)
		}
	case 402, 403:
		auth.StatusMessage = "payment_required"
		if disableCooling {
			auth.NextRetryAfter = time.Time{}
		} else {
			auth.NextRetryAfter = now.Add(30 * time.Minute)
		}
	case 404:
		auth.StatusMessage = "not_found"
		if disableCooling {
			auth.NextRetryAfter = time.Time{}
		} else {
			auth.NextRetryAfter = now.Add(12 * time.Hour)
		}
	case 429:
		auth.StatusMessage = "quota exhausted"
		auth.Quota.Exceeded = true
		auth.Quota.Reason = "quota"
		var next time.Time
		if !disableCooling {
			if retryAfter != nil {
				next = now.Add(*retryAfter)
			} else {
				cooldown, nextLevel := nextQuotaCooldown(auth.Quota.BackoffLevel, disableCooling)
				if cooldown > 0 {
					next = now.Add(cooldown)
				}
				auth.Quota.BackoffLevel = nextLevel
			}
		}
		auth.Quota.NextRecoverAt = next
		auth.NextRetryAfter = next
	case 408, 500, 502, 503, 504:
		auth.StatusMessage = "transient upstream error"
		if disableCooling {
			auth.NextRetryAfter = time.Time{}
		} else {
			auth.NextRetryAfter = now.Add(1 * time.Minute)
		}
	default:
		if auth.StatusMessage == "" {
			auth.StatusMessage = "request failed"
		}
	}
}

func isConfigManagedAIProvider(auth *Auth) bool {
	if auth == nil {
		return false
	}
	return isConfigProviderRetryCandidate(auth)
}

func applyConfigProviderModelFailureState(auth *Auth, state *ModelState, resultErr *Error, now time.Time) {
	if auth == nil || state == nil {
		return
	}
	state.Unavailable = false
	state.Status = StatusError
	state.UpdatedAt = now
	state.NextRetryAfter = time.Time{}
	state.Quota = QuotaState{}
	if resultErr != nil {
		state.LastError = cloneError(resultErr)
		state.StatusMessage = genericConfigProviderFailureMessage(resultErr)
		auth.LastError = cloneError(resultErr)
		auth.StatusMessage = state.StatusMessage
	} else if state.StatusMessage == "" {
		state.StatusMessage = "request failed"
		auth.StatusMessage = state.StatusMessage
	}
}

func applyConfigProviderAuthFailureState(auth *Auth, resultErr *Error, now time.Time) {
	if auth == nil {
		return
	}
	auth.Unavailable = false
	auth.Status = StatusError
	auth.UpdatedAt = now
	auth.NextRetryAfter = time.Time{}
	auth.Quota = QuotaState{}
	if resultErr != nil {
		auth.LastError = cloneError(resultErr)
		auth.StatusMessage = genericConfigProviderFailureMessage(resultErr)
	} else if auth.StatusMessage == "" {
		auth.StatusMessage = "request failed"
	}
}

func genericConfigProviderFailureMessage(resultErr *Error) string {
	if resultErr == nil {
		return "request failed"
	}
	message := strings.TrimSpace(resultErr.Message)
	if message == "" {
		return "request failed"
	}
	return message
}

// nextQuotaCooldown returns the next cooldown duration and updated backoff level for repeated quota errors.
func nextQuotaCooldown(prevLevel int, disableCooling bool) (time.Duration, int) {
	if prevLevel < 0 {
		prevLevel = 0
	}
	if disableCooling {
		return 0, prevLevel
	}
	cooldown := quotaBackoffBase * time.Duration(1<<prevLevel)
	if cooldown < quotaBackoffBase {
		cooldown = quotaBackoffBase
	}
	if cooldown >= quotaBackoffMax {
		return quotaBackoffMax, prevLevel
	}
	return cooldown, prevLevel + 1
}

// List returns all auth entries currently known by the manager.
func (m *Manager) List() []*Auth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Auth, 0, len(m.auths))
	for _, auth := range m.auths {
		list = append(list, auth.Clone())
	}
	return list
}

// GetByID retrieves an auth entry by its ID.

func (m *Manager) GetByID(id string) (*Auth, bool) {
	if id == "" {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	auth, ok := m.auths[id]
	if !ok {
		return nil, false
	}
	return auth.Clone(), true
}

// Executor returns the registered provider executor for a provider key.
func (m *Manager) Executor(provider string) (ProviderExecutor, bool) {
	if m == nil {
		return nil, false
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, false
	}

	m.mu.RLock()
	executor, okExecutor := m.executors[provider]
	if !okExecutor {
		lowerProvider := strings.ToLower(provider)
		if lowerProvider != provider {
			executor, okExecutor = m.executors[lowerProvider]
		}
	}
	m.mu.RUnlock()

	if !okExecutor || executor == nil {
		return nil, false
	}
	return executor, true
}

// CloseExecutionSession asks all registered executors to release the supplied execution session.
func (m *Manager) CloseExecutionSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || sessionID == "" {
		return
	}

	m.mu.RLock()
	executors := make([]ProviderExecutor, 0, len(m.executors))
	for _, exec := range m.executors {
		executors = append(executors, exec)
	}
	m.mu.RUnlock()

	for i := range executors {
		if closer, ok := executors[i].(ExecutionSessionCloser); ok && closer != nil {
			closer.CloseExecutionSession(sessionID)
		}
	}
}

func (m *Manager) useSchedulerFastPath() bool {
	if m == nil || m.scheduler == nil {
		return false
	}
	return isBuiltInSelector(m.selector)
}

func shouldRetrySchedulerPick(err error) bool {
	if err == nil {
		return false
	}
	var cooldownErr *modelCooldownError
	if errors.As(err, &cooldownErr) {
		return true
	}
	var authErr *Error
	if !errors.As(err, &authErr) || authErr == nil {
		return false
	}
	return authErr.Code == "auth_not_found" || authErr.Code == "auth_unavailable"
}

func (m *Manager) routeAwareSelectionRequired(auth *Auth, routeModel string) bool {
	if auth == nil || strings.TrimSpace(routeModel) == "" {
		return false
	}
	return m.selectionModelKeyForAuth(auth, routeModel) != canonicalModelKey(routeModel)
}

func (m *Manager) pickNextLegacy(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, error) {
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)

	m.mu.RLock()
	executor, okExecutor := m.executors[provider]
	if !okExecutor {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	candidates := make([]*Auth, 0, len(m.auths))
	modelKey := strings.TrimSpace(model)
	// Always use base model name (without thinking suffix) for auth matching.
	if modelKey != "" {
		parsed := thinking.ParseSuffix(modelKey)
		if parsed.ModelName != "" {
			modelKey = strings.TrimSpace(parsed.ModelName)
		}
	}
	registryRef := registry.GetGlobalRegistry()
	for _, candidate := range m.auths {
		if candidate.Provider != provider || candidate.Disabled {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		if _, used := tried[candidate.ID]; used {
			continue
		}
		if modelKey != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	available, errAvailable := m.availableAuthsForRouteModel(candidates, provider, model, time.Now())
	if errAvailable != nil {
		m.mu.RUnlock()
		return nil, nil, errAvailable
	}
	selected, errPick := m.selector.Pick(ctx, provider, selectionArgForSelector(m.selector, model), opts, available)
	if errPick != nil {
		m.mu.RUnlock()
		return nil, nil, errPick
	}
	if selected == nil {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	authCopy := selected.Clone()
	m.mu.RUnlock()
	if !selected.indexAssigned {
		m.mu.Lock()
		if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
			current.EnsureIndex()
			authCopy = current.Clone()
		}
		m.mu.Unlock()
	}
	return authCopy, executor, nil
}

func (m *Manager) pickNext(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, error) {
	if !m.useSchedulerFastPath() {
		return m.pickNextLegacy(ctx, provider, model, opts, tried)
	}
	if strings.TrimSpace(model) != "" {
		m.mu.RLock()
		for _, candidate := range m.auths {
			if candidate == nil || candidate.Provider != provider || candidate.Disabled {
				continue
			}
			if _, used := tried[candidate.ID]; used {
				continue
			}
			if m.routeAwareSelectionRequired(candidate, model) {
				m.mu.RUnlock()
				return m.pickNextLegacy(ctx, provider, model, opts, tried)
			}
		}
		m.mu.RUnlock()
	}
	executor, okExecutor := m.Executor(provider)
	if !okExecutor {
		return nil, nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	selected, errPick := m.scheduler.pickSingle(ctx, provider, model, opts, tried)
	if errPick != nil && model != "" && shouldRetrySchedulerPick(errPick) {
		m.syncScheduler()
		selected, errPick = m.scheduler.pickSingle(ctx, provider, model, opts, tried)
	}
	if errPick != nil {
		return nil, nil, errPick
	}
	if selected == nil {
		return nil, nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	authCopy := selected.Clone()
	if !selected.indexAssigned {
		m.mu.Lock()
		if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
			current.EnsureIndex()
			authCopy = current.Clone()
		}
		m.mu.Unlock()
	}
	return authCopy, executor, nil
}

func isModelCooldownSelectionError(err error) bool {
	if err == nil {
		return false
	}
	var cooldownErr *modelCooldownError
	if errors.As(err, &cooldownErr) {
		return true
	}
	var authErr *Error
	return errors.As(err, &authErr) && authErr != nil && authErr.Code == "auth_unavailable"
}

type cooldownFallbackCandidate struct {
	auth      *Auth
	executor  ProviderExecutor
	provider  string
	remaining time.Duration
	priority  int
}

func (m *Manager) pickNextMixedCooldownFallback(providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if len(providers) == 0 {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}

	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerKey := strings.TrimSpace(strings.ToLower(provider))
		if providerKey == "" {
			continue
		}
		providerSet[providerKey] = struct{}{}
	}
	if len(providerSet) == 0 {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}

	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	now := time.Now()
	registryRef := registry.GetGlobalRegistry()
	candidates := make([]cooldownFallbackCandidate, 0)

	m.mu.RLock()
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || !auth.GroupEnabled() {
			continue
		}
		if pinnedAuthID != "" && auth.ID != pinnedAuthID {
			continue
		}
		if _, alreadyTried := tried[auth.ID]; alreadyTried {
			continue
		}
		providerKey := strings.TrimSpace(strings.ToLower(auth.Provider))
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		executor, okExecutor := m.executors[providerKey]
		if !okExecutor || executor == nil {
			continue
		}
		if strings.TrimSpace(model) != "" && !m.authSupportsRouteModel(registryRef, auth, model) {
			continue
		}
		checkModel := m.selectionModelForAuth(auth, model)
		blocked, reason, next := isAuthBlockedForModel(auth, checkModel, now)
		if !blocked || reason == blockReasonDisabled || next.IsZero() {
			continue
		}
		priority := authPriority(auth)
		if priority <= 0 {
			continue
		}
		remaining := next.Sub(now)
		if remaining < 0 {
			continue
		}
		candidates = append(candidates, cooldownFallbackCandidate{
			auth:      auth.Clone(),
			executor:  executor,
			provider:  providerKey,
			remaining: remaining,
			priority:  priority,
		})
	}
	m.mu.RUnlock()

	if len(candidates) == 0 {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}

	sort.Slice(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		leftScore := left.remaining.Seconds() / float64(left.priority)
		rightScore := right.remaining.Seconds() / float64(right.priority)
		if leftScore != rightScore {
			return leftScore < rightScore
		}
		if left.remaining != right.remaining {
			return left.remaining < right.remaining
		}
		if left.priority != right.priority {
			return left.priority > right.priority
		}
		if left.provider != right.provider {
			return left.provider < right.provider
		}
		return left.auth.ID < right.auth.ID
	})

	selected := candidates[0]
	if selected.auth != nil && !selected.auth.indexAssigned {
		m.mu.Lock()
		if current := m.auths[selected.auth.ID]; current != nil && !current.indexAssigned {
			current.EnsureIndex()
			selected.auth = current.Clone()
		}
		m.mu.Unlock()
	}
	return selected.auth, selected.executor, selected.provider, nil
}

func (m *Manager) pickNextMixedLegacy(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)

	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		p := strings.TrimSpace(strings.ToLower(provider))
		if p == "" {
			continue
		}
		providerSet[p] = struct{}{}
	}
	if len(providerSet) == 0 {
		return nil, nil, "", &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	if pinnedAuthID == "" {
		if preferredProvider := m.preferredProviderForRequest(providers, opts.Metadata); preferredProvider != "" {
			auth, executor, errPick := m.pickNext(ctx, preferredProvider, model, opts, tried)
			if errPick == nil && auth != nil && executor != nil {
				return auth, executor, preferredProvider, nil
			}
		}
	}

	m.mu.RLock()
	candidates := make([]*Auth, 0, len(m.auths))
	modelKey := strings.TrimSpace(model)
	// Always use base model name (without thinking suffix) for auth matching.
	if modelKey != "" {
		parsed := thinking.ParseSuffix(modelKey)
		if parsed.ModelName != "" {
			modelKey = strings.TrimSpace(parsed.ModelName)
		}
	}
	registryRef := registry.GetGlobalRegistry()
	for _, candidate := range m.auths {
		if candidate == nil || candidate.Disabled {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		providerKey := strings.TrimSpace(strings.ToLower(candidate.Provider))
		if providerKey == "" {
			continue
		}
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		if _, used := tried[candidate.ID]; used {
			continue
		}
		if _, ok := m.executors[providerKey]; !ok {
			continue
		}
		if modelKey != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		m.mu.RUnlock()
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	available, errAvailable := m.availableAuthsForRouteModel(candidates, "mixed", model, time.Now())
	if errAvailable != nil {
		m.mu.RUnlock()
		if isModelCooldownSelectionError(errAvailable) {
			if auth, executor, providerKey, fallbackErr := m.pickNextMixedCooldownFallback(providers, model, opts, tried); fallbackErr == nil {
				return auth, executor, providerKey, nil
			}
		}
		return nil, nil, "", errAvailable
	}
	selected, errPick := m.selector.Pick(ctx, "mixed", selectionArgForSelector(m.selector, model), opts, available)
	if errPick != nil {
		m.mu.RUnlock()
		return nil, nil, "", errPick
	}
	if selected == nil {
		m.mu.RUnlock()
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	providerKey := strings.TrimSpace(strings.ToLower(selected.Provider))
	executor, okExecutor := m.executors[providerKey]
	if !okExecutor {
		m.mu.RUnlock()
		return nil, nil, "", &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	authCopy := selected.Clone()
	m.mu.RUnlock()
	if !selected.indexAssigned {
		m.mu.Lock()
		if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
			current.EnsureIndex()
			authCopy = current.Clone()
		}
		m.mu.Unlock()
	}
	return authCopy, executor, providerKey, nil
}

func (m *Manager) pickNextMixed(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if !m.useSchedulerFastPath() {
		return m.pickNextMixedLegacy(ctx, providers, model, opts, tried)
	}

	eligibleProviders := make([]string, 0, len(providers))
	seenProviders := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerKey := strings.TrimSpace(strings.ToLower(provider))
		if providerKey == "" {
			continue
		}
		if _, seen := seenProviders[providerKey]; seen {
			continue
		}
		if _, okExecutor := m.Executor(providerKey); !okExecutor {
			continue
		}
		seenProviders[providerKey] = struct{}{}
		eligibleProviders = append(eligibleProviders, providerKey)
	}
	if len(eligibleProviders) == 0 {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	if pinnedAuthIDFromMetadata(opts.Metadata) == "" {
		if preferredProvider := m.preferredProviderForRequest(eligibleProviders, opts.Metadata); preferredProvider != "" {
			auth, executor, errPick := m.pickNext(ctx, preferredProvider, model, opts, tried)
			if errPick == nil && auth != nil && executor != nil {
				return auth, executor, preferredProvider, nil
			}
		}
	}
	if strings.TrimSpace(model) != "" {
		providerSet := make(map[string]struct{}, len(eligibleProviders))
		for _, providerKey := range eligibleProviders {
			providerSet[providerKey] = struct{}{}
		}
		m.mu.RLock()
		for _, candidate := range m.auths {
			if candidate == nil || candidate.Disabled {
				continue
			}
			if _, ok := providerSet[strings.TrimSpace(strings.ToLower(candidate.Provider))]; !ok {
				continue
			}
			if _, used := tried[candidate.ID]; used {
				continue
			}
			if m.routeAwareSelectionRequired(candidate, model) {
				m.mu.RUnlock()
				return m.pickNextMixedLegacy(ctx, providers, model, opts, tried)
			}
		}
		m.mu.RUnlock()
	}

	selected, providerKey, errPick := m.scheduler.pickMixed(ctx, eligibleProviders, model, opts, tried)
	if errPick != nil && model != "" && shouldRetrySchedulerPick(errPick) {
		m.syncScheduler()
		selected, providerKey, errPick = m.scheduler.pickMixed(ctx, eligibleProviders, model, opts, tried)
	}
	if errPick != nil {
		if isModelCooldownSelectionError(errPick) {
			if auth, executor, fallbackProvider, fallbackErr := m.pickNextMixedCooldownFallback(eligibleProviders, model, opts, tried); fallbackErr == nil {
				return auth, executor, fallbackProvider, nil
			}
		}
		return nil, nil, "", errPick
	}
	if selected == nil {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	executor, okExecutor := m.Executor(providerKey)
	if !okExecutor {
		return nil, nil, "", &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	authCopy := selected.Clone()
	if !selected.indexAssigned {
		m.mu.Lock()
		if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
			current.EnsureIndex()
			authCopy = current.Clone()
		}
		m.mu.Unlock()
	}
	return authCopy, executor, providerKey, nil
}

func (m *Manager) preferredProviderForRequest(providers []string, metadata map[string]any) string {
	if m == nil || len(providers) == 0 {
		return ""
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil || len(cfg.Routing.Rules) == 0 {
		return ""
	}
	userAgent := metadataStringValue(metadata, cliproxyexecutor.RequestUserAgentMetadataKey)
	inputChars, okInputChars := metadataInt64Value(metadata, cliproxyexecutor.RequestInputCharsMetadataKey)
	for _, rule := range cfg.Routing.Rules {
		provider := strings.ToLower(strings.TrimSpace(rule.Provider))
		if provider == "" || !containsProviderKey(providers, provider) {
			continue
		}
		if !routingRuleMatchesUserAgent(rule.UserAgent, userAgent) {
			continue
		}
		if !routingRuleMatchesInputChars(rule.InputChars, inputChars, okInputChars) {
			continue
		}
		return provider
	}
	return ""
}

func containsProviderKey(providers []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return false
	}
	for _, provider := range providers {
		if strings.ToLower(strings.TrimSpace(provider)) == target {
			return true
		}
	}
	return false
}

func metadataStringValue(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	}
}

func metadataInt64Value(metadata map[string]any, key string) (int64, bool) {
	if len(metadata) == 0 {
		return 0, false
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch value := raw.(type) {
	case int:
		return int64(value), true
	case int8:
		return int64(value), true
	case int16:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	case uint:
		return int64(value), true
	case uint8:
		return int64(value), true
	case uint16:
		return int64(value), true
	case uint32:
		return int64(value), true
	case uint64:
		if value > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(value), true
	case float32:
		return int64(value), true
	case float64:
		return int64(value), true
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func routingRuleMatchesUserAgent(rule internalconfig.RoutingUserAgentRule, userAgent string) bool {
	matchMode := strings.ToLower(strings.TrimSpace(rule.Match))
	needle := strings.TrimSpace(rule.Value)
	if matchMode == "" && needle == "" {
		return true
	}
	if needle == "" || userAgent == "" {
		return false
	}
	switch matchMode {
	case "equals", "eq":
		return strings.EqualFold(userAgent, needle)
	case "contains":
		return strings.Contains(strings.ToLower(userAgent), strings.ToLower(needle))
	default:
		return false
	}
}

func routingRuleMatchesInputChars(rule internalconfig.RoutingInputCharsRule, inputChars int64, ok bool) bool {
	operator := strings.ToLower(strings.TrimSpace(rule.Operator))
	if operator == "" && rule.Value == 0 {
		return true
	}
	if !ok {
		return false
	}
	switch operator {
	case "gt", "greater-than", "greater_than":
		return inputChars > rule.Value
	case "lt", "less-than", "less_than":
		return inputChars < rule.Value
	default:
		return false
	}
}

func (m *Manager) persist(ctx context.Context, auth *Auth) error {
	if m.store == nil || auth == nil {
		return nil
	}
	if shouldSkipPersist(ctx) {
		return nil
	}
	if auth.Attributes != nil {
		if v := strings.ToLower(strings.TrimSpace(auth.Attributes["runtime_only"])); v == "true" {
			return nil
		}
	}
	if isConfigDerivedAuth(auth) {
		return nil
	}
	// Skip persistence when metadata is absent (e.g., runtime-only auths).
	if auth.Metadata == nil {
		return nil
	}
	_, err := m.store.Save(ctx, auth)
	return err
}

// StartAutoRefresh launches a background loop that evaluates auth freshness
// every few seconds and triggers refresh operations when required.
// Only one loop is kept alive; starting a new one cancels the previous run.
func (m *Manager) StartAutoRefresh(parent context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = refreshCheckInterval
	}

	m.mu.Lock()
	cancelPrev := m.refreshCancel
	m.refreshCancel = nil
	m.refreshLoop = nil
	m.mu.Unlock()
	if cancelPrev != nil {
		cancelPrev()
	}

	ctx, cancelCtx := context.WithCancel(parent)
	workers := refreshMaxConcurrency
	if cfg, ok := m.runtimeConfig.Load().(*internalconfig.Config); ok && cfg != nil && cfg.AuthAutoRefreshWorkers > 0 {
		workers = cfg.AuthAutoRefreshWorkers
	}
	loop := newAuthAutoRefreshLoop(m, interval, workers)

	m.mu.Lock()
	m.refreshCancel = cancelCtx
	m.refreshLoop = loop
	m.mu.Unlock()

	loop.rebuild(time.Now())
	go loop.run(ctx)
}

// StopAutoRefresh cancels the background refresh loop, if running.
// It also stops the selector if it implements StoppableSelector.
func (m *Manager) StopAutoRefresh() {
	m.mu.Lock()
	cancel := m.refreshCancel
	m.refreshCancel = nil
	m.refreshLoop = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Stop selector if it implements StoppableSelector (e.g., SessionAffinitySelector)
	if stoppable, ok := m.selector.(StoppableSelector); ok {
		stoppable.Stop()
	}
}

func (m *Manager) queueRefreshReschedule(authID string) {
	if m == nil || authID == "" {
		return
	}
	m.mu.RLock()
	loop := m.refreshLoop
	m.mu.RUnlock()
	if loop == nil {
		return
	}
	loop.queueReschedule(authID)
}

func (m *Manager) shouldRefresh(a *Auth, now time.Time) bool {
	if a == nil || a.Disabled {
		return false
	}
	if !a.NextRefreshAfter.IsZero() && now.Before(a.NextRefreshAfter) {
		return false
	}
	if evaluator, ok := a.Runtime.(RefreshEvaluator); ok && evaluator != nil {
		return evaluator.ShouldRefresh(now, a)
	}

	lastRefresh := a.LastRefreshedAt
	if lastRefresh.IsZero() {
		if ts, ok := authLastRefreshTimestamp(a); ok {
			lastRefresh = ts
		}
	}

	expiry, hasExpiry := a.ExpirationTime()

	if interval := authPreferredInterval(a); interval > 0 {
		if hasExpiry && !expiry.IsZero() {
			if !expiry.After(now) {
				return true
			}
			if expiry.Sub(now) <= interval {
				return true
			}
		}
		if lastRefresh.IsZero() {
			return true
		}
		return now.Sub(lastRefresh) >= interval
	}

	provider := strings.ToLower(a.Provider)
	lead := ProviderRefreshLead(provider, a.Runtime)
	if lead == nil {
		return false
	}
	if *lead <= 0 {
		if hasExpiry && !expiry.IsZero() {
			return now.After(expiry)
		}
		return false
	}
	if hasExpiry && !expiry.IsZero() {
		return time.Until(expiry) <= *lead
	}
	if !lastRefresh.IsZero() {
		return now.Sub(lastRefresh) >= *lead
	}
	return true
}

func authPreferredInterval(a *Auth) time.Duration {
	if a == nil {
		return 0
	}
	if d := durationFromMetadata(a.Metadata, "refresh_interval_seconds", "refreshIntervalSeconds", "refresh_interval", "refreshInterval"); d > 0 {
		return d
	}
	if d := durationFromAttributes(a.Attributes, "refresh_interval_seconds", "refreshIntervalSeconds", "refresh_interval", "refreshInterval"); d > 0 {
		return d
	}
	return 0
}

func durationFromMetadata(meta map[string]any, keys ...string) time.Duration {
	if len(meta) == 0 {
		return 0
	}
	for _, key := range keys {
		if val, ok := meta[key]; ok {
			if dur := parseDurationValue(val); dur > 0 {
				return dur
			}
		}
	}
	return 0
}

func durationFromAttributes(attrs map[string]string, keys ...string) time.Duration {
	if len(attrs) == 0 {
		return 0
	}
	for _, key := range keys {
		if val, ok := attrs[key]; ok {
			if dur := parseDurationString(val); dur > 0 {
				return dur
			}
		}
	}
	return 0
}

func parseDurationValue(val any) time.Duration {
	switch v := val.(type) {
	case time.Duration:
		if v <= 0 {
			return 0
		}
		return v
	case int:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case int32:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case int64:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint32:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint64:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case float32:
		if v <= 0 {
			return 0
		}
		return time.Duration(float64(v) * float64(time.Second))
	case float64:
		if v <= 0 {
			return 0
		}
		return time.Duration(v * float64(time.Second))
	case json.Number:
		if i, err := v.Int64(); err == nil {
			if i <= 0 {
				return 0
			}
			return time.Duration(i) * time.Second
		}
		if f, err := v.Float64(); err == nil && f > 0 {
			return time.Duration(f * float64(time.Second))
		}
	case string:
		return parseDurationString(v)
	}
	return 0
}

func parseDurationString(raw string) time.Duration {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	if dur, err := time.ParseDuration(s); err == nil && dur > 0 {
		return dur
	}
	if secs, err := strconv.ParseFloat(s, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	return 0
}

func authLastRefreshTimestamp(a *Auth) (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	if a.Metadata != nil {
		if ts, ok := lookupMetadataTime(a.Metadata, "last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"); ok {
			return ts, true
		}
	}
	if a.Attributes != nil {
		for _, key := range []string{"last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"} {
			if val := strings.TrimSpace(a.Attributes[key]); val != "" {
				if ts, ok := parseTimeValue(val); ok {
					return ts, true
				}
			}
		}
	}
	return time.Time{}, false
}

func lookupMetadataTime(meta map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if val, ok := meta[key]; ok {
			if ts, ok1 := parseTimeValue(val); ok1 {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}

func (m *Manager) markRefreshPending(id string, now time.Time) bool {
	m.mu.Lock()
	auth, ok := m.auths[id]
	if !ok || auth == nil || auth.Disabled {
		m.mu.Unlock()
		return false
	}
	if !auth.NextRefreshAfter.IsZero() && now.Before(auth.NextRefreshAfter) {
		m.mu.Unlock()
		return false
	}
	auth.NextRefreshAfter = now.Add(refreshPendingBackoff)
	m.auths[id] = auth
	m.mu.Unlock()

	m.queueRefreshReschedule(id)
	return true
}

func (m *Manager) refreshAuth(ctx context.Context, id string) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	auth := m.auths[id]
	var exec ProviderExecutor
	if auth != nil {
		exec = m.executors[auth.Provider]
	}
	m.mu.RUnlock()
	if auth == nil || exec == nil {
		return
	}
	cloned := auth.Clone()
	updated, err := exec.Refresh(ctx, cloned)
	if err != nil && errors.Is(err, context.Canceled) {
		log.Debugf("refresh canceled for %s, %s", auth.Provider, auth.ID)
		return
	}
	log.Debugf("refreshed %s, %s, %v", auth.Provider, auth.ID, err)
	now := time.Now()
	if err != nil {
		shouldReschedule := false
		m.mu.Lock()
		if current := m.auths[id]; current != nil {
			current.NextRefreshAfter = now.Add(refreshFailureBackoff)
			current.LastError = &Error{Message: err.Error()}
			m.auths[id] = current
			shouldReschedule = true
			if m.scheduler != nil {
				m.scheduler.upsertAuth(current.Clone())
			}
		}
		m.mu.Unlock()
		if shouldReschedule {
			m.queueRefreshReschedule(id)
		}
		return
	}
	if updated == nil {
		updated = cloned
	}
	// Preserve runtime created by the executor during Refresh.
	// If executor didn't set one, fall back to the previous runtime.
	if updated.Runtime == nil {
		updated.Runtime = auth.Runtime
	}
	updated.LastRefreshedAt = now
	updated.NextRefreshAfter = time.Time{}
	updated.LastError = nil
	updated.UpdatedAt = now
	if m.shouldRefresh(updated, now) {
		updated.NextRefreshAfter = now.Add(refreshIneffectiveBackoff)
	}
	_, _ = m.Update(ctx, updated)
}

func (m *Manager) executorFor(provider string) ProviderExecutor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.executors[provider]
}

// roundTripperContextKey is an unexported context key type to avoid collisions.
type roundTripperContextKey struct{}

// roundTripperFor retrieves an HTTP RoundTripper for the given auth if a provider is registered.
func (m *Manager) roundTripperFor(auth *Auth) http.RoundTripper {
	m.mu.RLock()
	p := m.rtProvider
	m.mu.RUnlock()
	if p == nil || auth == nil {
		return nil
	}
	return p.RoundTripperFor(auth)
}

// RoundTripperProvider defines a minimal provider of per-auth HTTP transports.
type RoundTripperProvider interface {
	RoundTripperFor(auth *Auth) http.RoundTripper
}

// RequestPreparer is an optional interface that provider executors can implement
// to mutate outbound HTTP requests with provider credentials.
type RequestPreparer interface {
	PrepareRequest(req *http.Request, auth *Auth) error
}

func executorKeyFromAuth(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		providerKey := strings.TrimSpace(auth.Attributes["provider_key"])
		compatName := strings.TrimSpace(auth.Attributes["compat_name"])
		if compatName != "" {
			if providerKey == "" {
				providerKey = compatName
			}
			return strings.ToLower(providerKey)
		}
	}
	return strings.ToLower(strings.TrimSpace(auth.Provider))
}

// logEntryWithRequestID returns a logrus entry with request_id field if available in context.
func logEntryWithRequestID(ctx context.Context) *log.Entry {
	if ctx == nil {
		return log.NewEntry(log.StandardLogger())
	}
	if reqID := logging.GetRequestID(ctx); reqID != "" {
		return log.WithField("request_id", reqID)
	}
	return log.NewEntry(log.StandardLogger())
}

func debugLogAuthSelection(entry *log.Entry, auth *Auth, provider string, model string) {
	if !log.IsLevelEnabled(log.DebugLevel) {
		return
	}
	if entry == nil || auth == nil {
		return
	}
	accountType, accountInfo := auth.AccountInfo()
	proxyInfo := auth.ProxyInfo()
	suffix := ""
	if proxyInfo != "" {
		suffix = " " + proxyInfo
	}
	switch accountType {
	case "api_key":
		entry.Debugf("Use API key %s for model %s%s", util.HideAPIKey(accountInfo), model, suffix)
	case "oauth":
		ident := formatOauthIdentity(auth, provider, accountInfo)
		entry.Debugf("Use OAuth %s for model %s%s", ident, model, suffix)
	}
}

func formatOauthIdentity(auth *Auth, provider string, accountInfo string) string {
	if auth == nil {
		return ""
	}
	// Prefer the auth's provider when available.
	providerName := strings.TrimSpace(auth.Provider)
	if providerName == "" {
		providerName = strings.TrimSpace(provider)
	}
	// Only log the basename to avoid leaking host paths.
	// FileName may be unset for some auth backends; fall back to ID.
	authFile := strings.TrimSpace(auth.FileName)
	if authFile == "" {
		authFile = strings.TrimSpace(auth.ID)
	}
	if authFile != "" {
		authFile = filepath.Base(authFile)
	}
	parts := make([]string, 0, 3)
	if providerName != "" {
		parts = append(parts, "provider="+providerName)
	}
	if authFile != "" {
		parts = append(parts, "auth_file="+authFile)
	}
	if len(parts) == 0 {
		return accountInfo
	}
	return strings.Join(parts, " ")
}

// InjectCredentials delegates per-provider HTTP request preparation when supported.
// If the registered executor for the auth provider implements RequestPreparer,
// it will be invoked to modify the request (e.g., add headers).
func (m *Manager) InjectCredentials(req *http.Request, authID string) error {
	if req == nil || authID == "" {
		return nil
	}
	m.mu.RLock()
	a := m.auths[authID]
	var exec ProviderExecutor
	if a != nil {
		exec = m.executors[executorKeyFromAuth(a)]
	}
	m.mu.RUnlock()
	if a == nil || exec == nil {
		return nil
	}
	if p, ok := exec.(RequestPreparer); ok && p != nil {
		return p.PrepareRequest(req, a)
	}
	return nil
}

// PrepareHttpRequest injects provider credentials into the supplied HTTP request.
func (m *Manager) PrepareHttpRequest(ctx context.Context, auth *Auth, req *http.Request) error {
	if m == nil {
		return &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if auth == nil {
		return &Error{Code: "auth_not_found", Message: "auth is nil"}
	}
	if req == nil {
		return &Error{Code: "invalid_request", Message: "http request is nil"}
	}
	if ctx != nil {
		*req = *req.WithContext(ctx)
	}
	providerKey := executorKeyFromAuth(auth)
	if providerKey == "" {
		return &Error{Code: "provider_not_found", Message: "auth provider is empty"}
	}
	exec := m.executorFor(providerKey)
	if exec == nil {
		return &Error{Code: "provider_not_found", Message: "executor not registered for provider: " + providerKey}
	}
	preparer, ok := exec.(RequestPreparer)
	if !ok || preparer == nil {
		return &Error{Code: "not_supported", Message: "executor does not support http request preparation"}
	}
	return preparer.PrepareRequest(req, auth)
}

// NewHttpRequest constructs a new HTTP request and injects provider credentials into it.
func (m *Manager) NewHttpRequest(ctx context.Context, auth *Auth, method, targetURL string, body []byte, headers http.Header) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	method = strings.TrimSpace(method)
	if method == "" {
		method = http.MethodGet
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, targetURL, reader)
	if err != nil {
		return nil, err
	}
	if headers != nil {
		httpReq.Header = headers.Clone()
	}
	if errPrepare := m.PrepareHttpRequest(ctx, auth, httpReq); errPrepare != nil {
		return nil, errPrepare
	}
	return httpReq, nil
}

// HttpRequest injects provider credentials into the supplied HTTP request and executes it.
func (m *Manager) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	if m == nil {
		return nil, &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if auth == nil {
		return nil, &Error{Code: "auth_not_found", Message: "auth is nil"}
	}
	if req == nil {
		return nil, &Error{Code: "invalid_request", Message: "http request is nil"}
	}
	providerKey := executorKeyFromAuth(auth)
	if providerKey == "" {
		return nil, &Error{Code: "provider_not_found", Message: "auth provider is empty"}
	}
	exec := m.executorFor(providerKey)
	if exec == nil {
		return nil, &Error{Code: "provider_not_found", Message: "executor not registered for provider: " + providerKey}
	}
	return exec.HttpRequest(ctx, auth, req)
}
