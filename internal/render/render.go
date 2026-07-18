// Package render converts database values into the shapes the HTTP layer sends
// back, and writes JSON responses. It is shared by every handler and depends on
// nothing else in the project.
package render

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

// JSON writes payload as a JSON response with the given status code.
func JSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}

// NullableString maps a NULL column to a nil pointer so it marshals as JSON null.
func NullableString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

// NullStringValue maps a NULL column to an empty string.
func NullStringValue(v sql.NullString) string {
	if !v.Valid {
		return ""
	}
	return strings.TrimSpace(v.String)
}

// OptionalStringDBValue returns nil for blank input so the column is stored as NULL.
func OptionalStringDBValue(input string) any {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

// OptionalStringValue returns nil for blank input so it marshals as JSON null.
func OptionalStringValue(input string) *string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// UniqueNonEmpty trims values, drops blanks, and removes duplicates while
// preserving the original order.
func UniqueNonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))

	for _, value := range values {
		clean := strings.TrimSpace(value)
		if clean == "" {
			continue
		}
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		result = append(result, clean)
	}
	return result
}

// PerformedWorks decodes the performed-works JSONB column, which has been
// written both as a plain string array and as an array of objects over time.
func PerformedWorks(raw []byte) []string {
	if len(raw) == 0 {
		return []string{}
	}

	var asStrings []string
	if err := json.Unmarshal(raw, &asStrings); err == nil {
		return UniqueNonEmpty(asStrings...)
	}

	var asAny []any
	if err := json.Unmarshal(raw, &asAny); err != nil {
		return []string{}
	}

	works := make([]string, 0, len(asAny))
	for _, item := range asAny {
		switch v := item.(type) {
		case string:
			works = append(works, v)
		case map[string]any:
			for _, key := range []string{"step", "title", "name", "text", "description"} {
				val, ok := v[key]
				if !ok {
					continue
				}
				text, ok := val.(string)
				if ok && strings.TrimSpace(text) != "" {
					works = append(works, text)
					break
				}
			}
		}
	}

	return UniqueNonEmpty(works...)
}

// StringArray decodes a JSONB column holding a string array. Some rows store the
// array double-encoded as a JSON string, so that form is handled too.
func StringArray(raw []byte) []string {
	if len(raw) == 0 {
		return []string{}
	}

	var items []string
	if err := json.Unmarshal(raw, &items); err == nil {
		result := make([]string, 0, len(items))
		for _, item := range items {
			clean := strings.TrimSpace(item)
			if clean == "" {
				continue
			}
			result = append(result, clean)
		}
		return result
	}

	// Handle case when JSONB contains a string with encoded JSON array.
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		var nested []string
		if err := json.Unmarshal([]byte(encoded), &nested); err == nil {
			result := make([]string, 0, len(nested))
			for _, item := range nested {
				clean := strings.TrimSpace(item)
				if clean == "" {
					continue
				}
				result = append(result, clean)
			}
			return result
		}
	}

	return []string{}
}
