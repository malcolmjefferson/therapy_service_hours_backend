package migrate_test

import (
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/malcolmjefferson/therapy_service_hours_backend/db/migrations"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/migrate"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

// S3-01 tests: the goals table, its constraints and the defaults every user gets. Fixtures are fictional.

type goalRow struct {
	target    *int
	celebrate int
}

func intp(n int) *int { return &n }

// wantDefaults is CLAUDE.md §7.2: Direct 1200 h, Supervision 100 h, Total Activity 3000 h, On-site tracked only.
var wantDefaults = map[string]goalRow{
	"direct":         {target: intp(1200)},
	"supervision":    {target: intp(100)},
	"total_activity": {target: intp(3000)},
	"on_site":        {target: nil},
}

func goalsOf(t *testing.T, pool *pgxpool.Pool, userID string) map[string]goalRow {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT goal_type, target_hours, last_celebrated_percent FROM goals WHERE user_id = $1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]goalRow{}
	for rows.Next() {
		var typ string
		var row goalRow
		if err := rows.Scan(&typ, &row.target, &row.celebrate); err != nil {
			t.Fatal(err)
		}
		got[typ] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func wantGoals(t *testing.T, pool *pgxpool.Pool, userID string, want map[string]goalRow) {
	t.Helper()
	got := goalsOf(t, pool, userID)
	if len(got) != len(want) {
		t.Fatalf("goals = %+v, want %+v", got, want)
	}
	for typ, w := range want {
		g, ok := got[typ]
		if !ok || (g.target == nil) != (w.target == nil) || (g.target != nil && *g.target != *w.target) || g.celebrate != w.celebrate {
			t.Errorf("goal %s = %+v, want %+v", typ, g, w)
		}
	}
}

func TestGoalsSchema_EveryNewUserGetsTheDefaults(t *testing.T) {
	pool := seedPool(t)
	for _, email := range []string{"first@example.test", "second@example.test"} {
		wantGoals(t, pool, insertUser(t, pool, email), wantDefaults)
	}
	if n := count(t, pool, `SELECT count(*) FROM goals`); n != 8 {
		t.Errorf("%d goals for two users, want 8", n)
	}
}

func TestGoalsSchema_DeactivatedAndRoleUsersAlsoGetGoals(t *testing.T) {
	pool := seedPool(t)
	_, err := pool.Exec(t.Context(), `INSERT INTO users (organization_id, email, is_admin, is_supervisor, status, deactivated_at)
		SELECT id, 'admin-supervisor@example.test', true, true, 'deactivated', now() FROM organizations`)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := pool.QueryRow(t.Context(), `SELECT id::text FROM users WHERE email = 'admin-supervisor@example.test'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	wantGoals(t, pool, id, wantDefaults)
}

func TestGoalsSchema_SeedAndFirstSignInProvisioningGetTheDefaults(t *testing.T) {
	pool := seedPool(t)
	at := time.Now().UTC()
	svc, _ := seed.New(db.NewSeedStore(pool), "dev", func() time.Time { return at })
	if _, err := svc.Run(t.Context(), seed.Request{AdminEmail: "seeded-admin@example.test"}); err != nil {
		t.Fatal(err)
	}
	var seeded string
	if err := pool.QueryRow(t.Context(), `SELECT id::text FROM users WHERE email = 'seeded-admin@example.test'`).Scan(&seeded); err != nil {
		t.Fatal(err)
	}
	wantGoals(t, pool, seeded, wantDefaults)

	_, err := pool.Exec(t.Context(), `INSERT INTO invitations (email, organization_id) SELECT 'invited@example.test', id FROM organizations`)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := db.NewIdentityStore(pool).Resolve(t.Context(), domain.Identity{Subject: "sub-1", Email: "invited@example.test"}, at)
	if err != nil {
		t.Fatal(err)
	}
	wantGoals(t, pool, actor.ID, wantDefaults)

	// Binding a user, or signing in again, never resets what the user has chosen.
	if _, err := pool.Exec(t.Context(), `UPDATE goals SET target_hours = 900, last_celebrated_percent = 50 WHERE user_id = $1 AND goal_type = 'direct'`, actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.NewIdentityStore(pool).Resolve(t.Context(), domain.Identity{Subject: "sub-1", Email: "invited@example.test"}, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE users SET is_supervisor = true WHERE id = $1`, actor.ID); err != nil {
		t.Fatal(err)
	}
	want := map[string]goalRow{}
	for typ, row := range wantDefaults {
		want[typ] = row
	}
	want["direct"] = goalRow{target: intp(900), celebrate: 50}
	wantGoals(t, pool, actor.ID, want)
}

func TestGoalsSchema_BackfillsUsersThatExistBeforeTheMigration(t *testing.T) {
	database := server.NewDatabase(t)
	conn := open(t, database)

	before := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() >= "00005" || e.IsDir() {
			continue
		}
		data, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		before[e.Name()] = &fstest.MapFile{Data: data}
	}
	if res := migrate.Run(t.Context(), conn, before, migrate.Options{LockID: 917_005}); res.Status != migrate.StatusOK || res.Version != 4 {
		t.Fatalf("migrate to 4 = %+v", res)
	}

	check := server.Connect(t, database)
	var admin, member string
	if err := check.QueryRow(t.Context(), `INSERT INTO users (organization_id, email) SELECT id, 'older-admin@example.test' FROM organizations RETURNING id::text`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	if err := check.QueryRow(t.Context(), `INSERT INTO users (organization_id, email, cognito_sub) SELECT id, 'older-member@example.test', 'older-sub' FROM organizations RETURNING id::text`).Scan(&member); err != nil {
		t.Fatal(err)
	}

	if res := migrate.Run(t.Context(), conn, migrations.FS, migrate.Options{LockID: 917_005}); res.Status != migrate.StatusOK || len(res.Applied) != 1 || res.Version != 5 {
		t.Fatalf("migrate to 5 = %+v", res)
	}
	pool := server.Connect(t, database)
	for _, id := range []string{admin, member} {
		rows, err := pool.Query(t.Context(), `SELECT goal_type, target_hours, last_celebrated_percent FROM goals WHERE user_id = $1`, id)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var typ string
			var target *int32
			var celebrated int16
			if err := rows.Scan(&typ, &target, &celebrated); err != nil {
				t.Fatal(err)
			}
			w := wantDefaults[typ]
			if (target == nil) != (w.target == nil) || (target != nil && int(*target) != *w.target) || celebrated != 0 {
				t.Errorf("backfilled %s = %v/%d, want %+v", typ, target, celebrated, w)
			}
			n++
		}
		rows.Close()
		if n != 4 {
			t.Errorf("user %s has %d goals after the migration, want 4", id, n)
		}
	}
}

func TestGoalsSchema_DefaultsAreIdempotentAndKeepChosenTargets(t *testing.T) {
	pool := seedPool(t)
	user := insertUser(t, pool, "idempotent@example.test")
	if _, err := pool.Exec(t.Context(), `UPDATE goals SET target_hours = 1500 WHERE user_id = $1 AND goal_type = 'direct'`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `SELECT insert_default_goals($1)`, user); err != nil {
		t.Fatal(err)
	}
	want := map[string]goalRow{}
	for typ, row := range wantDefaults {
		want[typ] = row
	}
	want["direct"] = goalRow{target: intp(1500)}
	wantGoals(t, pool, user, want)
}

func TestGoalsSchema_Constraints(t *testing.T) {
	pool := seedPool(t)
	user := insertUser(t, pool, "constraints@example.test")
	other := insertUser(t, pool, "other@example.test")

	// Every user already has all four goals, so each case replaces one row inside a transaction that
	// is rolled back; the constraint under test is the only thing that can reject the statement.
	cases := []struct {
		name       string
		sql        string
		args       []any
		code       string
		constraint string
	}{
		{"duplicate goal type for a user", `INSERT INTO goals (user_id, goal_type, target_hours) VALUES ($1, 'direct', 500)`, []any{user}, uniqueViolation, "goals_user_goal_type_key"},
		{"duplicate on-site goal", `INSERT INTO goals (user_id, goal_type) VALUES ($1, 'on_site')`, []any{user}, uniqueViolation, "goals_user_goal_type_key"},
		{"unknown user", `INSERT INTO goals (user_id, goal_type, target_hours) VALUES (gen_random_uuid(), 'direct', 500)`, nil, foreignKeyViolated, "goals_user_id_fkey"},
		{"unknown goal type", `UPDATE goals SET goal_type = 'weekly' WHERE user_id = $1 AND goal_type = 'direct'`, []any{user}, checkViolation, "goals_goal_type_check"},
		{"zero target", `UPDATE goals SET target_hours = 0 WHERE user_id = $1 AND goal_type = 'direct'`, []any{user}, checkViolation, "goals_target_positive_chk"},
		{"negative target", `UPDATE goals SET target_hours = -5 WHERE user_id = $1 AND goal_type = 'supervision'`, []any{user}, checkViolation, "goals_target_positive_chk"},
		{"on-site target", `UPDATE goals SET target_hours = 50 WHERE user_id = $1 AND goal_type = 'on_site'`, []any{user}, checkViolation, "goals_target_presence_chk"},
		{"target cleared on a goal that needs one", `UPDATE goals SET target_hours = NULL WHERE user_id = $1 AND goal_type = 'total_activity'`, []any{user}, checkViolation, "goals_target_presence_chk"},
		{"milestone that does not exist", `UPDATE goals SET last_celebrated_percent = 30 WHERE user_id = $1 AND goal_type = 'direct'`, []any{user}, checkViolation, "goals_celebrated_percent_chk"},
		{"celebrated above 100", `UPDATE goals SET last_celebrated_percent = 125 WHERE user_id = $1 AND goal_type = 'direct'`, []any{user}, checkViolation, "goals_celebrated_percent_chk"},
		{"negative celebrated", `UPDATE goals SET last_celebrated_percent = -25 WHERE user_id = $1 AND goal_type = 'direct'`, []any{user}, checkViolation, "goals_celebrated_percent_chk"},
		{"celebrating on-site", `UPDATE goals SET last_celebrated_percent = 25 WHERE user_id = $1 AND goal_type = 'on_site'`, []any{user}, checkViolation, "goals_on_site_not_celebrated_chk"},
		{"moving a goal onto another user's duplicate", `UPDATE goals SET user_id = $2 WHERE user_id = $1 AND goal_type = 'direct'`, []any{user, other}, uniqueViolation, "goals_user_goal_type_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(t.Context()) }()
			_, err = tx.Exec(t.Context(), tc.sql, tc.args...)
			wantViolation(t, err, tc.code, tc.constraint)
		})
	}
}

