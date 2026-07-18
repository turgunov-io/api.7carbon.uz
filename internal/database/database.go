// Package database owns the Postgres connection and every schema-introspection
// helper the handlers rely on. Keeping the introspection caches here means the
// rules about when a lookup may be reused live next to the queries themselves.
package database

import (
	"context"
	"database/sql"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"carbon_go/internal/env"
)

// Open connects to Postgres, tunes the pool, and verifies the connection.
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}

	maxConns, err := env.ParseIntOrDefault(os.Getenv("DB_MAX_CONNS"), 15)
	if err != nil || maxConns < 1 {
		maxConns = 15
	}
	db.SetMaxOpenConns(maxConns)
	// Keep idle equal to max: the database lives in another region, so every
	// reconnect pays a fresh TLS handshake. A smaller idle pool silently closed
	// healthy connections between bursts and re-dialled them on the next request.
	db.SetMaxIdleConns(maxConns)
	// Stay under the pooler's own idle timeout so we retire connections before
	// it does and never hand a dead one to a request.
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	var pingErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		pingErr = db.PingContext(ctx)
		cancel()
		if pingErr == nil {
			break
		}
		log.Printf("database ping attempt %d/3 failed: %v", attempt, pingErr)
		time.Sleep(2 * time.Second)
	}
	if pingErr != nil {
		db.Close()
		return nil, pingErr
	}
	return db, nil
}

// NormalizeDSN fills in the connection options the Supabase transaction pooler
// requires. simple_protocol and a disabled statement cache are mandatory there:
// pgbouncer in transaction mode cannot carry prepared statements across queries.
func NormalizeDSN(dsn string) string {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}

	q := parsed.Query()
	if strings.TrimSpace(q.Get("connect_timeout")) == "" {
		q.Set("connect_timeout", "10")
	}
	if strings.TrimSpace(q.Get("sslmode")) == "" {
		q.Set("sslmode", "require")
	}
	if strings.TrimSpace(q.Get("default_query_exec_mode")) == "" {
		q.Set("default_query_exec_mode", "simple_protocol")
	}
	if strings.TrimSpace(q.Get("statement_cache_capacity")) == "" {
		q.Set("statement_cache_capacity", "0")
	}
	parsed.RawQuery = q.Encode()

	return parsed.String()
}

// QuoteIdentifier escapes a single SQL identifier.
func QuoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

// QuoteTableName escapes a possibly schema-qualified table name.
func QuoteTableName(value string) string {
	parts := strings.Split(value, ".")
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, QuoteIdentifier(strings.TrimSpace(part)))
	}
	return strings.Join(quoted, ".")
}

// BaseTableName strips the schema prefix and quoting from a table name.
func BaseTableName(value string) string {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) == 0 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(parts[len(parts)-1]), `"`)
}
