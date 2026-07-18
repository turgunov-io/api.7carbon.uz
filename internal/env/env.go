// Package env holds the small helpers used to read and interpret process
// configuration. It deliberately depends on nothing else in the project so that
// every other package can import it without creating a cycle.
package env

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// LoadDotEnv reads a local .env file when one is present. A missing file is not
// an error: deployed containers rely on injected environment variables instead.
func LoadDotEnv() error {
	if _, err := os.Stat(".env"); err != nil {
		return nil
	}
	return godotenv.Load()
}

// FirstNonEmpty returns the first value that is not blank once trimmed.
func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ParseIntOrDefault parses raw as an int, falling back when it is blank.
func ParseIntOrDefault(raw string, fallback int) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback, nil
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	return number, nil
}

// IsTruthy reports whether value reads as an affirmative flag.
func IsTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