func TestGoalsSchema_ValidTargetsAndMilestonesAreAccepted(t *testing.T) {
	pool := seedPool(t)
	user := insertUser(t, pool, "valid-goals@example.test")
	for _, pct := range []int{0, 25, 50, 75, 100} {
		if _, err := pool.Exec(t.Context(), `UPDATE goals SET last_celebrated_percent = $2 WHERE user_id = $1 AND goal_type = 'direct'`, user, pct); err != nil {
			t.Fatalf("milestone %d: %v", pct, err)
		}
	}
	for _, target := range []int{1, 1200, 99999} {
		if _, err := pool.Exec(t.Context(), `UPDATE goals SET target_hours = $2 WHERE user_id = $1 AND goal_type = 'total_activity'`, user, target); err != nil {
			t.Fatalf("target %d: %v", target, err)
		}
	}
}

func TestGoalsSchema_GoalsGoWithTheirUser(t *testing.T) {
	pool := seedPool(t)
	gone := insertUser(t, pool, "leaving@example.test")
	kept := insertUser(t, pool, "staying@example.test")
	if _, err := pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM goals WHERE user_id = $1`, gone); n != 0 {
		t.Errorf("%d goals left for a deleted user", n)
	}
	wantGoals(t, pool, kept, wantDefaults)
}
