package sensitive

import (
	"regexp"

	"github.com/BenedictKing/api-proxy/internal/config"
)

type commandRule struct {
	name  string
	regex *regexp.Regexp
}

var builtinCommandRules = []commandRule{
	{
		name:  config.DangerousCmdRuleDestructive,
		regex: regexp.MustCompile(`(?i)\brm\s+-rf(?:\s+--no-preserve-root)?\s+/(?:[ \t]*(?:$|[;&|])|\r?\n)|\bdd\s+if\s*=\s*/dev/zero\b`),
	},
	{
		name:  config.DangerousCmdRuleDownloadExecute,
		regex: regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^\r\n|]*\|\s*(?:bash|sh)\b`),
	},
	{
		name:  config.DangerousCmdRuleReverseShell,
		regex: regexp.MustCompile(`(?i)\bnc\s+-e\s+(?:/bin/)?(?:ba)?sh\b|\bbash\s+-i\s+>&?\s*/dev/tcp/`),
	},
	{
		name:  config.DangerousCmdRulePrivilegeEscalation,
		regex: regexp.MustCompile(`(?i)\bchmod\s+777\s+/etc\b|\bsudo\s+su(?:\s|$)`),
	},
	{
		name:  config.DangerousCmdRuleEnvironmentTampering,
		regex: regexp.MustCompile(`(?i)\bLD_PRELOAD\s*=\s*[^\s;]+|\bPATH\s*=\s*["']?(?:\.|/tmp(?:/[^:\s;"']*)?|/var/tmp(?:/[^:\s;"']*)?|/dev/shm(?:/[^:\s;"']*)?)(?:[:\s;"']|$)`),
	},
}
