package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
)

// The DSN targets the Supabase transaction pooler with simple_protocol, so no
// query is ever server-cached and every probe costs a full cross-region
// round-trip. Schema shape does not change while the process runs, so these
// caches turn per-request introspection into a one-time cost.
var (
	cachedSchemaLookups sync.Map
	cachedTableColumns  sync.Map
	cachedQueryVariants sync.Map
)

// cachedLookup memoizes an information_schema probe for the process lifetime.
// Only successful lookups are stored, so a transient error never poisons the
// cache.
func cachedLookup[T any](key string, load func() (T, error)) (T, error) {
	if cached, ok := cachedSchemaLookups.Load(key); ok {
		return cached.(T), nil
	}
	value, err := load()
	if err != nil {
		var zero T
		return zero, err
	}
	cachedSchemaLookups.Store(key, value)
	return value, nil
}

// ResolveWorkPostTable reports which of the two historical table names exists.
func ResolveWorkPostTable(ctx context.Context, db *sql.DB) (string, error) {
	return cachedLookup("resolve:work_post", func() (string, error) {
		var workPost sql.NullString
		var blogPosts sql.NullString

		err := db.QueryRowContext(
			ctx,
			`SELECT to_regclass('public.work_post')::text, to_regclass('public.blog_posts')::text`,
		).Scan(&workPost, &blogPosts)
		if err != nil {
			return "", err
		}

		if workPost.Valid && strings.TrimSpace(workPost.String) != "" {
			return "work_post", nil
		}
		if blogPosts.Valid && strings.TrimSpace(blogPosts.String) != "" {
			return "blog_posts", nil
		}
		return "", nil
	})
}

// ResolveTuningTable reports which spelling of the tuning table exists.
func ResolveTuningTable(ctx context.Context, db *sql.DB) (string, error) {
	return cachedLookup("resolve:tuning", func() (string, error) {
		var tuning sql.NullString
		var tunning sql.NullString

		err := db.QueryRowContext(
			ctx,
			`SELECT to_regclass('public.tuning')::text, to_regclass('public.tunning')::text`,
		).Scan(&tuning, &tunning)
		if err != nil {
			return "", err
		}

		if tuning.Valid && strings.TrimSpace(tuning.String) != "" {
			return "tuning", nil
		}
		if tunning.Valid && strings.TrimSpace(tunning.String) != "" {
			return "tunning", nil
		}
		return "", nil
	})
}

// HasColumn reports whether a column exists on a public table.
func HasColumn(ctx context.Context, db *sql.DB, tableName, columnName string) (bool, error) {
	return cachedLookup("column:"+tableName+"."+columnName, func() (bool, error) {
		var exists bool
		err := db.QueryRowContext(
			ctx,
			`SELECT EXISTS (
				SELECT 1
				FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = $1
				  AND column_name = $2
			)`,
			tableName,
			columnName,
		).Scan(&exists)
		if err != nil {
			return false, err
		}
		return exists, nil
	})
}

// HasTable reports whether a table exists in the public schema.
func HasTable(ctx context.Context, db *sql.DB, tableName string) (bool, error) {
	return cachedLookup("table:"+tableName, func() (bool, error) {
		var exists bool
		err := db.QueryRowContext(
			ctx,
			`SELECT EXISTS (
				SELECT 1
				FROM information_schema.tables
				WHERE table_schema = 'public'
				  AND table_name = $1
			)`,
			tableName,
		).Scan(&exists)
		if err != nil {
			return false, err
		}
		return exists, nil
	})
}

// TableColumns returns the column set of a public table, cached per table.
func TableColumns(ctx context.Context, db *sql.DB, tableName string) (map[string]struct{}, error) {
	if cached, ok := cachedTableColumns.Load(tableName); ok {
		return cached.(map[string]struct{}), nil
	}

	baseTable := BaseTableName(tableName)
	if baseTable == "" {
		return nil, fmt.Errorf("invalid table name %q", tableName)
	}

	rows, err := db.QueryContext(
		ctx,
		`SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = $1`,
		baseTable,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := map[string]struct{}{}
	for rows.Next() {
		var columnName string
		if err := rows.Scan(&columnName); err != nil {
			return nil, err
		}
		columns[columnName] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	cachedTableColumns.Store(tableName, columns)
	return columns, nil
}

// QuerySchemaVariant runs the first query in a compatibility ladder that the
// live schema accepts. The winning index is cached per key, so later requests go
// straight to it instead of re-paying a failed round-trip for every earlier
// variant. If the cached variant ever stops working the ladder is retried from
// the top, which keeps the fallback behaviour intact across a schema change.
func QuerySchemaVariant(ctx context.Context, db *sql.DB, key string, queries []string) (*sql.Rows, error) {
	if cached, ok := cachedQueryVariants.Load(key); ok {
		if index, valid := cached.(int); valid && index < len(queries) {
			rows, err := db.QueryContext(ctx, queries[index])
			if err == nil {
				return rows, nil
			}
			cachedQueryVariants.Delete(key)
			log.Printf("%s: cached query variant %d stopped working, re-probing: %v", key, index, err)
		}
	}

	var lastErr error
	for index, query := range queries {
		rows, err := db.QueryContext(ctx, query)
		if err == nil {
			cachedQueryVariants.Store(key, index)
			return rows, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no query variants configured")
	}
	return nil, lastErr
}
