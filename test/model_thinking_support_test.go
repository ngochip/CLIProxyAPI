package test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

func TestClaudeModelThinkingSupport(t *testing.T) {
	model := "claude-sonnet-4-5-20250929"
	
	if !util.ModelSupportsThinking(model) {
		t.Errorf("Model %s should support thinking", model)
	}
	
	if util.ModelUsesThinkingLevels(model) {
		t.Errorf("Model %s should not use thinking levels", model)
	}
	
	budget, ok := util.ThinkingEffortToBudget(model, "high")
	if !ok {
		t.Errorf("Should convert reasoning_effort='high' to budget")
	}
	if budget <= 0 {
		t.Errorf("Budget should be positive, got %d", budget)
	}
	
	t.Logf("✓ Model %s supports thinking with budget-based config", model)
	t.Logf("  effort='high' -> budget=%d", budget)
}

