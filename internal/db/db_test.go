package db_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/danilopavk/alphabeter/internal/db"
	"github.com/danilopavk/alphabeter/internal/testutil"
)

func TestMigrateCreatesSchemaAndIsIdempotent(t *testing.T) {
	pool := testutil.Postgres(t) // already migrated once
	ctx := context.Background()

	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	for _, table := range []string{"users", "refresh_tokens", "kids", "answer_log"} {
		var exists bool
		err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&exists)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %s missing", table)
		}
	}

	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(files) {
		t.Errorf("schema_migrations rows = %d, want %d (one per migration file)", applied, len(files))
	}

	var hasColumn bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = 'users' AND column_name = 'email_verified_at')`).Scan(&hasColumn); err != nil {
		t.Fatal(err)
	}
	if !hasColumn {
		t.Error("users.email_verified_at missing")
	}
}
