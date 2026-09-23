package store

// Attempt is one upstream call made while serving a request. The gateway
// may call upstream more than once (see internal/resilience): after a
// failure it can clamp max_tokens, prune the context, exclude a provider,
// back off, or fall back to another route, as long as nothing has been
// written to the client yet.
type Attempt struct {
	// N counts from 1.
	N     int    `json:"n"`
	Route string `json:"route"`
	// Provider is who the route points at (openrouter, anthropic, ...);
	// UpstreamProvider is the provider behind an aggregator that served or
	// failed the call (OpenRouter's provider_name, e.g. "GMICloud").
	Provider         string `json:"provider,omitempty"`
	UpstreamProvider string `json:"upstream_provider,omitempty"`
	Model            string `json:"model,omitempty"`
	// Status is the upstream HTTP status (0 when the call never got one).
	Status int `json:"status"`
	// Class is the failure class (context_overflow, output_too_large,
	// rate_limited, overloaded, provider_error, model_unavailable, auth,
	// bad_request, unknown); "" when the attempt succeeded.
	Class string `json:"class,omitempty"`
	// Message is the provider's own error message, unwrapped from any
	// nesting (OpenRouter's metadata.raw).
	Message string `json:"message,omitempty"`
	// Action is what the gateway did after this attempt: clamp_max_tokens,
	// emergency_prune, ignore_provider, backoff, fallback (a retry follows),
	// or none / give_up (this was the last attempt). "" on success.
	Action string `json:"action,omitempty"`
	// ActionDetail explains the action, or why no recovery was possible.
	ActionDetail string `json:"action_detail,omitempty"`
	DurationMs   int64  `json:"duration_ms"`
	// WaitMs is the backoff slept after this attempt, before the next one.
	WaitMs int64 `json:"wait_ms,omitempty"`
	// Changes are the body changes this attempt was sent with, relative to
	// the body the pipeline produced.
	Changes *AttemptChanges `json:"changes,omitempty"`
}

// AttemptChanges lists what the gateway changed in the request body.
type AttemptChanges struct {
	MaxTokens      *MaxTokensChange      `json:"max_tokens,omitempty"`
	EmergencyPrune *EmergencyPruneChange `json:"emergency_prune,omitempty"`
	// IgnoredProviders were excluded through OpenRouter's provider routing.
	IgnoredProviders []string `json:"ignored_providers,omitempty"`
	// CacheInvalidated is true when the change rewrote the prompt prefix, so
	// the provider's prompt cache no longer matches.
	CacheInvalidated bool `json:"cache_invalidated,omitempty"`
}

// MaxTokensChange is one rewrite of the output-token limit.
type MaxTokensChange struct {
	// Field is max_tokens, max_completion_tokens or max_output_tokens.
	Field string `json:"field"`
	// From is nil when the request did not set a limit.
	From *int `json:"from"`
	To   int  `json:"to"`
	// Reason: filled_missing, above_max_output, exceeds_context_window, or
	// provider_error (clamped after the provider rejected the value).
	Reason string `json:"reason"`
	// Where the limits came from: builtin, openrouter, learned,
	// override:route, override:alias, override:model, or error (numbers
	// read from the provider's message).
	LimitSource     string `json:"limit_source,omitempty"`
	ContextWindow   int    `json:"context_window,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
	EstInputTokens  int    `json:"est_input_tokens,omitempty"`
	// ThinkingBudgetFrom/To: an Anthropic thinking budget lowered to stay
	// below the new max_tokens.
	ThinkingBudgetFrom int `json:"thinking_budget_from,omitempty"`
	ThinkingBudgetTo   int `json:"thinking_budget_to,omitempty"`
}

// EmergencyPruneChange is a pruning pass run because the context overflowed.
type EmergencyPruneChange struct {
	// Dropped counts blocks newly dropped by the emergency pass.
	Dropped int `json:"dropped"`
	// SavedTokens is the estimated difference against the body that
	// overflowed; TokensBefore/After are the estimated sizes.
	SavedTokens  int     `json:"saved_tokens"`
	TokensBefore int     `json:"tokens_before"`
	TokensAfter  int     `json:"tokens_after"`
	Threshold    float64 `json:"keep_threshold"`
}
