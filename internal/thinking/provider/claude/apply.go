// Package claude implements thinking configuration scaffolding for Claude models.
//
// Claude models use the thinking.budget_tokens format with values in the range
// 1024-128000. Some Claude models support ZeroAllowed (sonnet-4-5, opus-4-5),
// while older models do not.
// See: _bmad-output/planning-artifacts/architecture.md#Epic-6
package claude

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for Claude models.
// This applier is stateless and holds no configuration.
type Applier struct{}

// NewApplier creates a new Claude thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("claude", NewApplier())
}

// Apply applies thinking configuration to Claude request body.
//
// IMPORTANT: This method expects config to be pre-validated by thinking.ValidateConfig.
// ValidateConfig handles:
//   - Mode conversion (Level→Budget, Auto→Budget for non-DynamicAllowed models)
//   - Budget clamping to model range
//   - ZeroAllowed constraint enforcement
//
// Apply processes ModeBudget, ModeNone, and ModeAuto:
//   - ModeAuto + DynamicAllowed → thinking.type="adaptive" (Opus 4.6+)
//   - ModeBudget → thinking.type="enabled" + budget_tokens
//   - ModeNone → thinking.type="disabled"
//
// Expected output format when adaptive (Opus 4.6+):
//
//	{
//	  "thinking": {
//	    "type": "adaptive"
//	  }
//	}
//
// Expected output format when enabled (legacy):
//
//	{
//	  "thinking": {
//	    "type": "enabled",
//	    "budget_tokens": 16384
//	  }
//	}
//
// Expected output format when disabled:
//
//	{
//	  "thinking": {
//	    "type": "disabled"
//	  }
//	}
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return applyCompatibleClaude(body, config)
	}
	if modelInfo.Thinking == nil {
		return body, nil
	}

	// Speed-only config: chỉ set speed, KHÔNG touch thinking
	// Ví dụ: claude-opus-4-6(fast) → speed=fast, thinking giữ nguyên từ request
	if isSpeedOnly(config) {
		if len(body) == 0 || !gjson.ValidBytes(body) {
			body = []byte(`{}`)
		}
		return applySpeed(body, config), nil
	}

	// Effort-only config: chỉ set output_config.effort, KHÔNG touch thinking
	// Ví dụ: claude-opus-4-6(max) → effort=max, thinking giữ nguyên từ request
	if isEffortOnly(config) {
		if len(body) == 0 || !gjson.ValidBytes(body) {
			body = []byte(`{}`)
		}
		return applySpeed(applyEffort(body, config), config), nil
	}

	// Xử lý ModeBudget, ModeNone, và ModeAuto (adaptive)
	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto {
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	// ModeNone → disabled (nhưng vẫn có thể set effort và speed)
	if config.Mode == thinking.ModeNone || config.Budget == 0 {
		result, _ := sjson.SetBytes(body, "thinking.type", "disabled")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		result = applyEffort(result, config)
		result = applySpeed(result, config)
		return result, nil
	}

	// ModeAuto + DynamicAllowed → adaptive thinking (Opus 4.6+)
	// Claude tự quyết định khi nào và bao nhiêu thinking, không cần budget_tokens
	if config.Mode == thinking.ModeAuto && modelInfo.Thinking.DynamicAllowed {
		result, _ := sjson.SetBytes(body, "thinking.type", "adaptive")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		result = applyEffort(result, config)
		result = applySpeed(result, config)
		return result, nil
	}

	// ModeBudget hoặc ModeAuto fallback → enabled + budget_tokens
	result, _ := sjson.SetBytes(body, "thinking.type", "enabled")
	if config.Budget > 0 {
		result, _ = sjson.SetBytes(result, "thinking.budget_tokens", config.Budget)
		// Ensure max_tokens > thinking.budget_tokens (Anthropic API constraint)
		result = a.normalizeClaudeBudget(result, config.Budget, modelInfo)
	} else {
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
	}
	result = applyEffort(result, config)
	result = applySpeed(result, config)
	return result, nil
}

// isSpeedOnly returns true khi config CHỈ có Speed, không có thinking mode hay effort nào.
// Ví dụ: claude-opus-4-6(fast) → Speed="fast", Mode=0, Budget=0, Level="", Effort=""
func isSpeedOnly(config thinking.ThinkingConfig) bool {
	return config.Speed != "" && config.Effort == "" && config.Mode == thinking.ModeBudget && config.Budget == 0 && config.Level == ""
}

// isEffortOnly returns true khi config CHỈ có Effort (và có thể có Speed), không có thinking mode nào.
// Ví dụ: claude-opus-4-6(max) → Effort="max", Mode=0, Budget=0, Level=""
func isEffortOnly(config thinking.ThinkingConfig) bool {
	return config.Effort != "" && config.Mode == thinking.ModeBudget && config.Budget == 0 && config.Level == ""
}

