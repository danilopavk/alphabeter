package db_test

import (
	"context"
	"path/filepath"
	"strconv"
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

// Kids added before URLs used slugs get one from their name, unique per parent.
func TestMigrateGivesExistingKidsSlugs(t *testing.T) {
	pool := testutil.Postgres(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Back to the schema before the slug migration.
	exec(`DELETE FROM schema_migrations WHERE version = 'migrations/003_kid_slugs.sql'`)
	exec(`ALTER TABLE kids DROP COLUMN slug`)
	exec(`CREATE INDEX kids_user_idx ON kids (user_id)`)

	var parent, other int64
	pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ('a@example.com', 'x') RETURNING id`).Scan(&parent)
	pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ('b@example.com', 'x') RETURNING id`).Scan(&other)
	names := []struct {
		user int64
		name string
	}{
		{parent, "Mia"}, {parent, "Ana Marija"}, {parent, "O'Brien!!"}, {parent, "!!!"}, {parent, "Đorđe"},
		{parent, "mia"}, {other, "Mia"},
	}
	ids := make([]int64, len(names))
	for i, n := range names {
		pool.QueryRow(ctx, `INSERT INTO kids (user_id, name, state) VALUES ($1, $2, '\x00') RETURNING id`,
			n.user, n.name).Scan(&ids[i])
	}

	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	want := []string{"mia", "ana-marija", "o-brien", "kid", "đorđe", "mia-" + strconv.FormatInt(ids[5], 10), "mia"}
	for i, id := range ids {
		var slug string
		if err := pool.QueryRow(ctx, `SELECT slug FROM kids WHERE id = $1`, id).Scan(&slug); err != nil {
			t.Fatal(err)
		}
		if slug != want[i] {
			t.Errorf("%q got slug %q, want %q", names[i].name, slug, want[i])
		}
	}
	_, err := pool.Exec(ctx, `INSERT INTO kids (user_id, name, slug, state) VALUES ($1, 'MIA', 'mia', '\x00')`, parent)
	if err == nil {
		t.Error("a parent's second kid with slug mia was accepted")
	}
}
