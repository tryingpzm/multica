// Package storetest opens a throwaway PostgreSQL schema with the personal agent migration applied, for tests that run
// against the real store. It reads DATABASE_URL (default: the local dev database) and skips the test when PostgreSQL is
// unreachable.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open returns a pool whose search_path is a fresh schema holding the personal agent tables; the schema is dropped on cleanup.
func Open(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("postgres unavailable: %v", err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "pa_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	for _, name := range []string{"999191_personal_agent.up.sql", "999192_personal_agent_sources.up.sql", "999193_personal_agent_execution.up.sql",
		"999194_personal_agent_panel.up.sql", "999195_personal_agent_labels.up.sql", "999196_personal_agent_source_state.up.sql", "999197_personal_agent_rule_origins.up.sql", "999199_personal_agent_language.up.sql",
		"999200_personal_agent_outbox_error.up.sql", "999201_personal_agent_plan.up.sql", "999202_personal_agent_follow.up.sql", "999203_personal_agent_outbox_discarded.up.sql"} {
		sql, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../../../migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("migrate %s: %v", name, err)
		}
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return pool
}