// applySpeed sets "speed" top-level field nếu config.Speed được chỉ định.
// Speed độc lập với thinking mode — có thể set speed mà không cần bật thinking.
// Fast mode (Opus 4.6+): tăng output tokens per second lên đến 2.5x.
func applySpeed(body []byte, config thinking.ThinkingConfig) []byte {
	if config.Speed == "" {
		return body
	}
	body, _ = sjson.SetBytes(body, "speed", config.Speed)
	return body
}

// applyEffort sets output_config.effort nếu config.Effort được chỉ định.
// Effort độc lập với thinking mode — có thể set effort mà không cần bật thinking.
func applyEffort(body []byte, config thinking.ThinkingConfig) []byte {
	if config.Effort == "" {
		return body
	}
	body, _ = sjson.SetBytes(body, "output_config.effort", config.Effort)
	return body
}

// normalizeClaudeBudget applies Claude-specific constraints to ensure max_tokens > budget_tokens.
// Anthropic API requires this constraint; violating it returns a 400 error.
func (a *Applier) normalizeClaudeBudget(body []byte, budgetTokens int, modelInfo *registry.ModelInfo) []byte {
	if budgetTokens <= 0 {
		return body
	}

	// Ensure the request satisfies Claude constraints:
	//  1) Determine effective max_tokens (request overrides model default)
	//  2) If budget_tokens >= max_tokens, reduce budget_tokens to max_tokens-1
	//  3) If the adjusted budget falls below the model minimum, leave the request unchanged
	//  4) If max_tokens came from model default, write it back into the request

	effectiveMax, setDefaultMax := a.effectiveMaxTokens(body, modelInfo)
	if setDefaultMax && effectiveMax > 0 {
		body, _ = sjson.SetBytes(body, "max_tokens", effectiveMax)
	}

	// Compute the budget we would apply after enforcing budget_tokens < max_tokens.
	adjustedBudget := budgetTokens
	if effectiveMax > 0 && adjustedBudget >= effectiveMax {
		adjustedBudget = effectiveMax - 1
	}

	minBudget := 0
	if modelInfo != nil && modelInfo.Thinking != nil {
		minBudget = modelInfo.Thinking.Min
	}
	if minBudget > 0 && adjustedBudget > 0 && adjustedBudget < minBudget {
		// If enforcing the max_tokens constraint would push the budget below the model minimum,
		// leave the request unchanged.
		return body
	}

	if adjustedBudget != budgetTokens {
		body, _ = sjson.SetBytes(body, "thinking.budget_tokens", adjustedBudget)
	}

	return body
}

// effectiveMaxTokens returns the max tokens to cap thinking:
// prefer request-provided max_tokens; otherwise fall back to model default.
// The boolean indicates whether the value came from the model default (and thus should be written back).
func (a *Applier) effectiveMaxTokens(body []byte, modelInfo *registry.ModelInfo) (max int, fromModel bool) {
	if maxTok := gjson.GetBytes(body, "max_tokens"); maxTok.Exists() && maxTok.Int() > 0 {
		return int(maxTok.Int()), false
	}
	if modelInfo != nil && modelInfo.MaxCompletionTokens > 0 {
		return modelInfo.MaxCompletionTokens, true
	}
	return 0, false
}

// applyCompatibleClaude xử lý thinking cho user-defined model (không có modelInfo).
// Với user-defined model, ModeAuto mặc định dùng "adaptive" (Opus 4.6+ style)
// vì không có modelInfo để kiểm tra DynamicAllowed.
func applyCompatibleClaude(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	// Speed-only: chỉ set speed, không touch thinking
	if isSpeedOnly(config) {
		return applySpeed(body, config), nil
	}

	// Effort-only: chỉ set effort + speed, không touch thinking
	if isEffortOnly(config) {
		return applySpeed(applyEffort(body, config), config), nil
	}

	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto {
		return applySpeed(body, config), nil
	}

	switch config.Mode {
	case thinking.ModeNone:
		result, _ := sjson.SetBytes(body, "thinking.type", "disabled")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		result = applyEffort(result, config)
		result = applySpeed(result, config)
		return result, nil
	case thinking.ModeAuto:
		// User-defined model: dùng adaptive (Opus 4.6+ recommended)
		result, _ := sjson.SetBytes(body, "thinking.type", "adaptive")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		result = applyEffort(result, config)
		result = applySpeed(result, config)
		return result, nil
	default:
		result, _ := sjson.SetBytes(body, "thinking.type", "enabled")
		result, _ = sjson.SetBytes(result, "thinking.budget_tokens", config.Budget)
		result = applyEffort(result, config)
		result = applySpeed(result, config)
		return result, nil
	}
}
