package tagteam

import (
	"os"
	"sort"
	"strings"
)

const redactedSecret = "[REDACTED]"

func redactSecrets(text string) string {
	return redactSecretsWithOverlay(text, nil)
}

func redactSecretsWithOverlay(text string, overlay map[string]string) string {
	if text == "" {
		return ""
	}
	replacements := secretValuesFromEnv(overlay)
	if len(replacements) == 0 {
		return text
	}
	redacted := text
	for _, secret := range replacements {
		redacted = strings.ReplaceAll(redacted, secret, redactedSecret)
	}
	return redacted
}

func secretValuesFromEnv(overlay map[string]string) []string {
	values := []string{}
	seen := map[string]bool{}
	add := func(key, value string) {
		value = strings.TrimSpace(value)
		if value == "" || !isSensitiveEnvKey(key) || seen[value] {
			return
		}
		seen[value] = true
		values = append(values, value)
	}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			add(key, value)
		}
	}
	for key, value := range overlay {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		add(key, value)
	}
	sort.Slice(values, func(i, j int) bool {
		return len(values[i]) > len(values[j])
	})
	return values
}

func isSensitiveEnvKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	if upper == "" {
		return false
	}
	sensitiveParts := []string{
		"API_KEY",
		"AUTH_TOKEN",
		"ACCESS_TOKEN",
		"REFRESH_TOKEN",
		"SECRET",
		"PASSWORD",
		"PRIVATE_KEY",
	}
	for _, part := range sensitiveParts {
		if strings.Contains(upper, part) {
			return true
		}
	}
	return false
}
