// Package claude implements thinking configuration scaffolding for Claude models.
//
// Claude models support two thinking control styles:
//   - Manual thinking: thinking.type="enabled" with thinking.budget_tokens (token budget)
//   - Adaptive thinking (Claude 4.6): thinking.type="adaptive" with output_config.effort (low/medium/high/max)
//
// Some Claude models support ZeroAllowed (sonnet-4-5, opus-4-5), while older models do not.
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
// Apply processes:
//   - ModeBudget: manual thinking budget_tokens
//   - ModeLevel: adaptive thinking effort (Claude 4.6)
//   - ModeAuto: provider default adaptive/manual behavior
//   - ModeNone: disabled
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
// Expected output format for adaptive:
//
//	{
//	  "thinking": {
//	    "type": "adaptive"
//	  },
//	  "output_config": {
//	    "effort": "high"
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
	if isSpeedOnly(config) {
		if len(body) == 0 || !gjson.ValidBytes(body) {
			body = []byte(`{}`)
		}
		return applySpeed(body, config), nil
	}

	// Effort-only config: chỉ set effort + speed, không touch thinking
	if isEffortOnly(config) {
		if len(body) == 0 || !gjson.ValidBytes(body) {
			body = []byte(`{}`)
		}
		return applySpeed(applyEffort(body, config), config), nil
	}


	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	supportsAdaptive := modelInfo != nil && modelInfo.Thinking != nil && len(modelInfo.Thinking.Levels) > 0

	switch config.Mode {
	case thinking.ModeNone:
		result, _ := sjson.SetBytes(body, "thinking.type", "disabled")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		result, _ = sjson.DeleteBytes(result, "output_config.effort")
		if oc := gjson.GetBytes(result, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			result, _ = sjson.DeleteBytes(result, "output_config")
		}
		result = applySpeed(result, config)
		return result, nil

	case thinking.ModeLevel:
		// Adaptive thinking effort is only valid when the model advertises discrete levels.
		// (Claude 4.6 uses output_config.effort.)
		if supportsAdaptive && config.Level != "" {
			result, _ := sjson.SetBytes(body, "thinking.type", "adaptive")
			result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
			result, _ = sjson.SetBytes(result, "output_config.effort", string(config.Level))
			return result, nil
		}

		// Fallback for non-adaptive Claude models: convert level to budget_tokens.
		if budget, ok := thinking.ConvertLevelToBudget(string(config.Level)); ok {
			config.Mode = thinking.ModeBudget
			config.Budget = budget
			config.Level = ""
		} else {
			return body, nil
		}
		fallthrough

	case thinking.ModeBudget:
		// Budget is expected to be pre-validated by ValidateConfig (clamped, ZeroAllowed enforced).
		// Decide enabled/disabled based on budget value.
		if config.Budget == 0 {
			result, _ := sjson.SetBytes(body, "thinking.type", "disabled")
			result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
			result, _ = sjson.DeleteBytes(result, "output_config.effort")
			if oc := gjson.GetBytes(result, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
				result, _ = sjson.DeleteBytes(result, "output_config")
			}
			return result, nil
		}

		result, _ := sjson.SetBytes(body, "thinking.type", "enabled")
		result, _ = sjson.SetBytes(result, "thinking.budget_tokens", config.Budget)
		result, _ = sjson.DeleteBytes(result, "output_config.effort")
		if oc := gjson.GetBytes(result, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			result, _ = sjson.DeleteBytes(result, "output_config")
		}

		// Ensure max_tokens > thinking.budget_tokens (Anthropic API constraint).
		result = a.normalizeClaudeBudget(result, config.Budget, modelInfo)
		return result, nil

	case thinking.ModeAuto:
		// For Claude 4.6 models, auto maps to adaptive thinking with upstream defaults.
		if supportsAdaptive {
			result, _ := sjson.SetBytes(body, "thinking.type", "adaptive")
			result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
			// Explicit effort is optional for adaptive thinking; omit it to allow upstream default.
			result, _ = sjson.DeleteBytes(result, "output_config.effort")
			if oc := gjson.GetBytes(result, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
				result, _ = sjson.DeleteBytes(result, "output_config")
			}
			return result, nil
		}

		// Legacy fallback: enable thinking without specifying budget_tokens.
		result, _ := sjson.SetBytes(body, "thinking.type", "enabled")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		result, _ = sjson.DeleteBytes(result, "output_config.effort")
		if oc := gjson.GetBytes(result, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			result, _ = sjson.DeleteBytes(result, "output_config")
		}
		return result, nil

	default:
		return body, nil
	}
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
		result, _ = sjson.DeleteBytes(result, "output_config.effort")
		if oc := gjson.GetBytes(result, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			result, _ = sjson.DeleteBytes(result, "output_config")
		}
		result = applySpeed(result, config)
		return result, nil
	case thinking.ModeAuto:
		// User-defined model: dùng adaptive (Opus 4.6+ recommended)
		result, _ := sjson.SetBytes(body, "thinking.type", "adaptive")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		result, _ = sjson.DeleteBytes(result, "output_config.effort")
		if oc := gjson.GetBytes(result, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			result, _ = sjson.DeleteBytes(result, "output_config")
		}
		result = applySpeed(result, config)
		return result, nil
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		result, _ := sjson.SetBytes(body, "thinking.type", "adaptive")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		result, _ = sjson.SetBytes(result, "output_config.effort", string(config.Level))
		result = applySpeed(result, config)
		return result, nil
	default:
		result, _ := sjson.SetBytes(body, "thinking.type", "enabled")
		result, _ = sjson.SetBytes(result, "thinking.budget_tokens", config.Budget)
		result, _ = sjson.DeleteBytes(result, "output_config.effort")
		if oc := gjson.GetBytes(result, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			result, _ = sjson.DeleteBytes(result, "output_config")
		}
		result = applySpeed(result, config)
		return result, nil
	}
}
