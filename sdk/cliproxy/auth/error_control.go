package auth

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"sort"
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// This file hosts the fork's error-control engine: whole-request retry rounds
// over the full candidate set with exponential inter-round backoff, count-based
// provider cooldowns, and optional parallel dispatch of same-priority
// credentials. It replaces the upstream single-pass failover loop for non-home
// execution.

// executionResetSignal snapshots the execution reset generation so in-flight
// requests can detect credential changes and restart their retry loops.
type executionResetSignal struct {
	generation uint64
	ch         chan struct{}
}

var errExecutionReset = errors.New("execution reset requested")

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
	if m == nil || signal.ch == nil {
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

// SetErrorControlConfig stores the sanitized retry-round policy configuration.
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
	if policy.RetryRounds != nil {
		value := *policy.RetryRounds
		if value < 1 {
			value = 1
		}
		policy.RetryRounds = internalconfig.DefaultIntPtr(value)
	}
	return policy
}

type errorControlPolicy struct {
	retryRounds          int
	roundBackoffBase     float64
	roundBackoffExponent float64
	roundBackoffMax      float64
}

func applyErrorControlPolicy(target *errorControlPolicy, policy internalconfig.ErrorControlPolicy) {
	if target == nil {
		return
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

// effectiveErrorControlPolicy resolves layered policy: defaults <- error-control
// default <- per-provider <- per-auth metadata overrides.
func (m *Manager) effectiveErrorControlPolicy(provider string, auth *Auth) errorControlPolicy {
	policy := errorControlPolicy{
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
	if policy.retryRounds < 1 {
		policy.retryRounds = 1
	}
	return policy
}

// maxRetryRounds returns the highest configured retry-round count across the
// requested providers and their credential overrides.
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

// retryRoundWait returns how much of the target interval is still outstanding
// after accounting for the time the round itself consumed.
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

// waitForCooldownSignal sleeps for wait unless the context is canceled or an
// execution reset fires; reset returns true so the caller can rebuild state.
func waitForCooldownSignal(ctx context.Context, wait time.Duration, signal executionResetSignal) (bool, error) {
	if wait <= 0 {
		return false, nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	if signal.ch == nil {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
			return false, nil
		}
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-signal.ch:
		return true, nil
	case <-timer.C:
		return false, nil
	}
}

// ---- Count-based provider cooldown ----

type providerCooldownState struct {
	generatedRaw float64
	remaining    int
}

type providerCooldownPolicy struct {
	start    int
	exponent float64
	max      int
}

func (m *Manager) effectiveProviderCooldownPolicy(auth *Auth) providerCooldownPolicy {
	policy := providerCooldownPolicy{
		start:    internalconfig.DefaultProviderCooldownStart,
		exponent: internalconfig.DefaultProviderCooldownExponent,
		max:      internalconfig.DefaultProviderCooldownMax,
	}
	if m != nil {
		if cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config); cfg != nil {
			applyProviderCooldownConfig(&policy, cfg.ProviderCooldown)
		}
	}
	if auth != nil {
		if v, ok := auth.ProviderCooldownStartOverride(); ok {
			policy.start = v
		}
		if v, ok := auth.ProviderCooldownExponentOverride(); ok {
			policy.exponent = v
		}
		if v, ok := auth.ProviderCooldownMaxOverride(); ok {
			policy.max = v
		}
	}
	if policy.start < 1 {
		policy.start = internalconfig.DefaultProviderCooldownStart
	}
	if policy.exponent <= 0 {
		policy.exponent = internalconfig.DefaultProviderCooldownExponent
	}
	if policy.max < 1 {
		policy.max = internalconfig.DefaultProviderCooldownMax
	}
	return policy
}

func applyProviderCooldownConfig(target *providerCooldownPolicy, cfg internalconfig.ProviderCooldownConfig) {
	if target == nil {
		return
	}
	if cfg.Start != nil {
		target.start = *cfg.Start
	}
	if cfg.Exponent != nil {
		target.exponent = *cfg.Exponent
	}
	if cfg.Max != nil {
		target.max = *cfg.Max
	}
}

func providerCooldownRawAfterFailure(previousRaw float64, policy providerCooldownPolicy) float64 {
	if policy.start < 1 || policy.max < 1 {
		return 0
	}
	if policy.exponent <= 0 {
		policy.exponent = internalconfig.DefaultProviderCooldownExponent
	}
	value := float64(policy.start)
	if previousRaw > 0 {
		value = previousRaw * policy.exponent
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value > float64(policy.max) {
		return float64(policy.max)
	}
	if value < 1 {
		return 1
	}
	return value
}

func providerCooldownCountFromRaw(value float64, policy providerCooldownPolicy) int {
	if value <= 0 || policy.max < 1 {
		return 0
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value > float64(policy.max) {
		return policy.max
	}
	count := int(math.Floor(value))
	if count < 1 {
		count = 1
	}
	if count > policy.max {
		return policy.max
	}
	return count
}

func (m *Manager) nextProviderCooldownRawLocked(auth *Auth) float64 {
	if m == nil || auth == nil {
		return 0
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return 0
	}
	state := m.providerCooldown[authID]
	return providerCooldownRawAfterFailure(state.generatedRaw, m.effectiveProviderCooldownPolicy(auth))
}

func (m *Manager) resetProviderCooldownLocked(authID string) {
	if m == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" || len(m.providerCooldown) == 0 {
		return
	}
	delete(m.providerCooldown, authID)
}

func (m *Manager) recordProviderCooldownFailureLocked(auth *Auth) {
	if m == nil || auth == nil {
		return
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return
	}
	if m.providerCooldown == nil {
		m.providerCooldown = make(map[string]providerCooldownState)
	}
	state := m.providerCooldown[authID]
	policy := m.effectiveProviderCooldownPolicy(auth)
	state.generatedRaw = providerCooldownRawAfterFailure(state.generatedRaw, policy)
	state.remaining = providerCooldownCountFromRaw(state.generatedRaw, policy)
	m.providerCooldown[authID] = state
}

// ---- Failure streaks (parallel-dispatch eligibility input) ----

func (m *Manager) authFailureStreakLocked(authID string) int {
	if m == nil || len(m.authFailureStreak) == 0 {
		return 0
	}
	return m.authFailureStreak[strings.TrimSpace(authID)]
}

func (m *Manager) incrementAuthFailureStreakLocked(authID string) {
	if m == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	if m.authFailureStreak == nil {
		m.authFailureStreak = make(map[string]int)
	}
	m.authFailureStreak[authID]++
}

func (m *Manager) resetAuthFailureStreakLocked(authID string) {
	if m == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	if m.authFailureStreak == nil {
		m.authFailureStreak = make(map[string]int)
	}
	m.authFailureStreak[authID] = 0
}

// ---- Provider pool versions (chain staleness) ----

func (m *Manager) bumpProviderPoolVersionLocked(provider string) {
	if m == nil {
		return
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return
	}
	if m.providerPoolVersions == nil {
		m.providerPoolVersions = make(map[string]uint64)
	}
	m.providerPoolVersions[provider]++
	if m.providerPoolVersions[provider] == 0 {
		m.providerPoolVersions[provider] = 1
	}
}

func (m *Manager) bumpProviderPoolVersionsLocked(providers ...string) {
	if m == nil {
		return
	}
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			continue
		}
		if _, ok := seen[provider]; ok {
			continue
		}
		seen[provider] = struct{}{}
		m.bumpProviderPoolVersionLocked(provider)
	}
}

func (m *Manager) providerPoolVersionsForLocked(providerSet map[string]struct{}) map[string]uint64 {
	if len(providerSet) == 0 {
		return nil
	}
	versions := make(map[string]uint64, len(providerSet))
	for provider := range providerSet {
		if provider == "" {
			continue
		}
		versions[provider] = m.providerPoolVersions[provider]
	}
	return versions
}

func (m *Manager) retryCandidateChainStale(chain *retryCandidateChain) bool {
	if m == nil || chain == nil || len(chain.versions) == 0 {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for provider, version := range chain.versions {
		if m.providerPoolVersions[provider] != version {
			return true
		}
	}
	return false
}

// ---- Retry candidates and chains ----

type retryCandidate struct {
	auth                         *Auth
	executor                     ProviderExecutor
	provider                     string
	authFile                     bool
	priority                     priorityTier
	failureStreak                int
	parallelEligible             bool
	roundDispatchIndex           int
	providerCooldownRemaining    int
	providerCooldownGeneratedRaw float64
}

type retryCandidateChain struct {
	candidates []retryCandidate
	versions   map[string]uint64
	index      int
}

// nextBatch returns the next dispatch unit: a single candidate, or every
// parallel-eligible candidate sharing the head candidate's priority tier.
func (c *retryCandidateChain) nextBatch() ([]retryCandidate, bool) {
	if c == nil || c.index >= len(c.candidates) {
		return nil, false
	}
	first := c.candidates[c.index]
	if !first.parallelEligible {
		c.index++
		return []retryCandidate{first}, true
	}
	priority := first.priority
	end := c.index
	for end < len(c.candidates) && c.candidates[end].priority == priority {
		end++
	}
	batch := make([]retryCandidate, 0, end-c.index)
	remaining := make([]retryCandidate, 0, end-c.index)
	for _, candidate := range c.candidates[c.index:end] {
		if candidate.parallelEligible {
			batch = append(batch, candidate)
		} else {
			remaining = append(remaining, candidate)
		}
	}
	if len(batch) == 0 {
		c.index++
		return []retryCandidate{first}, true
	}
	nextCandidates := make([]retryCandidate, 0, len(c.candidates)-len(batch))
	nextCandidates = append(nextCandidates, c.candidates[:c.index]...)
	nextCandidates = append(nextCandidates, remaining...)
	nextCandidates = append(nextCandidates, c.candidates[end:]...)
	c.candidates = nextCandidates
	return batch, true
}

var shuffleRetryCandidates = func(candidates []retryCandidate) {
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
}

type parallelRequestConfig struct {
	enabled     bool
	minRound    int
	minFailures int
}

func (m *Manager) parallelRequestConfig() parallelRequestConfig {
	if m == nil {
		return parallelRequestConfig{}
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil || !cfg.Routing.ParallelRequestsEnabled {
		return parallelRequestConfig{}
	}
	minRound := cfg.Routing.ParallelRequestsMinRound
	if minRound < 0 {
		minRound = 0
	}
	minFailures := cfg.Routing.ParallelRequestsMinFailures
	if minFailures < 0 {
		minFailures = 0
	}
	return parallelRequestConfig{
		enabled:     true,
		minRound:    minRound,
		minFailures: minFailures,
	}
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
	return auth.AuthKind() == AuthKindAPIKey
}

// isConfigManagedAIProvider reports whether failure state should skip
// time-based cooldowns because the credential is config-synthesized and the
// count-based provider cooldown gates it instead.
func isConfigManagedAIProvider(auth *Auth) bool {
	return isConfigProviderRetryCandidate(auth)
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

// applyConfigProviderModelFailureState records the failure for observability
// without scheduling a time-based suspension.
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

// retryCandidates collects every eligible credential for the request, ordered
// by priority tier with the configured routing strategy inside a tier.
func (m *Manager) retryCandidates(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options) ([]retryCandidate, map[string]uint64, error) {
	_ = ctx
	if m == nil {
		return nil, nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key != "" {
			providerSet[key] = struct{}{}
		}
	}
	if len(providerSet) == 0 {
		return nil, nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)

	now := time.Now()
	registryRef := registry.GetGlobalRegistry()
	candidates := make([]retryCandidate, 0)
	totalAuthFiles := 0
	blockedAuthFiles := 0
	cooldownAuthFiles := 0
	earliestAuthFileRetry := time.Time{}

	m.mu.RLock()
	versions := m.providerPoolVersionsForLocked(providerSet)
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
		providerCooldownRemaining := 0
		if state, ok := m.providerCooldown[auth.ID]; ok && state.remaining > 0 {
			providerCooldownRemaining = state.remaining
		}
		candidates = append(candidates, retryCandidate{
			auth:                         authCopy,
			executor:                     executor,
			provider:                     providerKey,
			authFile:                     authFile,
			priority:                     authPriorityTier(auth),
			failureStreak:                m.authFailureStreakLocked(auth.ID),
			providerCooldownRemaining:    providerCooldownRemaining,
			providerCooldownGeneratedRaw: m.nextProviderCooldownRawLocked(auth),
		})
	}
	m.mu.RUnlock()

	if len(candidates) == 0 {
		if totalAuthFiles > 0 && cooldownAuthFiles == totalAuthFiles && !earliestAuthFileRetry.IsZero() {
			resetIn := earliestAuthFileRetry.Sub(now)
			if resetIn < 0 {
				resetIn = 0
			}
			return nil, nil, newModelCooldownError(model, "", resetIn)
		}
		if totalAuthFiles > 0 && cooldownAuthFiles+blockedAuthFiles == totalAuthFiles {
			return nil, nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
		}
		return nil, nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}

	strategy := m.candidateOrderingStrategy()
	if strategy == candidateOrderingRandom {
		shuffleRetryCandidates(candidates)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		if left.priority != right.priority {
			return left.priority.betterThan(right.priority)
		}
		if strategy == candidateOrderingLastSuccess && left.failureStreak != right.failureStreak {
			return left.failureStreak < right.failureStreak
		}
		if strategy == candidateOrderingFillFirst && left.auth != nil && right.auth != nil {
			return left.auth.ID < right.auth.ID
		}
		return false
	})

	candidates = m.applyCustomSelectorOrder(ctx, providers, model, opts, candidates)

	return candidates, versions, nil
}

// applyCustomSelectorOrder lets a non-built-in selector (session affinity,
// downstream websocket pinning, plugin-provided pickers) choose which
// credential leads the retry chain. Only the head is delegated: the remaining
// candidates keep the strategy order so round/cooldown failover still walks
// the whole pool, and stateful selectors see exactly one Pick per chain build
// like the pre-engine dispatch loop did per attempt.
func (m *Manager) applyCustomSelectorOrder(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, candidates []retryCandidate) []retryCandidate {
	if m == nil || len(candidates) < 2 {
		return candidates
	}
	m.mu.RLock()
	selector := m.selector
	m.mu.RUnlock()
	if selector == nil || isBuiltInSelector(selector) {
		return candidates
	}
	available := make([]*Auth, 0, len(candidates))
	for i := range candidates {
		if candidates[i].auth != nil {
			available = append(available, candidates[i].auth)
		}
	}
	if len(available) < 2 {
		return candidates
	}
	providerLabel := "mixed"
	if len(providers) == 1 {
		providerLabel = strings.ToLower(strings.TrimSpace(providers[0]))
	}
	picked, errPick := selector.Pick(ctx, providerLabel, selectionArgForSelector(selector, model), opts, available)
	if errPick != nil || picked == nil {
		return candidates
	}
	for i := range candidates {
		if candidates[i].auth == nil || candidates[i].auth.ID != picked.ID {
			continue
		}
		if i == 0 {
			return candidates
		}
		reordered := make([]retryCandidate, 0, len(candidates))
		reordered = append(reordered, candidates[i])
		reordered = append(reordered, candidates[:i]...)
		reordered = append(reordered, candidates[i+1:]...)
		return reordered
	}
	return candidates
}

const (
	candidateOrderingRandom      = "random"
	candidateOrderingFillFirst   = "fill-first"
	candidateOrderingLastSuccess = "last-success"
)

// candidateOrderingStrategy maps routing.strategy onto candidate ordering.
// last-success keeps the pre-shuffle order and floats low-failure-streak
// credentials to the front of each priority tier; fill-first keeps the stable
// registration order so earlier credentials drain first; anything else
// (round-robin, the default) randomizes same-priority candidates per request.
func (m *Manager) candidateOrderingStrategy() string {
	if m == nil {
		return candidateOrderingRandom
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		return candidateOrderingRandom
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Routing.Strategy)) {
	case candidateOrderingLastSuccess:
		return candidateOrderingLastSuccess
	case candidateOrderingFillFirst:
		return candidateOrderingFillFirst
	default:
		return candidateOrderingRandom
	}
}

func (m *Manager) buildRetryCandidateChain(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, round int) (*retryCandidateChain, error) {
	candidates, versions, err := m.retryCandidates(ctx, providers, model, opts)
	if err != nil {
		return nil, err
	}
	candidates = m.roundCandidates(candidates, round)
	if len(candidates) > 0 {
		candidates = m.applyProviderCooldownToCandidates(candidates)
	}
	// max-retry-credentials caps how many distinct credentials one request may
	// consume per round; 0 keeps the whole pool eligible.
	if limit := int(m.maxRetryCredentials.Load()); limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	m.annotateParallelEligibility(candidates, round)
	return &retryCandidateChain{candidates: candidates, versions: versions}, nil
}

func (m *Manager) annotateParallelEligibility(candidates []retryCandidate, round int) {
	if len(candidates) == 0 {
		return
	}
	policy := m.parallelRequestConfig()
	if !policy.enabled || round+1 <= policy.minRound {
		return
	}
	for i := range candidates {
		if candidates[i].auth == nil {
			continue
		}
		candidates[i].parallelEligible = candidates[i].failureStreak >= policy.minFailures
	}
}

// applyProviderCooldownToCandidates consumes one cooldown tick per candidate
// and drops still-cooling candidates from the round. When every candidate is
// cooling, the smallest remaining count is deducted from all so the pool never
// deadlocks with zero dispatchable credentials.
func (m *Manager) applyProviderCooldownToCandidates(candidates []retryCandidate) []retryCandidate {
	if m == nil || len(candidates) == 0 {
		return candidates
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	minRemaining := 0
	allCooling := true
	for _, candidate := range candidates {
		authID := ""
		if candidate.auth != nil {
			authID = strings.TrimSpace(candidate.auth.ID)
		}
		if authID == "" {
			allCooling = false
			break
		}
		remaining := m.providerCooldown[authID].remaining
		if remaining <= 0 {
			allCooling = false
			break
		}
		if minRemaining == 0 || remaining < minRemaining {
			minRemaining = remaining
		}
	}
	if allCooling && minRemaining > 0 {
		m.decrementProviderCooldownLocked(candidates, minRemaining)
	}

	active := make([]retryCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		authID := ""
		if candidate.auth != nil {
			authID = strings.TrimSpace(candidate.auth.ID)
		}
		if authID == "" {
			candidate.providerCooldownRemaining = 0
			candidate.providerCooldownGeneratedRaw = m.nextProviderCooldownRawLocked(candidate.auth)
			active = append(active, candidate)
			continue
		}
		state := m.providerCooldown[authID]
		if state.remaining <= 0 {
			candidate.providerCooldownRemaining = 0
			candidate.providerCooldownGeneratedRaw = m.nextProviderCooldownRawLocked(candidate.auth)
			active = append(active, candidate)
			continue
		}
		state.remaining--
		if state.remaining < 0 {
			state.remaining = 0
		}
		m.providerCooldown[authID] = state
	}
	return active
}

func (m *Manager) decrementProviderCooldownLocked(candidates []retryCandidate, count int) {
	if m == nil || count <= 0 || len(candidates) == 0 {
		return
	}
	for _, candidate := range candidates {
		if candidate.auth == nil {
			continue
		}
		authID := strings.TrimSpace(candidate.auth.ID)
		if authID == "" {
			continue
		}
		state := m.providerCooldown[authID]
		if state.remaining <= 0 {
			continue
		}
		state.remaining -= count
		if state.remaining < 0 {
			state.remaining = 0
		}
		m.providerCooldown[authID] = state
	}
}

// roundCandidates filters candidates whose effective retry-round budget covers
// the (zero-based) round about to run.
func (m *Manager) roundCandidates(candidates []retryCandidate, round int) []retryCandidate {
	if round <= 0 || len(candidates) == 0 {
		return candidates
	}
	active := make([]retryCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		policy := m.effectiveErrorControlPolicy(candidate.provider, candidate.auth)
		if policy.retryRounds > round {
			active = append(active, candidate)
		}
	}
	return active
}

func assignRoundDispatchIndexes(candidates []retryCandidate, next *int) []retryCandidate {
	if len(candidates) == 0 {
		return candidates
	}
	out := make([]retryCandidate, len(candidates))
	copy(out, candidates)
	for i := range out {
		if next != nil {
			*next = *next + 1
			out[i].roundDispatchIndex = *next
		}
	}
	return out
}

// executionContextForCandidate stamps scheduling metadata for usage sinks and
// attaches the per-auth round tripper.
func (m *Manager) executionContextForCandidate(ctx context.Context, candidate retryCandidate, round int) context.Context {
	execCtx := ctx
	if m != nil {
		if rt := m.roundTripperFor(candidate.auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
	}
	metadata := coreusage.MetadataFromContext(execCtx)
	metadata.RetryRound = round + 1
	metadata.RoundDispatchIndex = candidate.roundDispatchIndex
	metadata.ParallelEligible = candidate.parallelEligible
	metadata.ProviderCooldownRemaining = candidate.providerCooldownRemaining
	metadata.ProviderCooldownGeneratedRaw = candidate.providerCooldownGeneratedRaw
	if m != nil && candidate.auth != nil {
		authID := strings.TrimSpace(candidate.auth.ID)
		if authID != "" {
			m.mu.RLock()
			state := m.providerCooldown[authID]
			if state.remaining > 0 {
				metadata.ProviderCooldownRemaining = state.remaining
			} else {
				metadata.ProviderCooldownRemaining = 0
			}
			metadata.ProviderCooldownGeneratedRaw = m.nextProviderCooldownRawLocked(candidate.auth)
			m.mu.RUnlock()
		}
	}
	return coreusage.WithRequestMetadata(execCtx, metadata)
}

// newUpstreamExhaustedError sanitizes the terminal error sent downstream after
// all rounds and candidates failed. The status code is preserved so clients can
// distinguish rate limiting from server failures, but the body is fixed and the
// error is marked retryable.
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

// ---- Runtime error clearing on execution reset ----

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

// ---- Auth config fingerprint (execution restart detection on Update) ----

func authConfigMetadataFingerprint(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	keys := map[string]struct{}{
		"backoff-mode":           {},
		"backoff_mode":           {},
		"cooldown-exponent":      {},
		"cooldown-max":           {},
		"cooldown-start":         {},
		"cooldown_exponent":      {},
		"cooldown_max":           {},
		"cooldown_start":         {},
		"disable-cooling":        {},
		"disable_cooling":        {},
		"headers":                {},
		"priority":               {},
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

func shouldResetAuthRuntimeOnUpdate(existing, next *Auth) bool {
	if existing == nil || next == nil {
		return false
	}
	return authConfigFingerprint(existing) != authConfigFingerprint(next)
}

// ---- Candidate executors ----

// executeRetryCandidate runs one non-streaming attempt against one credential,
// walking its model pool. It mirrors the credential-execution semantics of the
// mixed executor (auth prepare, request interceptor, alias rewriting).
func (m *Manager) executeRetryCandidate(ctx context.Context, candidate retryCandidate, round int, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, signal executionResetSignal) (cliproxyexecutor.Response, error) {
	return m.executeResponseRetryCandidate(ctx, candidate, round, req, opts, signal, false)
}

// executeCountRetryCandidate is executeRetryCandidate for CountTokens.
func (m *Manager) executeCountRetryCandidate(ctx context.Context, candidate retryCandidate, round int, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, signal executionResetSignal) (cliproxyexecutor.Response, error) {
	return m.executeResponseRetryCandidate(ctx, candidate, round, req, opts, signal, true)
}

func (m *Manager) executeResponseRetryCandidate(ctx context.Context, candidate retryCandidate, round int, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, signal executionResetSignal, countTokens bool) (cliproxyexecutor.Response, error) {
	if candidate.executor == nil || candidate.auth == nil {
		return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	entry := logEntryWithRequestID(ctx)
	debugLogAuthSelection(entry, candidate.auth, candidate.provider, req.Model)
	publishSelectedAuthMetadata(opts.Metadata, candidate.auth.ID)

	routeModel := req.Model
	auth := candidate.auth
	execCtx := m.executionContextForCandidate(ctx, candidate, round)
	execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)

	models, pooled, aliasResult := m.preparedExecutionModelsWithAlias(auth, routeModel)
	if len(models) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "auth_unavailable", Message: "no upstream model available"}
	}

	var errPrepare error
	auth, errPrepare = m.prepareRequestAuth(execCtx, candidate.executor, auth)
	if errPrepare != nil {
		result := Result{AuthID: candidate.auth.ID, AuthGeneration: candidate.auth.RuntimeGeneration, Provider: candidate.provider, Model: routeModel, Success: false, Error: &Error{Message: errPrepare.Error()}}
		if se, ok := errors.AsType[cliproxyexecutor.StatusError](errPrepare); ok && se != nil {
			result.Error.HTTPStatus = se.StatusCode()
		}
		m.MarkResult(execCtx, result)
		return cliproxyexecutor.Response{}, errPrepare
	}

	var lastErr error
	for _, upstreamModel := range models {
		if m.executionResetChanged(signal) {
			return cliproxyexecutor.Response{}, errExecutionReset
		}
		resultModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)
		execReq := req
		execReq.Model = upstreamModel
		execOpts := opts
		execReq, execOpts = applyRequestAfterAuthInterceptor(execCtx, candidate.executor, candidate.provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
		var resp cliproxyexecutor.Response
		var errExec error
		if countTokens {
			resp, errExec = candidate.executor.CountTokens(execCtx, auth, execReq, execOpts)
		} else {
			resp, errExec = candidate.executor.Execute(execCtx, auth, execReq, execOpts)
		}
		result := Result{AuthID: auth.ID, AuthGeneration: auth.RuntimeGeneration, Provider: candidate.provider, Model: resultModel, Success: errExec == nil}
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
			// Request-shape failures will not succeed on another pooled model
			// (or another credential); everything else falls through to the
			// next upstream model in the alias pool.
			if isRequestInvalidError(errExec) {
				return cliproxyexecutor.Response{}, errExec
			}
			continue
		}
		m.MarkResult(execCtx, result)
		if !countTokens {
			rewriteForceMappedResponse(&resp, aliasResult)
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = &Error{Code: "auth_not_found", Message: "no upstream model available"}
	}
	return cliproxyexecutor.Response{}, lastErr
}

func (m *Manager) executeStreamRetryCandidate(ctx context.Context, candidate retryCandidate, round int, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, signal executionResetSignal) (*cliproxyexecutor.StreamResult, error) {
	return m.executeStreamRetryCandidateWithCompletion(ctx, candidate, round, req, opts, signal, nil)
}

func (m *Manager) executeStreamRetryCandidateWithCompletion(ctx context.Context, candidate retryCandidate, round int, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, signal executionResetSignal, onComplete func(success bool)) (*cliproxyexecutor.StreamResult, error) {
	if candidate.executor == nil || candidate.auth == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	entry := logEntryWithRequestID(ctx)
	debugLogAuthSelection(entry, candidate.auth, candidate.provider, req.Model)
	publishSelectedAuthMetadata(opts.Metadata, candidate.auth.ID)

	routeModel := req.Model
	auth := candidate.auth
	execCtx := m.executionContextForCandidate(ctx, candidate, round)

	models, pooled, aliasResult := m.preparedExecutionModelsWithAlias(auth, routeModel)
	if len(models) == 0 {
		return nil, &Error{Code: "auth_unavailable", Message: "no upstream model available"}
	}

	var errPrepare error
	auth, errPrepare = m.prepareRequestAuth(execCtx, candidate.executor, auth)
	if errPrepare != nil {
		result := Result{AuthID: candidate.auth.ID, AuthGeneration: candidate.auth.RuntimeGeneration, Provider: candidate.provider, Model: routeModel, Success: false, Error: &Error{Message: errPrepare.Error()}}
		if se, ok := errors.AsType[cliproxyexecutor.StatusError](errPrepare); ok && se != nil {
			result.Error.HTTPStatus = se.StatusCode()
		}
		m.MarkResult(execCtx, result)
		return nil, errPrepare
	}

	if m.executionResetChanged(signal) {
		return nil, errExecutionReset
	}
	execReq := sanitizeDownstreamWebsocketFallbackRequest(execCtx, auth, req)
	streamResult, errStream := m.executeStreamWithModelPoolSignal(execCtx, candidate.executor, auth, candidate.provider, execReq, opts, routeModel, models, pooled, aliasResult, signal, onComplete)
	if errors.Is(errStream, errExecutionReset) {
		return nil, errExecutionReset
	}
	if errStream != nil {
		if errCtx := execCtx.Err(); errCtx != nil {
			return nil, errCtx
		}
		return nil, errStream
	}
	return streamResult, nil
}

type retryResponseExecutor func(context.Context, retryCandidate, int, cliproxyexecutor.Request, cliproxyexecutor.Options, executionResetSignal) (cliproxyexecutor.Response, error)

type retryResponseResult struct {
	resp cliproxyexecutor.Response
	err  error
}

// executeResponseCandidateBatch dispatches a batch (usually size 1) of
// candidates. Parallel batches race; the first success cancels the rest with
// ErrParallelRequestAborted so usage sinks can tell losers from real failures.
func (m *Manager) executeResponseCandidateBatch(ctx context.Context, candidates []retryCandidate, round int, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, signal executionResetSignal, execute retryResponseExecutor) (cliproxyexecutor.Response, error) {
	if len(candidates) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	if len(candidates) == 1 {
		return execute(ctx, candidates[0], round, req, opts, signal)
	}
	batchCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	results := make(chan retryResponseResult, len(candidates))
	for _, candidate := range candidates {
		candidate := candidate
		go func() {
			resp, err := execute(batchCtx, candidate, round, req, opts, signal)
			results <- retryResponseResult{resp: resp, err: err}
		}()
	}
	var lastErr error
	for range candidates {
		result := <-results
		if result.err == nil {
			cancel(coreusage.ErrParallelRequestAborted)
			return result.resp, nil
		}
		if errors.Is(result.err, errExecutionReset) {
			cancel(errExecutionReset)
			return cliproxyexecutor.Response{}, errExecutionReset
		}
		lastErr = result.err
	}
	if lastErr == nil {
		lastErr = &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	return cliproxyexecutor.Response{}, lastErr
}

type retryStreamResult struct {
	result *cliproxyexecutor.StreamResult
	err    error
}

func (m *Manager) executeStreamCandidateBatch(ctx context.Context, candidates []retryCandidate, round int, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, signal executionResetSignal) (*cliproxyexecutor.StreamResult, error) {
	if len(candidates) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	if len(candidates) == 1 {
		return m.executeStreamRetryCandidate(ctx, candidates[0], round, req, opts, signal)
	}
	batchCtx, cancel := context.WithCancelCause(ctx)
	results := make(chan retryStreamResult, len(candidates))
	for _, candidate := range candidates {
		candidate := candidate
		go func() {
			result, err := m.executeStreamRetryCandidateWithCompletion(batchCtx, candidate, round, req, opts, signal, func(success bool) {
				if success {
					cancel(coreusage.ErrParallelRequestAborted)
					return
				}
				cancel(nil)
			})
			results <- retryStreamResult{result: result, err: err}
		}()
	}
	var lastErr error
	for range candidates {
		result := <-results
		if result.err == nil {
			return result.result, nil
		}
		if errors.Is(result.err, errExecutionReset) {
			cancel(errExecutionReset)
			return nil, errExecutionReset
		}
		lastErr = result.err
	}
	if lastErr == nil {
		lastErr = &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	cancel(nil)
	return nil, lastErr
}
