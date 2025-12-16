package util

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

const (
	ThinkingBudgetMetadataKey          = "thinking_budget"
	ThinkingIncludeThoughtsMetadataKey = "thinking_include_thoughts"
	ReasoningEffortMetadataKey         = "reasoning_effort"
	ThinkingOriginalModelMetadataKey   = "thinking_original_model"
)

// modelAliases map các tên model alias sang tên model chuẩn.
// Ví dụ: "claude-4.5-sonnet-thinking" → "claude-sonnet-4-5-thinking"
// Mặc định chứa các alias built-in, có thể được override bởi config.
var (
	modelAliases   = make(map[string]string)
	aliasesMutex   sync.RWMutex
	aliasesLoaded  bool
)

// defaultModelAliases chứa các alias mặc định khi không có config.
var defaultModelAliases = map[string]string{
	// Claude aliases với format khác
	"claude-4.5-sonnet":               "claude-sonnet-4-5",
	"claude-4.5-sonnet-thinking":      "claude-sonnet-4-5-thinking",
	"claude-4.5-sonnet-thinking-low":  "claude-sonnet-4-5-thinking-low",
	"claude-4.5-sonnet-thinking-medium": "claude-sonnet-4-5-thinking-medium",
	"claude-4.5-sonnet-thinking-high": "claude-sonnet-4-5-thinking-high",
	
	"claude-4.5-opus":                 "claude-opus-4-5",
	"claude-4.5-opus-thinking":        "claude-opus-4-5-thinking",
	"claude-4.5-opus-thinking-low":    "claude-opus-4-5-thinking-low",
	"claude-4.5-opus-thinking-medium": "claude-opus-4-5-thinking-medium",
	"claude-4.5-opus-thinking-high":   "claude-opus-4-5-thinking-high",
}

