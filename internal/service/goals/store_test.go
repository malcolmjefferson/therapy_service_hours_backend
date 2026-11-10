package goals_test

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/malcolmjefferson/therapy_service_hours_backend/db/migrations"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/migrate"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/goals"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/testpg"
)

// S3-03 end-to-end tests: the real service and GoalStore against PostgreSQL with the real migrations
// (GO-17, GO-72). Users are fictional pseudonyms (CLAUDE.md §0.7).

var server *testpg.Server

func TestMain(m *testing.M) { os.Exit(testpg.Main(m, &server)) }

type env struct {
	t     *testing.T
	conn  *pgx.Conn
	svc   *goals.Service
	store *db.GoalStore
}

func newEnv(t *testing.T) *env {
	t.Helper()
	database := server.NewDatabase(t)
	pool, err := db.Open(t.Context(), db.Settings{
		Host: server.Host, Port: server.Port, Database: database, User: server.User,
		Mode: db.AuthPassword, Password: server.Password,
	}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	sqlDB := stdlib.OpenDBFromPool(pool)
	res := migrate.Run(t.Context(), sqlDB, migrations.FS, migrate.Options{})
	_ = sqlDB.Close()
	if res.Status != migrate.StatusOK {
		t.Fatalf("migrate: %+v", res)
	}
	store := db.NewGoalStore(pool)
	svc, err := goals.New(store)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, conn: server.Connect(t, database), svc: svc, store: store}
}

// user inserts a user (the trigger gives them the four default goals) and returns them as an actor.
func (e *env) user(email string) domain.Actor {
	e.t.Helper()
	var id, org string
	err := e.conn.QueryRow(e.t.Context(),
		`INSERT INTO users (organization_id, email) SELECT id, $1 FROM organizations WHERE name = 'Default Organization' RETURNING id::text, organization_id::text`,
		email).Scan(&id, &org)
	if err != nil {
		e.t.Fatalf("insert user: %v", err)
	}
	return domain.Actor{ID: id, OrganizationID: org, Email: email, Active: true}
}

// entry inserts a legacy (no times) entry in the given category position, status and minutes.
func (e *env) entry(actor domain.Actor, sortOrder int, status string, minutes int, onSite bool) {
	e.t.Helper()
	_, err := e.conn.Exec(e.t.Context(),
		`INSERT INTO entries (user_id, session_date, duration_minutes, category_id, on_site, status, needs_time)
		 SELECT $1::uuid, DATE '2026-10-01', $2, c.id, $3, $4, true FROM categories c WHERE c.sort_order = $5`,
		actor.ID, minutes, onSite, status, sortOrder)
	if err != nil {
		e.t.Fatalf("insert entry: %v", err)
	}
}

func targetOf(t *testing.T, cards []goals.Card, g domain.GoalType) *int32 {
	t.Helper()
	for _, c := range cards {
		if c.Type == g {
			return c.TargetHours
		}
	}
	t.Fatalf("goal %s missing", g)
	return nil
}

func TestStore_ProgressMatchesTheHandComputedFixtureAndDefaults(t *testing.T) {
	e := newEnv(t)
	me := e.user("progress-a@example.test")
	e.entry(me, 1, "approved", 90, true)   // Individual Counseling, direct
	e.entry(me, 2, "approved", 60, false)  // Group Facilitation, direct
	e.entry(me, 1, "pending", 45, true)    // direct
	e.entry(me, 1, "rejected", 120, true)  // excluded
	e.entry(me, 11, "approved", 75, false) // Group Supervision
	e.entry(me, 12, "approved", 60, false) // Individual Supervision
	e.entry(me, 12, "pending", 45, false)  // Individual Supervision
	e.entry(me, 6, "approved", 30, true)   // Writing Progress Notes, indirect
	cards, err := e.svc.Progress(t.Context(), me)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 4 {
		t.Fatalf("cards = %+v", cards)
	}
	// Direct: approved 150 min = 2.5 h, pending 45 min = 0.75 h, of 1200 h.
	d := cards[0]
	if *d.TargetHours != 1200 || d.Hours.Approved != 2.5 || d.Hours.Pending != 0.75 || *d.ApprovedPercent != 0 {
		t.Errorf("direct = %+v", d)
	}
	// Supervision: approved 135 min = 2.25 h (group 1.25 + individual 1), pending 45 min = 0.75 h, of 100 h.
	s := cards[1]
	if *s.TargetHours != 100 || s.Hours.Approved != 2.25 || s.Hours.Pending != 0.75 || *s.ApprovedPercent != 2 ||
		s.SupervisionGroup.Approved != 1.25 || s.SupervisionIndividual.Approved != 1 || s.SupervisionIndividual.Pending != 0.75 {
		t.Errorf("supervision = %+v / %+v / %+v", s, s.SupervisionGroup, s.SupervisionIndividual)
	}
	// Total: approved 90+60+75+60+30 = 315 min = 5.25 h; pending 90 min = 1.5 h.
	if a := cards[2]; *a.TargetHours != 3000 || a.Hours.Approved != 5.25 || a.Hours.Pending != 0.75+0.75 {
		t.Errorf("total = %+v", a)
	}
	// On-site: approved 90+30 = 120 min = 2 h; pending 45 min = 0.75 h; no target.
	if o := cards[3]; o.TargetHours != nil || o.Hours.Approved != 2 || o.Hours.Pending != 0.75 {
		t.Errorf("on-site = %+v", o)
	}
}

