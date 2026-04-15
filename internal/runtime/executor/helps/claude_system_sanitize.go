package helps

import (
	"regexp"
	"strings"
)

// ThirdPartyIdentityPrefixes lists identity preamble prefixes used by known
// third-party clients. Any paragraph whose trimmed text starts with one of
// these is removed during OAuth prompt sanitization (Stage 1).
var ThirdPartyIdentityPrefixes = []string{
	"You are a personal assistant operating inside",
	"You are OpenCode",
	"You are Cline",
	"You are Roo Code",
	"You are Roo,",
	"You are an AI coding assistant, powered by",
	"You are Windsurf",
	"You are Cascade",
	"You are Augment",
	"You are Aide",
	"You are Cursor",
}

// ThirdPartyAnchorURLs lists URLs whose presence in a paragraph causes the
// entire paragraph to be removed (Stage 2). This is resilient to surrounding
// text changes — as long as the URL appears somewhere in the paragraph, the
// removal works.
var ThirdPartyAnchorURLs = []string{
	// OpenClaw
	"docs.openclaw.ai",
	"github.com/openclaw/openclaw",
	"discord.com/invite/clawd",
	"clawhub.ai",
	// OpenCode
	"github.com/anomalyco/opencode",
	"opencode.ai/docs",
	// Cline
	"github.com/cline/",
	// Roo Code
	"github.com/RooVetGit/Roo-Code",
}

// ThirdPartyTextReplacement is a single find-and-replace rule applied to
// remaining text after paragraph removal (Stage 3).
type ThirdPartyTextReplacement struct {
	Match       string
	Replacement string
}

// ThirdPartyTextReplacements lists inline text replacements applied after
// paragraph filtering (Stage 3).
var ThirdPartyTextReplacements = []ThirdPartyTextReplacement{
	{"if OpenCode honestly", "if the assistant honestly"},
}

// ThirdPartyBrandKeyword maps a case-insensitive brand keyword to its
// generic replacement. Used in Stage 4 to scrub all remaining occurrences
// including those embedded in file paths, tool names, and env vars.
type ThirdPartyBrandKeyword struct {
	Pattern     *regexp.Regexp
	Replacement string
}

// ThirdPartyBrandKeywords lists brand keywords to scrub (Stage 4).
// Order matters: longer/more-specific patterns first to avoid partial matches.
var ThirdPartyBrandKeywords = []ThirdPartyBrandKeyword{
	// OpenClaw ecosystem
	{regexp.MustCompile(`(?i)lossless-claw`), "lossless-recall"},
	{regexp.MustCompile(`(?i)clawflow`), "taskflow"},
	{regexp.MustCompile(`(?i)clawhub`), "skillhub"},
	{regexp.MustCompile(`(?i)OPENCLAW_CACHE_BOUNDARY`), "AGENT_CACHE_BOUNDARY"},
	{regexp.MustCompile(`(?i)openclaw`), "agent"},
	{regexp.MustCompile(`(?i)mcporter`), "mcp-cli"},
	{regexp.MustCompile(`(?i)clawd`), "agent-d"},
	// OpenCode ecosystem
	{regexp.MustCompile(`(?i)opencode`), "agent"},
}

// ScrubBrandKeywords replaces all brand keyword occurrences in text (Stage 4).
func ScrubBrandKeywords(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	for _, kw := range ThirdPartyBrandKeywords {
		text = kw.Pattern.ReplaceAllString(text, kw.Replacement)
	}
	return text
}
