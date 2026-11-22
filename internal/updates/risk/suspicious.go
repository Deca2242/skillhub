package risk

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	urlPattern        = regexp.MustCompile(`(?i)\b(?:https?|ftp|wss?)://[^\s<>"'\)\]]+`)
	bareDomainPattern = regexp.MustCompile(`\b[a-zA-Z0-9-]+(?:\.[a-zA-Z0-9-]+)*\.[a-zA-Z]{2,24}\b`)
	bareIPPattern     = regexp.MustCompile(`\b(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})(?::\d{1,5}(?:/[^\s<>"'\)\]]*)?|/[^\s<>"'\)\]]+)`)
	localhostPattern  = regexp.MustCompile(`(?i)\blocalhost(?::\d{1,5})?(?:/[^\s<>"'\)\]]*)?\b`)

	shellPipePattern  = regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^;\n]*\|\s*(?:(?:sudo(?:\s+-[a-zA-Z0-9-]+)*|env)\s+)*(?:[a-zA-Z0-9_./-]*/)?(?:sh|bash|zsh|dash|ksh|fish|ash|python3?|perl|ruby|node|eval)\b`)
	shellChainPattern = regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^;\n]*(?:&&|;)\s*(?:(?:sudo(?:\s+-[a-zA-Z0-9-]+)*|env)\s+)*(?:[a-zA-Z0-9_./-]*/)?(?:sh|bash|zsh|dash|eval)\b`)
	shellSubPattern   = regexp.MustCompile(`(?i)\b(?:sh|bash|zsh|dash|eval)\b[^(\n]*(?:\$\(|<\()\s*(?:curl|wget)\b`)

	base64Pattern = regexp.MustCompile(`[A-Za-z0-9+/_-]{199,}={0,2}`)

	sensitivePathPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:~/|\$HOME/|/root/|/home/[^/\s]+/)?\.(?:ssh|aws|kube|gnupg)(?:/|\b)`),
		regexp.MustCompile(`(?i)(?:^|[\s"'` + "`" + `=(/*])(?:\.env(?:\.[a-zA-Z0-9_-]+)?|[a-zA-Z0-9_*-]+\.env)(?:$|[\s"'` + "`" + `),:;])`),
		regexp.MustCompile(`(?i)(?:~/|\$HOME/|/root/|/home/[^/\s]+/)?\.(?:netrc|npmrc|pypirc|git-credentials|docker/config\.json|bash_history|zsh_history)\b`),
		regexp.MustCompile(`(?i)(?:~/|\$HOME/|/root/|/home/[^/\s]+/)?\.config/gh/hosts\.yml\b`),
		regexp.MustCompile(`(?i)(?:~/|\$HOME/|/root/|/home/[^/\s]+/)?\.(?:codex|claude|gemini|cursor)/(?:auth\.json|credentials\.json|\.credentials\.json|oauth_creds\.json)\b`),
		regexp.MustCompile(`(?i)\b(?:credentials|secrets|service-account)\.(?:json|ya?ml|toml)\b`),
		regexp.MustCompile(`(?i)\b(?:id_rsa|id_ed25519|id_ecdsa|id_dsa|authorized_keys)\b`),
		regexp.MustCompile(`(?i)/etc/(?:passwd|shadow|sudoers)\b`),
	}

	tokenEnvVarPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\$\{?[a-z][a-z0-9_]*_(?:token|key|secret|password|passwd|credential|credentials|auth|pat|access_key_id)\b\}?`),
		regexp.MustCompile(`(?i)\b(?:process\.env|os\.environ|os\.getenv|printenv)\b[^\n]*\b(?:[a-z][a-z0-9_]*_)?(?:token|api_key|secret_key|access_key|access_key_id|private_key|auth_token|bearer_token|secret|password|pat|stripe_key)\b`),
		regexp.MustCompile(`(?i)\b(?:[a-z][a-z0-9_]*_)?(?:token|api_key|secret_key|access_key|access_key_id|private_key|auth_token|bearer_token|secret|password|passwd|pat|stripe_key)=[^\s]`),
		regexp.MustCompile(`\b[A-Z][A-Z0-9_]*(?:_(?:TOKEN|API_KEY|SECRET_KEY|ACCESS_KEY|ACCESS_KEY_ID|PRIVATE_KEY|AUTH_TOKEN|BEARER_TOKEN|SECRET|PASSWORD|PAT|STRIPE_KEY))\b`),
		regexp.MustCompile(`\b(?:sk-(?:proj-|ant-)?[A-Za-z0-9_-]{12,}|ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[bp]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16})\b`),
		regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/-]{8,}=*`),
	}

	injectionReplacer = strings.NewReplacer("*", " ", "_", " ", "-", " ")

	injectionPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:ignore|disregard|forget|override|skip|ignora|olvida|anula)\b(?:\s+[a-z0-9']+){0,3}\s+(?:(?:previous|prior|above|earlier|preceding|safety|security|anteriores|previas)\s+(?:instructions?|prompts?|rules?|directions?|guidelines?|constraints?|context)|(?:the\s+)?(?:instructions?|prompts?|rules?|directions?|guidelines?|context)\s+(?:above|below)|(?:las\s+)?instrucciones\s+(?:anteriores|previas)|everything\b|all\s+(?:of\s+)?the\s+above\b|todo\s+lo\s+anterior\b)`),
		regexp.MustCompile(`(?i)\bbypass\s+(?:all\s+|any\s+)?(?:safety|security|permission|approval|confirmation|guardrails|restrictions|filters)\b`),
		regexp.MustCompile(`(?i)\b(?:do\s+not|don't|never)\s+(?:tell|inform|mention|report|show|notify)\s+(?:the\s+)?user\b`),
		regexp.MustCompile(`(?i)\bwithout\s+(?:telling|informing|notifying)\s+(?:the\s+)?user\b`),
		regexp.MustCompile(`(?i)\bhide\s+(?:this|that|it)\s+from\s+(?:the\s+)?user\b`),
		regexp.MustCompile(`(?i)\byou\s+are\s+now\s+(?:in\s+)?(?:developer|unrestricted|jailbreak|god|admin)\s+mode\b`),
		regexp.MustCompile(`(?i)\bact\s+as\s+if\s+you\s+have\s+no\s+restrictions\b`),
		regexp.MustCompile(`(?i)(?:<\s*/?\s*system\s*>|\[\s*system\s*\]|\bsystem\s+prompt\s+override\b|\bnew\s+system\s+prompt\b)`),
		regexp.MustCompile(`(?i)\bexfiltrate\b`),
		regexp.MustCompile(`(?i)\bsend\s+(?:the\s+|all\s+)?(?:contents|secrets|tokens|keys|credentials)\s+to\b`),
	}

	knownNonTLDExtensions = stringSet(
		"cjs", "conf", "cfg", "cpp", "css", "dll", "doc", "docx", "dylib",
		"env", "exe", "gif", "go", "hpp", "html", "ini", "jar", "java",
		"jpeg", "jpg", "js", "json", "jsx", "lock", "log", "mjs", "mts",
		"pdf", "png", "ppt", "pptx", "rb", "rst", "scss", "sql", "svg",
		"tar", "toml", "tsx", "txt", "wasm", "webp", "xls", "xlsx", "xml",
		"yaml", "yml",
	)
)

func stringSet(names ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

func hasSuspiciousText(files []AddedFileLines, knownPaths map[string]struct{}) bool {
	for _, f := range files {
		for _, line := range f.Lines {
			if isSuspiciousLine(line, knownPaths) {
				return true
			}
		}
	}
	return false
}

func isSuspiciousLine(line string, knownPaths map[string]struct{}) bool {
	if line == "" {
		return false
	}
	return urlPattern.MatchString(line) ||
		hasBareIP(line) ||
		localhostPattern.MatchString(line) ||
		hasBareDomain(line, knownPaths) ||
		hasShellFetch(line) ||
		hasLongBase64(line) ||
		matchesAny(sensitivePathPatterns, line) ||
		matchesAny(tokenEnvVarPatterns, line) ||
		hasInstructionOverride(line)
}

func hasBareDomain(line string, knownPaths map[string]struct{}) bool {
	for _, m := range bareDomainPattern.FindAllString(line, -1) {
		if matchesKnownPath(m, knownPaths) {
			continue
		}
		dot := strings.LastIndexByte(m, '.')
		if dot < 0 {
			continue
		}
		if isExemptNonTLDLabel(m[dot+1:]) {
			continue
		}
		return true
	}
	return false
}

func matchesKnownPath(candidate string, knownPaths map[string]struct{}) bool {
	_, ok := knownPaths[candidate]
	return ok
}

func isExemptNonTLDLabel(label string) bool {
	if isRealTLD(label) {
		return false
	}
	if label != strings.ToLower(label) {
		return true
	}
	_, isExt := knownNonTLDExtensions[label]
	return isExt
}

func isRealTLD(label string) bool {
	if isMixedCase(label) {
		return false
	}
	_, ok := ianaTLDs[strings.ToLower(label)]
	return ok
}

func isMixedCase(s string) bool {
	return s != strings.ToLower(s) && s != strings.ToUpper(s)
}

func hasBareIP(line string) bool {
	for _, m := range bareIPPattern.FindAllStringSubmatch(line, -1) {
		if isValidIPv4Octets(m[1:5]) {
			return true
		}
	}
	return false
}

func isValidIPv4Octets(octets []string) bool {
	for _, raw := range octets {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

func hasShellFetch(line string) bool {
	return shellPipePattern.MatchString(line) ||
		shellChainPattern.MatchString(line) ||
		shellSubPattern.MatchString(line)
}

func hasLongBase64(line string) bool {
	for _, m := range base64Pattern.FindAllString(line, -1) {
		if len(m) > 200 && countAlnum(m) > 100 {
			return true
		}
	}
	return false
}

func countAlnum(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		isDigit := c >= '0' && c <= '9'
		isUpper := c >= 'A' && c <= 'Z'
		isLower := c >= 'a' && c <= 'z'
		if isDigit || isUpper || isLower {
			n++
		}
	}
	return n
}

func hasInstructionOverride(line string) bool {
	normalized := injectionReplacer.Replace(line)
	return matchesAny(injectionPatterns, normalized)
}

func matchesAny(patterns []*regexp.Regexp, line string) bool {
	for _, p := range patterns {
		if p.MatchString(line) {
			return true
		}
	}
	return false
}
