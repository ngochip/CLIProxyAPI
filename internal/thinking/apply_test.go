package thinking

import "testing"

func TestParseSuffixToConfig_ThreePartCompoundSuffix(t *testing.T) {
	tests := []struct {
		name      string
		rawSuffix string
		want      ThinkingConfig
	}{
		{
			name:      "auto high fast",
			rawSuffix: "auto+high+fast",
			want: ThinkingConfig{
				Mode:   ModeAuto,
				Budget: -1,
				Effort: "high",
				Speed:  "fast",
			},
		},
		{
			name:      "auto max fast",
			rawSuffix: "auto+max+fast",
			want: ThinkingConfig{
				Mode:   ModeAuto,
				Budget: -1,
				Effort: "max",
				Speed:  "fast",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSuffixToConfig(tt.rawSuffix, "claude", "claude-opus-4-6")
			if got.Mode != tt.want.Mode {
				t.Fatalf("Mode = %v, want %v", got.Mode, tt.want.Mode)
			}
			if got.Budget != tt.want.Budget {
				t.Fatalf("Budget = %d, want %d", got.Budget, tt.want.Budget)
			}
			if got.Effort != tt.want.Effort {
				t.Fatalf("Effort = %q, want %q", got.Effort, tt.want.Effort)
			}
			if got.Speed != tt.want.Speed {
				t.Fatalf("Speed = %q, want %q", got.Speed, tt.want.Speed)
			}
		})
	}
}
