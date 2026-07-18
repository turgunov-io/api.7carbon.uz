// Package storage wraps the Supabase Storage REST API and the path handling
// that keeps caller-supplied object names safe.
package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// httpClient is shared across calls so connections are pooled and kept alive.
// A fresh client per request opened a new TLS connection every time and leaked
// idle connections until GC.
var httpClient = &http.Client{Timeout: 20 * time.Second}

// Config holds the Supabase project URL and the service-role key used to reach
// the storage API.
type Config struct {
	BaseURL       string
	ServiceRole   string
	DefaultBucket string
}

// ListOptions describes a page of objects to fetch from a bucket.
type ListOptions struct {
	Prefix     string
	Limit      int
	Offset     int
	SortColumn string
	SortOrder  string
	Search     string
}

// LoadConfig reads the Supabase storage settings from the environment.
func LoadConfig() (Config, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("SUPABASE_URL")), "/")
	serviceRole := strings.TrimSpace(os.Getenv("SUPABASE_SERVICE_ROLE_KEY"))
	defaultBucket := strings.TrimSpace(os.Getenv("STORAGE_BUCKET"))
	if defaultBucket == "" {
		defaultBucket = "cars"
	}

	if baseURL == "" {
		return Config{}, errors.New("SUPABASE_URL is not set")
	}
	if serviceRole == "" {
		return Config{}, errors.New("SUPABASE_SERVICE_ROLE_KEY is not set")
	}

	return Config{
		BaseURL:       baseURL,
		ServiceRole:   serviceRole,
		DefaultBucket: defaultBucket,
	}, nil
}

// ListBuckets returns the sorted names of every bucket in the project.
func ListBuckets(ctx context.Context, cfg Config) ([]string, error) {
	listURL := fmt.Sprintf("%s/storage/v1/bucket", strings.TrimRight(cfg.BaseURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build buckets request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.ServiceRole)
	req.Header.Set("apikey", cfg.ServiceRole)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("buckets request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("buckets request status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var payload []map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode buckets response: %w", err)
	}

	buckets := make([]string, 0, len(payload))
	for _, bucket := range payload {
		name, _ := bucket["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		buckets = append(buckets, name)
	}
	sort.Strings(buckets)
	return buckets, nil
}

// ListBucketFiles returns one page of objects from a bucket.
func ListBucketFiles(ctx context.Context, cfg Config, bucket string, opts ListOptions) (any, error) {
	payload := map[string]any{
		"prefix": opts.Prefix,
		"limit":  opts.Limit,
		"offset": opts.Offset,
		"sortBy": map[string]any{
			"column": opts.SortColumn,
			"order":  opts.SortOrder,
		},
	}
	if opts.Search != "" {
		payload["search"] = opts.Search
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("build list payload: %w", err)
	}

	listURL := fmt.Sprintf("%s/storage/v1/object/list/%s", strings.TrimRight(cfg.BaseURL, "/"), url.PathEscape(bucket))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, listURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build list request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.ServiceRole)
	req.Header.Set("apikey", cfg.ServiceRole)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list request status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	if len(raw) == 0 {
		return []any{}, nil
	}

	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("decode list response: %w", err)
	}
	return data, nil
}

// Do performs an arbitrary storage request using the shared client.
func Do(req *http.Request) (*http.Response, error) {
	return httpClient.Do(req)
}

// CleanBucket validates a bucket name supplied by a caller.
func CleanBucket(value string) (string, error) {
	bucket := strings.TrimSpace(value)
	if bucket == "" {
		return "", errors.New("bucket is required")
	}
	if strings.ContainsAny(bucket, `/\`) || strings.Contains(bucket, "..") {
		return "", errors.New("invalid bucket")
	}
	return bucket, nil
}

// CleanOptionalPath validates a path, allowing it to be absent.
func CleanOptionalPath(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	return CleanPath(trimmed)
}

// CleanPath normalises a storage path and rejects traversal attempts.
func CleanPath(value string) (string, error) {
	candidate := strings.TrimSpace(strings.ReplaceAll(value, `\`, `/`))
	candidate = strings.Trim(candidate, "/")
	if candidate == "" {
		return "", errors.New("path is required")
	}

	parts := strings.Split(candidate, "/")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		clean := strings.TrimSpace(part)
		if clean == "" {
			continue
		}
		if clean == "." || clean == ".." {
			return "", errors.New("invalid path")
		}
		result = append(result, clean)
	}
	if len(result) == 0 {
		return "", errors.New("path is required")
	}

	return strings.Join(result, "/"), nil
}

// SanitizeFilename reduces an uploaded name to a safe base filename.
func SanitizeFilename(value string) string {
	name := strings.TrimSpace(strings.ReplaceAll(value, `\`, `/`))
	if name == "" {
		return ""
	}
	parts := strings.Split(name, "/")
	base := strings.TrimSpace(parts[len(parts)-1])
	base = strings.Trim(base, ".")
	if base == "" || base == "." || base == ".." {
		return ""
	}
	return base
}

// ObjectURL builds the authenticated URL for an object.
func ObjectURL(baseURL, bucket, objectPath string) string {
	return fmt.Sprintf(
		"%s/storage/v1/object/%s/%s",
		strings.TrimRight(baseURL, "/"),
		url.PathEscape(bucket),
		EncodePath(objectPath),
	)
}

// PublicURL builds the public URL for an object.
func PublicURL(baseURL, bucket, objectPath string) string {
	return fmt.Sprintf(
		"%s/storage/v1/object/public/%s/%s",
		strings.TrimRight(baseURL, "/"),
		url.PathEscape(bucket),
		EncodePath(objectPath),
	)
}

// EncodePath percent-escapes each segment of a storage path.
func EncodePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
