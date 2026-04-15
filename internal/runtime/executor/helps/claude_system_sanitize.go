package helps

// ThirdPartyIdentityPrefixes lists identity preamble prefixes used by known
// third-party clients. Any paragraph whose trimmed text starts with one of
// these is removed during OAuth prompt sanitization (Stage 1).
var ThirdPartyIdentityPrefixes = []string{
	"You are a personal assistant operating inside OpenClaw",
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
// remaining text after paragraph removal (Stage 3). Only specific branded
// phrases are targeted — file paths and tool names are left intact.
type ThirdPartyTextReplacement struct {
	Match       string
	Replacement string
}

// ThirdPartyTextReplacements lists inline text replacements applied after
// paragraph filtering. These target agent-role brand mentions without
// affecting file paths like ~/.openclaw/workspace/.
var ThirdPartyTextReplacements = []ThirdPartyTextReplacement{
	{"operating inside OpenClaw", "operating as the assistant"},
	{"OpenClaw Agent Framework", "Agent Framework"},
	{"OpenClaw CLI Quick Reference", "CLI Quick Reference"},
	{"OpenClaw docs:", "Docs:"},
	{"OpenClaw behavior, commands, config", "agent behavior, commands, config"},
	{"if OpenCode honestly", "if the assistant honestly"},
}