func TestStore_SetTargetChangesOnlyTheCallersGoal(t *testing.T) {
	e := newEnv(t)
	me, other := e.user("set-a@example.test"), e.user("set-b@example.test")
	got, err := e.svc.SetTarget(t.Context(), me, domain.GoalDirect, 1500)
	if err != nil || got.Type != domain.GoalDirect || got.TargetHours == nil || *got.TargetHours != 1500 {
		t.Fatalf("SetTarget = %+v, %v", got, err)
	}
	mine, err := e.svc.Progress(t.Context(), me)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := e.svc.Progress(t.Context(), other)
	if err != nil {
		t.Fatal(err)
	}
	if *targetOf(t, mine, domain.GoalDirect) != 1500 || *targetOf(t, mine, domain.GoalSupervision) != 100 {
		t.Errorf("my targets = %+v", mine)
	}
	if *targetOf(t, theirs, domain.GoalDirect) != 1200 {
		t.Errorf("another user's direct target changed to %d", *targetOf(t, theirs, domain.GoalDirect))
	}
}

func TestStore_SetTargetKeepsTheCelebratedMilestone(t *testing.T) {
	e := newEnv(t)
	me := e.user("celebrated@example.test")
	if _, err := e.conn.Exec(t.Context(), `UPDATE goals SET last_celebrated_percent = 50 WHERE user_id = $1 AND goal_type = 'total_activity'`, me.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.SetTarget(t.Context(), me, domain.GoalTotalActivity, 2000)
	if err != nil || got.LastCelebratedPercent != 50 {
		t.Fatalf("SetTarget = %+v, %v", got, err)
	}
}

func TestStore_OnSiteHasNoTargetAndStaysNull(t *testing.T) {
	e := newEnv(t)
	me := e.user("onsite@example.test")
	if _, err := e.svc.SetTarget(t.Context(), me, domain.GoalOnSite, 100); err == nil {
		t.Fatal("setting an On-site target succeeded")
	}
	var target *int32
	if err := e.conn.QueryRow(t.Context(), `SELECT target_hours FROM goals WHERE user_id = $1 AND goal_type = 'on_site'`, me.ID).Scan(&target); err != nil || target != nil {
		t.Errorf("on-site target = %v, %v; want NULL", target, err)
	}
	// Even called past the service, the SQL itself never updates On-site.
	if _, err := e.store.SetTarget(t.Context(), me.ID, domain.GoalOnSite, 100); err == nil {
		t.Error("the store updated On-site")
	}
}

func TestStore_RefusesNonPositiveTargetsAndLeavesTheOldOne(t *testing.T) {
	e := newEnv(t)
	me := e.user("bad@example.test")
	for _, hours := range []int32{0, -1} {
		if _, err := e.svc.SetTarget(t.Context(), me, domain.GoalDirect, hours); err == nil {
			t.Fatalf("hours %d accepted", hours)
		}
	}
	cards, err := e.svc.Progress(t.Context(), me)
	if err != nil || *targetOf(t, cards, domain.GoalDirect) != 1200 {
		t.Errorf("target after refusals = %v, %v", cards, err)
	}
}

func TestStore_ProgressForAnUnknownUserIsIncomplete(t *testing.T) {
	e := newEnv(t)
	ghost := domain.Actor{ID: "00000000-0000-4000-8000-0000000000ff", OrganizationID: "00000000-0000-4000-8000-000000000001", Active: true}
	if _, err := e.svc.Progress(t.Context(), ghost); err == nil {
		t.Error("progress for a user with no goals succeeded")
	}
	if _, err := e.svc.SetTarget(t.Context(), ghost, domain.GoalDirect, 10); err == nil {
		t.Error("SetTarget for a user with no goals succeeded")
	}
}
