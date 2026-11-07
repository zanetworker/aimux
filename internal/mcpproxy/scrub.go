package mcpproxy

import (
	"regexp"
	"sort"
	"strings"
)

// redacted replaces anything that looks like a secret in text the proxy
// keeps from a server (stderr lines, error messages).
const redacted = "[redacted]"

// minSecretLen is the shortest env value treated as a secret; shorter ones
// (ports, flags, "true") are too common in ordinary text to replace.
const minSecretLen = 6

var (
	bearerToken = regexp.MustCompile(`(?i)\b(bearer\s+)[^\s"',;]+`)
	tokenShapes = []*regexp.Regexp{
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{6,}`),        // OpenAI, Anthropic style keys
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{6,}`),   // GitHub tokens
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{6,}`), // GitHub fine-grained tokens
	}
	// longRun is a hex or base64 run; it counts as a token only with both a
	// letter and a digit, so long identifiers and words stay readable.
	longRun = regexp.MustCompile(`[A-Za-z0-9+=_-]{32,}`)
)

// scrubber hides configured env values and token-shaped strings.
type scrubber struct {
	values []string // longest first, so a value containing another goes whole
}

// newScrubber returns a scrubber for the values of the given env maps.
func newScrubber(envs ...map[string]string) *scrubber {
	seen := map[string]bool{}
	sc := &scrubber{}
	for _, env := range envs {
		for _, v := range env {
			if len(v) >= minSecretLen && !seen[v] {
				seen[v] = true
				sc.values = append(sc.values, v)
			}
		}
	}
	sort.Slice(sc.values, func(i, j int) bool { return len(sc.values[i]) > len(sc.values[j]) })
	return sc
}

func (sc *scrubber) scrub(s string) string {
	for _, v := range sc.values {
		s = strings.ReplaceAll(s, v, redacted)
	}
	s = bearerToken.ReplaceAllString(s, "${1}"+redacted)
	for _, re := range tokenShapes {
		s = re.ReplaceAllString(s, redacted)
	}
	return longRun.ReplaceAllStringFunc(s, func(run string) string {
		if strings.ContainsAny(run, "0123456789") && strings.IndexFunc(run, isLetter) >= 0 {
			return redacted
		}
		return run
	})
}

func isLetter(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

// secretName matches environment variable names that usually hold a secret.
var secretName = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASS|AUTH|CREDENTIAL|PRIVATE|DSN)`)

// secretEnv picks, from an inherited environment (KEY=VALUE lines), the
// variables a server could echo that hold secrets: names that look secret,
// and URLs carrying credentials (scheme://user:pass@host). Everything else
// (paths, the shell, locale) stays readable in scrubbed text.
func secretEnv(environ []string) map[string]string {
	out := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || v == "" {
			continue
		}
		if secretName.MatchString(k) || (strings.Contains(v, "://") && strings.Contains(v, "@")) {
			out[k] = v
		}
	}
	return out
}