// SetModelAliases cập nhật model aliases từ config.
// Gọi function này khi load config để update aliases.
func SetModelAliases(aliases map[string]string) {
	aliasesMutex.Lock()
	defer aliasesMutex.Unlock()
	
	// Start with default aliases
	modelAliases = make(map[string]string)
	for k, v := range defaultModelAliases {
		modelAliases[strings.ToLower(k)] = v
	}
	
	// Merge with config aliases (config overrides defaults)
	for k, v := range aliases {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			modelAliases[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	
	aliasesLoaded = true
}

// getModelAliases trả về copy of current aliases (thread-safe).
func getModelAliases() map[string]string {
	aliasesMutex.RLock()
	defer aliasesMutex.RUnlock()
	
	// Nếu chưa load, dùng default
	if !aliasesLoaded {
		result := make(map[string]string)
		for k, v := range defaultModelAliases {
			result[strings.ToLower(k)] = v
		}
		return result
	}
	
	// Return copy
	result := make(map[string]string)
	for k, v := range modelAliases {
		result[k] = v
	}
	return result
}

// thinkingModelAliases maps thinking model aliases to their actual upstream model names.
// Ví dụ: "claude-sonnet-4-5-thinking" → "claude-sonnet-4-5-20250929"
var thinkingModelAliases = map[string]string{
	"claude-sonnet-4-5":  "claude-sonnet-4-5-20250929",
	"claude-opus-4-5":    "claude-opus-4-5-20251101",
	"claude-sonnet-4":    "claude-sonnet-4-20250514",
	"claude-opus-4":      "claude-opus-4-20250514",
	"claude-opus-4-1":    "claude-opus-4-1-20250805",
	"claude-3-7-sonnet":  "claude-3-7-sonnet-20250219",
	"claude-3-5-sonnet":  "claude-3-5-sonnet-20241022",
	"claude-3-5-haiku":   "claude-3-5-haiku-20241022",
	"claude-3-opus":      "claude-3-opus-20240229",
	"claude-3-sonnet":    "claude-3-sonnet-20240229",
	"claude-3-haiku":     "claude-3-haiku-20240307",
}

// thinkingSuffixes định nghĩa các suffix và reasoning effort tương ứng
var thinkingSuffixes = []struct {
	suffix string
	effort string
}{
	{"-thinking-high", "high"},
	{"-high-thinking", "high"},
	{"-thinking-medium", "medium"},
	{"-medium-thinking", "medium"},
	{"-thinking-low", "low"},
	{"-low-thinking", "low"},
	{"-thinking", "medium"}, // Default thinking = medium
}

// ResolveModelAlias giải quyết alias model sang tên model chuẩn.
// Ví dụ: "claude-4.5-sonnet-thinking" → "claude-sonnet-4-5-thinking"
func ResolveModelAlias(modelName string) string {
	if modelName == "" {
		return modelName
	}
	
	// Get current aliases (thread-safe)
	aliases := getModelAliases()
	
	// Kiểm tra exact match (case-insensitive)
	lower := strings.ToLower(strings.TrimSpace(modelName))
	if resolved, ok := aliases[lower]; ok {
		return resolved
	}
	
	return modelName
}

// NormalizeThinkingModel parses dynamic thinking suffixes on model names and returns
// the normalized base model with extracted metadata. Supported patterns:
//   - "(<value>)" where value can be:
//   - A numeric budget (e.g., "(8192)", "(16384)")
//   - A reasoning effort level (e.g., "(high)", "(medium)", "(low)")
//   - "-thinking", "-thinking-low", "-thinking-medium", "-thinking-high" suffixes
//
// Examples:
//   - "claude-sonnet-4-5-20250929(16384)" → budget=16384
//   - "gpt-5.1(high)" → reasoning_effort="high"
//   - "gemini-2.5-pro(32768)" → budget=32768
//   - "claude-sonnet-4-5-thinking" → base=claude-sonnet-4-5-20250929, effort=medium
//   - "claude-opus-4-5-thinking-high" → base=claude-opus-4-5-20251101, effort=high
//   - "claude-4.5-sonnet-thinking" → base=claude-sonnet-4-5-20250929, effort=medium (alias resolved)
//
// Note: Empty parentheses "()" are not supported and will be ignored.
func NormalizeThinkingModel(modelName string) (string, map[string]any) {
	if modelName == "" {
		return modelName, nil
	}

	// Bước 1: Giải quyết alias trước
	resolvedModel := ResolveModelAlias(modelName)
	baseModel := resolvedModel

	var (
		budgetOverride  *int
		reasoningEffort *string
		matched         bool
	)

	// Kiểm tra suffix -thinking trước
	for _, ts := range thinkingSuffixes {
		if strings.HasSuffix(strings.ToLower(modelName), ts.suffix) {
			// Lấy base model prefix (phần trước suffix -thinking)
			prefix := modelName[:len(modelName)-len(ts.suffix)]

			// Tìm mapping trong thinkingModelAliases
			if actualModel, ok := thinkingModelAliases[strings.ToLower(prefix)]; ok {
				baseModel = actualModel
			} else {
				// Nếu không có alias, giữ nguyên prefix (có thể đã là model đầy đủ)
				baseModel = prefix
			}

			effort := ts.effort
			reasoningEffort = &effort
			matched = true
			break
		}
	}

	// Match "(<value>)" pattern at the end of the model name
	if !matched {
		if idx := strings.LastIndex(modelName, "("); idx != -1 {
			if !strings.HasSuffix(modelName, ")") {
				// Incomplete parenthesis, ignore
				return baseModel, nil
			}

			value := modelName[idx+1 : len(modelName)-1] // Extract content between ( and )
			if value == "" {
				// Empty parentheses not supported
				return baseModel, nil
			}

			candidateBase := modelName[:idx]

			// Auto-detect: pure numeric → budget, string → reasoning effort level
			if parsed, ok := parseIntPrefix(value); ok {
				// Numeric value: treat as thinking budget
				baseModel = candidateBase
				budgetOverride = &parsed
				matched = true
			} else {
				// String value: treat as reasoning effort level
				baseModel = candidateBase
				raw := strings.ToLower(strings.TrimSpace(value))
				if raw != "" {
					reasoningEffort = &raw
					matched = true
				}
			}
		}
	}

	if !matched {
		return baseModel, nil
	}

	metadata := map[string]any{
		ThinkingOriginalModelMetadataKey: modelName, // Lưu model name gốc từ request
	}
	
	// Nếu có alias resolution, cũng lưu lại model đã resolved
	if resolvedModel != modelName {
		metadata["resolved_model"] = resolvedModel
	}
	if budgetOverride != nil {
		metadata[ThinkingBudgetMetadataKey] = *budgetOverride
	}
	if reasoningEffort != nil {
		metadata[ReasoningEffortMetadataKey] = *reasoningEffort
	}
	return baseModel, metadata
}

// ThinkingFromMetadata extracts thinking overrides from metadata produced by NormalizeThinkingModel.
// It accepts both the new generic keys and legacy Gemini-specific keys.
func ThinkingFromMetadata(metadata map[string]any) (*int, *bool, *string, bool) {
	if len(metadata) == 0 {
		return nil, nil, nil, false
	}

	var (
		budgetPtr  *int
		includePtr *bool
		effortPtr  *string
		matched    bool
	)

	readBudget := func(key string) {
		if budgetPtr != nil {
			return
		}
		if raw, ok := metadata[key]; ok {
			if v, okNumber := parseNumberToInt(raw); okNumber {
				budget := v
				budgetPtr = &budget
				matched = true
			}
		}
	}

	readInclude := func(key string) {
		if includePtr != nil {
			return
		}
		if raw, ok := metadata[key]; ok {
			switch v := raw.(type) {
			case bool:
				val := v
				includePtr = &val
				matched = true
			case *bool:
				if v != nil {
					val := *v
					includePtr = &val
					matched = true
				}
			}
		}
	}

	readEffort := func(key string) {
		if effortPtr != nil {
			return
		}
		if raw, ok := metadata[key]; ok {
			if val, okStr := raw.(string); okStr && strings.TrimSpace(val) != "" {
				normalized := strings.ToLower(strings.TrimSpace(val))
				effortPtr = &normalized
				matched = true
			}
		}
	}

	readBudget(ThinkingBudgetMetadataKey)
	readBudget(GeminiThinkingBudgetMetadataKey)
	readInclude(ThinkingIncludeThoughtsMetadataKey)
	readInclude(GeminiIncludeThoughtsMetadataKey)
	readEffort(ReasoningEffortMetadataKey)
	readEffort("reasoning.effort")

	return budgetPtr, includePtr, effortPtr, matched
}

// ResolveThinkingConfigFromMetadata derives thinking budget/include overrides,
// converting reasoning effort strings into budgets when possible.
func ResolveThinkingConfigFromMetadata(model string, metadata map[string]any) (*int, *bool, bool) {
	budget, include, effort, matched := ThinkingFromMetadata(metadata)
	if !matched {
		return nil, nil, false
	}
	// Level-based models (OpenAI-style) do not accept numeric thinking budgets in
	// Claude/Gemini-style protocols, so we don't derive budgets for them here.
	if ModelUsesThinkingLevels(model) {
		return nil, nil, false
	}

	if budget == nil && effort != nil {
		if derived, ok := ThinkingEffortToBudget(model, *effort); ok {
			budget = &derived
		}
	}
	return budget, include, budget != nil || include != nil || effort != nil
}

// ReasoningEffortFromMetadata resolves a reasoning effort string from metadata,
// inferring "auto" and "none" when budgets request dynamic or disabled thinking.
func ReasoningEffortFromMetadata(metadata map[string]any) (string, bool) {
	budget, include, effort, matched := ThinkingFromMetadata(metadata)
	if !matched {
		return "", false
	}
	if effort != nil && *effort != "" {
		return strings.ToLower(strings.TrimSpace(*effort)), true
	}
	if budget != nil {
		switch *budget {
		case -1:
			return "auto", true
		case 0:
			return "none", true
		}
	}
	if include != nil && !*include {
		return "none", true
	}
	return "", true
}

// ThinkingEffortToBudget maps reasoning effort levels to approximate budgets,
// clamping the result to the model's supported range.
func ThinkingEffortToBudget(model, effort string) (int, bool) {
	if effort == "" {
		return 0, false
	}
	normalized, ok := NormalizeReasoningEffortLevel(model, effort)
	if !ok {
		normalized = strings.ToLower(strings.TrimSpace(effort))
	}
	switch normalized {
	case "none":
		return 0, true
	case "auto":
		return NormalizeThinkingBudget(model, -1), true
	case "minimal":
		return NormalizeThinkingBudget(model, 512), true
	case "low":
		return NormalizeThinkingBudget(model, 1024), true
	case "medium":
		return NormalizeThinkingBudget(model, 8192), true
	case "high":
		return NormalizeThinkingBudget(model, 24576), true
	case "xhigh":
		return NormalizeThinkingBudget(model, 32768), true
	default:
		return 0, false
	}
}

// ResolveOriginalModel returns the original model name stored in metadata (if present),
// otherwise falls back to the provided model.
func ResolveOriginalModel(model string, metadata map[string]any) string {
	normalize := func(name string) string {
		if name == "" {
			return ""
		}
		if base, _ := NormalizeThinkingModel(name); base != "" {
			return base
		}
		return strings.TrimSpace(name)
	}

	if metadata != nil {
		if v, ok := metadata[ThinkingOriginalModelMetadataKey]; ok {
			if s, okStr := v.(string); okStr && strings.TrimSpace(s) != "" {
				if base := normalize(s); base != "" {
					return base
				}
			}
		}
		if v, ok := metadata[GeminiOriginalModelMetadataKey]; ok {
			if s, okStr := v.(string); okStr && strings.TrimSpace(s) != "" {
				if base := normalize(s); base != "" {
					return base
				}
			}
		}
	}
	// Fallback: try to re-normalize the model name when metadata was dropped.
	if base := normalize(model); base != "" {
		return base
	}
	return model
}

func parseIntPrefix(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	digits := strings.TrimLeft(value, "-")
	if digits == "" {
		return 0, false
	}
	end := len(digits)
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			end = i
			break
		}
	}
	if end == 0 {
		return 0, false
	}
	val, err := strconv.Atoi(digits[:end])
	if err != nil {
		return 0, false
	}
	return val, true
}

func parseNumberToInt(raw any) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		if val, err := v.Int64(); err == nil {
			return int(val), true
		}
	case string:
		if strings.TrimSpace(v) == "" {
			return 0, false
		}
		if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return parsed, true
		}
	}
	return 0, false
}
