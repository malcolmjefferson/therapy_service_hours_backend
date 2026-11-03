package migrate_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// S6-01 schema tests: the supervision rules and the notifications table are enforced by the
// database itself (ARC-21), so a bug in a later service cannot create a cross-organization,
// self or second live link. Fixtures are fictional.

func insertOrganization(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(), `INSERT INTO organizations (name) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	return id
}

func insertUserIn(t *testing.T, pool *pgxpool.Pool, orgID, email string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(t.Context(),
		`INSERT INTO users (organization_id, email) VALUES ($1::uuid, $2) RETURNING id::text`, orgID, email).Scan(&id)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func defaultOrganization(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(), `SELECT id::text FROM organizations WHERE name = 'Default Organization'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// requestLink inserts a pending link in orgID.
func requestLink(t *testing.T, pool *pgxpool.Pool, orgID, supervisee, supervisor string) (string, error) {
	t.Helper()
	var id string
	err := pool.QueryRow(t.Context(),
		`INSERT INTO supervision_links (supervisee_id, supervisor_id, organization_id) VALUES ($1::uuid, $2::uuid, $3::uuid) RETURNING id::text`,
		supervisee, supervisor, orgID).Scan(&id)
	return id, err
}

func mustRequestLink(t *testing.T, pool *pgxpool.Pool, orgID, supervisee, supervisor string) string {
	t.Helper()
	id, err := requestLink(t, pool, orgID, supervisee, supervisor)
	if err != nil {
		t.Fatalf("request link: %v", err)
	}
	return id
}

func setStatus(t *testing.T, pool *pgxpool.Pool, linkID, set string) error {
	t.Helper()
	_, err := pool.Exec(t.Context(), `UPDATE supervision_links SET `+set+` WHERE id = $1::uuid`, linkID) // nosemgrep: no-string-built-sql
	return err
}

const (
	acceptLink  = `status = 'accepted', responded_at = now()`
	declineLink = `status = 'declined', responded_at = now()`
	cancelLink  = `status = 'cancelled'`
	endLink     = `status = 'ended', ended_at = now()`
)

type linkRow struct {
	status           string
	requested        time.Time
	responded, ended *time.Time
	endedBy          *string
}

func readLink(t *testing.T, pool *pgxpool.Pool, id string) linkRow {
	t.Helper()
	var r linkRow
	err := pool.QueryRow(t.Context(),
		`SELECT status, requested_at, responded_at, ended_at, ended_by::text FROM supervision_links WHERE id = $1::uuid`, id).
		Scan(&r.status, &r.requested, &r.responded, &r.ended, &r.endedBy)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSupervisionLinks_RequestAcceptAndEndStoreTheLifecycle(t *testing.T) {
	pool := seedPool(t)
	org := defaultOrganization(t, pool)
	supervisee := insertUserIn(t, pool, org, "supervisee@example.test")
	supervisor := insertUserIn(t, pool, org, "supervisor@example.test")

	link := mustRequestLink(t, pool, org, supervisee, supervisor)
	if r := readLink(t, pool, link); r.status != "pending" || r.requested.IsZero() || r.responded != nil || r.ended != nil || r.endedBy != nil {
		t.Fatalf("new link = %+v, want pending with no response or end", r)
	}
	if err := setStatus(t, pool, link, acceptLink); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if r := readLink(t, pool, link); r.status != "accepted" || r.responded == nil {
		t.Fatalf("accepted link = %+v", r)
	}
	if err := setStatus(t, pool, link, `status = 'ended', ended_at = now(), ended_by = '`+supervisor+`'`); err != nil {
		t.Fatalf("end: %v", err)
	}
	r := readLink(t, pool, link)
	if r.status != "ended" || r.responded == nil || r.ended == nil || r.endedBy == nil || *r.endedBy != supervisor {
		t.Fatalf("ended link = %+v", r)
	}

	// History is kept and the supervisee can ask someone new (CLAUDE.md §6.2).
	other := insertUserIn(t, pool, org, "other-supervisor@example.test")
	mustRequestLink(t, pool, org, supervisee, other)
	if n := count(t, pool, `SELECT count(*) FROM supervision_links WHERE supervisee_id = $1::uuid`, supervisee); n != 2 {
		t.Errorf("links for the supervisee = %d, want the ended one kept plus the new one", n)
	}
}

func TestSupervisionLinks_SelfLinkFails(t *testing.T) {
	pool := seedPool(t)
	org := defaultOrganization(t, pool)
	user := insertUserIn(t, pool, org, "self@example.test")

	_, err := requestLink(t, pool, org, user, user)
	wantViolation(t, err, checkViolation, "supervision_links_not_self_chk")

	// Also when an existing link is edited to point at itself.
	other := insertUserIn(t, pool, org, "not-self@example.test")
	link := mustRequestLink(t, pool, org, user, other)
	wantViolation(t, setStatus(t, pool, link, `supervisor_id = supervisee_id`), checkViolation, "supervision_links_not_self_chk")
	if n := count(t, pool, `SELECT count(*) FROM supervision_links`); n != 1 {
		t.Errorf("links stored = %d, want only the valid one", n)
	}
}

func TestSupervisionLinks_SecondLiveLinkForASuperviseeFails(t *testing.T) {
	pool := seedPool(t)
	org := defaultOrganization(t, pool)
	supervisee := insertUserIn(t, pool, org, "trainee@example.test")
	first := insertUserIn(t, pool, org, "first-supervisor@example.test")
	second := insertUserIn(t, pool, org, "second-supervisor@example.test")

	link := mustRequestLink(t, pool, org, supervisee, first)
	const live = "supervision_links_one_live_per_supervisee_idx"

	// Pending blocks another request, to the same supervisor or a different one.
	_, err := requestLink(t, pool, org, supervisee, second)
	wantViolation(t, err, uniqueViolation, live)
	_, err = requestLink(t, pool, org, supervisee, first)
	wantViolation(t, err, uniqueViolation, live)

	// Accepted blocks it too.
	if err := setStatus(t, pool, link, acceptLink); err != nil {
		t.Fatal(err)
	}
	_, err = requestLink(t, pool, org, supervisee, second)
	wantViolation(t, err, uniqueViolation, live)

	// Every way a link stops being live frees the supervisee: ended after acceptance, declined or
	// cancelled while pending. The new link is live again each time.
	if err := setStatus(t, pool, link, endLink); err != nil {
		t.Fatalf("end: %v", err)
	}
	declined := mustRequestLink(t, pool, org, supervisee, second)
	if err := setStatus(t, pool, declined, declineLink); err != nil {
		t.Fatalf("decline: %v", err)
	}
	cancelled := mustRequestLink(t, pool, org, supervisee, first)
	if err := setStatus(t, pool, cancelled, cancelLink); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	mustRequestLink(t, pool, org, supervisee, first)
	if n := count(t, pool, `SELECT count(*) FROM supervision_links WHERE supervisee_id = $1::uuid AND status IN ('pending', 'accepted')`, supervisee); n != 1 {
		t.Errorf("live links = %d, want exactly 1", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM supervision_links WHERE supervisee_id = $1::uuid`, supervisee); n != 4 {
		t.Errorf("links kept for the supervisee = %d, want 4 (ended, declined, cancelled, pending)", n)
	}

	// Reviving a closed link while another one is live is refused too.
	wantViolation(t, setStatus(t, pool, cancelled, `status = 'pending'`), uniqueViolation, live)

	// A supervisor can have many supervisees, each with their own live link.
	for _, name := range []string{"a", "b", "c"} {
		mustRequestLink(t, pool, org, insertUserIn(t, pool, org, "trainee-"+name+"@example.test"), first)
	}
}

func TestSupervisionLinks_ConcurrentRequestsLeaveExactlyOneLiveLink(t *testing.T) {
	pool := seedPool(t)
	org := defaultOrganization(t, pool)
	supervisee := insertUserIn(t, pool, org, "racing-trainee@example.test")
	const requests = 8
	var supervisors [requests]string
	for i := range supervisors {
		supervisors[i] = insertUserIn(t, pool, org, "racing-supervisor-"+string(rune('a'+i))+"@example.test")
	}

	var wins, rejected atomic.Int32
	var wg sync.WaitGroup
	for i := range supervisors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := requestLink(t, pool, org, supervisee, supervisors[i]); err != nil {
				rejected.Add(1)
				return
			}
			wins.Add(1)
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || rejected.Load() != requests-1 {
		t.Errorf("%d requests succeeded and %d failed, want exactly 1 and %d", wins.Load(), rejected.Load(), requests-1)
	}
	if n := count(t, pool, `SELECT count(*) FROM supervision_links WHERE supervisee_id = $1::uuid`, supervisee); n != 1 {
		t.Errorf("links stored = %d, want 1", n)
	}
}

func TestSupervisionLinks_CrossOrganizationInsertFails(t *testing.T) {
	pool := seedPool(t)
	home := defaultOrganization(t, pool)
	away := insertOrganization(t, pool, "Other Clinic")
	supervisee := insertUserIn(t, pool, home, "home-trainee@example.test")
	homeSupervisor := insertUserIn(t, pool, home, "home-supervisor@example.test")
	awaySupervisor := insertUserIn(t, pool, away, "away-supervisor@example.test")
	awayTrainee := insertUserIn(t, pool, away, "away-trainee@example.test")

	for _, tc := range []struct {
		name                        string
		org, supervisee, supervisor string
		constraint                  string
	}{
		{"supervisor in another organization", home, supervisee, awaySupervisor, "supervision_links_supervisor_organization_chk"},
		{"supervisee in another organization", home, awayTrainee, homeSupervisor, "supervision_links_supervisee_organization_chk"},
		{"link organization matches neither person", away, supervisee, homeSupervisor, "supervision_links_supervisee_organization_chk"},
		{"link organization matches only the supervisor", away, supervisee, awaySupervisor, "supervision_links_supervisee_organization_chk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := requestLink(t, pool, tc.org, tc.supervisee, tc.supervisor)
			wantViolation(t, err, checkViolation, tc.constraint)
		})
	}
	// A link inserted directly as accepted is checked the same way.
	_, err := pool.Exec(t.Context(),
		`INSERT INTO supervision_links (supervisee_id, supervisor_id, organization_id, status, responded_at) VALUES ($1::uuid, $2::uuid, $3::uuid, 'accepted', now())`,
		supervisee, awaySupervisor, home)
	wantViolation(t, err, checkViolation, "supervision_links_supervisor_organization_chk")
	if n := count(t, pool, `SELECT count(*) FROM supervision_links`); n != 0 {
		t.Errorf("links stored = %d, want none", n)
	}

	// The same organization on both sides works, in either organization.
	mustRequestLink(t, pool, home, supervisee, homeSupervisor)
	mustRequestLink(t, pool, away, awayTrainee, awaySupervisor)
}

func TestSupervisionLinks_OrganizationIsRecheckedOnAcceptAndNeverBlocksClosing(t *testing.T) {
	pool := seedPool(t)
	home := defaultOrganization(t, pool)
	away := insertOrganization(t, pool, "Other Clinic")
	supervisee := insertUserIn(t, pool, home, "moving-trainee@example.test")
	supervisor := insertUserIn(t, pool, home, "moving-supervisor@example.test")

	moveSupervisor := func() {
		t.Helper()
		if _, err := pool.Exec(t.Context(), `UPDATE users SET organization_id = $1::uuid WHERE id = $2::uuid`, away, supervisor); err != nil {
			t.Fatal(err)
		}
	}

	// A supervisor who changed organization after the request cannot accept it.
	link := mustRequestLink(t, pool, home, supervisee, supervisor)
	moveSupervisor()
	wantViolation(t, setStatus(t, pool, link, acceptLink), checkViolation, "supervision_links_supervisor_organization_chk")
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM supervision_links WHERE id = $1::uuid`, link).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("status after the refused accept = %q, %v, want pending", status, err)
	}
	// Declining, cancelling and ending are never blocked by the organization check.
	if err := setStatus(t, pool, link, declineLink); err != nil {
		t.Fatalf("decline across organizations: %v", err)
	}

	// Editing a live link to a person from another organization is refused.
	if _, err := pool.Exec(t.Context(), `UPDATE users SET organization_id = $1::uuid WHERE id = $2::uuid`, home, supervisor); err != nil {
		t.Fatal(err)
	}
	live := mustRequestLink(t, pool, home, supervisee, supervisor)
	outsider := insertUserIn(t, pool, away, "outsider@example.test")
	wantViolation(t, setStatus(t, pool, live, `supervisor_id = '`+outsider+`'`), checkViolation, "supervision_links_supervisor_organization_chk")
	wantViolation(t, setStatus(t, pool, live, `organization_id = '`+away+`'`), checkViolation, "supervision_links_supervisee_organization_chk")

	// An accepted link whose people later moved organization can still be ended (S6-08 does that).
	if err := setStatus(t, pool, live, acceptLink); err != nil {
		t.Fatal(err)
	}
	moveSupervisor()
	if err := setStatus(t, pool, live, endLink); err != nil {
		t.Fatalf("ending a link across organizations: %v", err)
	}
}

func TestSupervisionLinks_OrganizationChangeInFlightIsSeenByTheCheck(t *testing.T) {
	pool := seedPool(t)
	home := defaultOrganization(t, pool)
	away := insertOrganization(t, pool, "Other Clinic")
	supervisee := insertUserIn(t, pool, home, "inflight-trainee@example.test")
	supervisor := insertUserIn(t, pool, home, "inflight-supervisor@example.test")

	// One transaction moves the supervisor and has not committed; a request in another transaction
	// must wait for it (FOR SHARE) and then see the new organization, not the stale one.
	mover, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mover.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err := mover.Exec(t.Context(), `UPDATE users SET organization_id = $1::uuid WHERE id = $2::uuid`, away, supervisor); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := requestLink(t, pool, home, supervisee, supervisor)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the request finished while the organization change was uncommitted: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	if err := mover.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		wantViolation(t, err, checkViolation, "supervision_links_supervisor_organization_chk")
	case <-time.After(10 * time.Second):
		t.Fatal("the request did not finish after the organization change committed")
	}
	if n := count(t, pool, `SELECT count(*) FROM supervision_links`); n != 0 {
		t.Errorf("links stored = %d, want none", n)
	}
}

func TestSupervisionLinks_UnknownPeopleAreRejectedByForeignKeys(t *testing.T) {
	pool := seedPool(t)
	org := defaultOrganization(t, pool)
	user := insertUserIn(t, pool, org, "known@example.test")
	const missing = "00000000-0000-4000-8000-000000000000"

	_, err := requestLink(t, pool, org, missing, user)
	wantViolation(t, err, foreignKeyViolated, "supervision_links_supervisee_id_fkey")
	_, err = requestLink(t, pool, org, user, missing)
	wantViolation(t, err, foreignKeyViolated, "supervision_links_supervisor_id_fkey")
	_, err = requestLink(t, pool, missing, user, insertUserIn(t, pool, org, "known-too@example.test"))
	wantViolation(t, err, checkViolation, "supervision_links_supervisee_organization_chk")
}

func TestSupervisionLinks_StateConstraintsRejectInconsistentRows(t *testing.T) {
	pool := seedPool(t)
	org := defaultOrganization(t, pool)
	supervisee := insertUserIn(t, pool, org, "state-trainee@example.test")
	supervisor := insertUserIn(t, pool, org, "state-supervisor@example.test")
	link := mustRequestLink(t, pool, org, supervisee, supervisor)

	for _, tc := range []struct {
		name, set, constraint string
	}{
		{"unknown status", `status = 'approved'`, "supervision_links_status_check"},
		{"empty status", `status = ''`, "supervision_links_status_check"},
		{"accepted without a response time", `status = 'accepted'`, "supervision_links_responded_at_chk"},
		{"declined without a response time", `status = 'declined'`, "supervision_links_responded_at_chk"},
		{"pending with a response time", `responded_at = now()`, "supervision_links_responded_at_chk"},
		{"cancelled with a response time", `status = 'cancelled', responded_at = now()`, "supervision_links_responded_at_chk"},
		{"ended without an end time", `status = 'ended', responded_at = now()`, "supervision_links_ended_at_chk"},
		{"end time on a pending request", `ended_at = now()`, "supervision_links_ended_at_chk"},
		{"ended_by on a pending request", `ended_by = supervisor_id`, "supervision_links_ended_by_chk"},
		{"response before the request", `status = 'accepted', responded_at = requested_at - interval '1 second'`, "supervision_links_responded_after_requested_chk"},
		{"end before the response", `status = 'ended', responded_at = now(), ended_at = now() - interval '1 hour'`, "supervision_links_ended_after_responded_chk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantViolation(t, setStatus(t, pool, link, tc.set), checkViolation, tc.constraint)
		})
	}
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM supervision_links WHERE id = $1::uuid`, link).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("link after every refused update = %q, %v, want unchanged", status, err)
	}
}

func TestSupervisionLinks_DeletingAUserRemovesTheirLinksButKeepsHistoryOfOthers(t *testing.T) {
	pool := seedPool(t)
	org := defaultOrganization(t, pool)
	supervisee := insertUserIn(t, pool, org, "leaving-trainee@example.test")
	supervisor := insertUserIn(t, pool, org, "staying-supervisor@example.test")
	admin := insertUserIn(t, pool, org, "ending-admin@example.test")

	// A link the admin ended survives the admin's deletion, with ended_by cleared.
	ended := mustRequestLink(t, pool, org, supervisee, supervisor)
	if err := setStatus(t, pool, ended, acceptLink); err != nil {
		t.Fatal(err)
	}
	if err := setStatus(t, pool, ended, `status = 'ended', ended_at = now(), ended_by = '`+admin+`'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1::uuid`, admin); err != nil {
		t.Fatalf("deleting the user who ended a link: %v", err)
	}
	var endedBy *string
	if err := pool.QueryRow(t.Context(), `SELECT ended_by::text FROM supervision_links WHERE id = $1::uuid`, ended).Scan(&endedBy); err != nil || endedBy != nil {
		t.Fatalf("ended_by after deleting the ender = %v, %v, want NULL on a kept link", endedBy, err)
	}

	// Deleting either person removes the links they were part of.
	live := mustRequestLink(t, pool, org, supervisee, supervisor)
	if _, err := pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1::uuid`, supervisee); err != nil {
		t.Fatalf("deleting a supervisee: %v", err)
	}
	if n := count(t, pool, `SELECT count(*) FROM supervision_links WHERE id = ANY($1::uuid[])`, []string{ended, live}); n != 0 {
		t.Errorf("links left after deleting the supervisee = %d, want 0", n)
	}
	other := insertUserIn(t, pool, org, "other-trainee@example.test")
	mustRequestLink(t, pool, org, other, supervisor)
	if _, err := pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1::uuid`, supervisor); err != nil {
		t.Fatalf("deleting a supervisor: %v", err)
	}
	if n := count(t, pool, `SELECT count(*) FROM supervision_links`); n != 0 {
		t.Errorf("links left after deleting the supervisor = %d, want 0", n)
	}

	// An organization that still has members cannot be deleted (CLAUDE.md §6.1), links or not.
	if _, err := pool.Exec(t.Context(), `DELETE FROM organizations WHERE id = $1::uuid`, org); err == nil {
		t.Error("deleted an organization that still has users")
	}
}

func insertNotification(t *testing.T, pool *pgxpool.Pool, userID, typ, payload string) (string, error) {
	t.Helper()
	var id string
	err := pool.QueryRow(t.Context(),
		`INSERT INTO notifications (user_id, type, payload) VALUES ($1::uuid, $2, COALESCE(NULLIF($3, '')::jsonb, '{}'::jsonb)) RETURNING id::text`,
		userID, typ, payload).Scan(&id)
	return id, err
}

func TestNotifications_StoreAnUnreadEventForAUser(t *testing.T) {
	pool := seedPool(t)
	user := insertUser(t, pool, "notified@example.test")

	id, err := insertNotification(t, pool, user, "supervision.request_received", `{"link_id": "x", "from": "Tara Trainee"}`)
	if err != nil {
		t.Fatal(err)
	}
	var typ string
	var payload string
	var readAt *time.Time
	var created time.Time
	if err := pool.QueryRow(t.Context(), `SELECT type, payload::text, read_at, created_at FROM notifications WHERE id = $1::uuid`, id).
		Scan(&typ, &payload, &readAt, &created); err != nil {
		t.Fatal(err)
	}
	if typ != "supervision.request_received" || readAt != nil || created.IsZero() || !strings.Contains(payload, "Tara Trainee") {
		t.Errorf("stored notification = %q %s read %v created %v", typ, payload, readAt, created)
	}

	// The payload defaults to an empty object, and read_at is set when the event is read.
	empty, err := insertNotification(t, pool, user, "supervision.ended", "")
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM notifications WHERE id = $1::uuid AND payload = '{}'::jsonb`, empty); n != 1 {
		t.Errorf("default payload is not an empty object")
	}
	if _, err := pool.Exec(t.Context(), `UPDATE notifications SET read_at = now() WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM notifications WHERE user_id = $1::uuid AND read_at IS NULL`, user); n != 1 {
		t.Errorf("unread = %d, want 1", n)
	}
}

func TestNotifications_ConstraintsRejectMalformedRows(t *testing.T) {
	pool := seedPool(t)
	user := insertUser(t, pool, "constraints-notified@example.test")

	for _, tc := range []struct {
		name, typ, payload, constraint, code string
	}{
		{"empty type", "", "", "notifications_type_check", checkViolation},
		{"upper-case type", "Supervision.Ended", "", "notifications_type_check", checkViolation},
		{"type with a space", "request received", "", "notifications_type_check", checkViolation},
		{"type with an injection payload", "x'; DROP TABLE users; --", "", "notifications_type_check", checkViolation},
		{"type starting with a dot", ".ended", "", "notifications_type_check", checkViolation},
		{"type over 100 characters", strings.Repeat("a", 101), "", "", stringDataTooLong},
		{"payload that is an array", "supervision.ended", `["a"]`, "notifications_payload_chk", checkViolation},
		{"payload that is a string", "supervision.ended", `"text"`, "notifications_payload_chk", checkViolation},
		{"payload over 8 KiB", "supervision.ended", `{"note": "` + strings.Repeat("x", 8200) + `"}`, "notifications_payload_chk", checkViolation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := insertNotification(t, pool, user, tc.typ, tc.payload)
			if tc.constraint == "" {
				requireSQLState(t, err, tc.code)
				return
			}
			wantViolation(t, err, tc.code, tc.constraint)
		})
	}
	_, err := insertNotification(t, pool, "00000000-0000-4000-8000-000000000000", "supervision.ended", "")
	wantViolation(t, err, foreignKeyViolated, "notifications_user_id_fkey")
	if n := count(t, pool, `SELECT count(*) FROM notifications`); n != 0 {
		t.Errorf("notifications stored = %d, want none", n)
	}
}

func TestNotifications_GoWithTheirUser(t *testing.T) {
	pool := seedPool(t)
	gone := insertUser(t, pool, "gone-notified@example.test")
	kept := insertUser(t, pool, "kept-notified@example.test")
	for _, u := range []string{gone, kept} {
		if _, err := insertNotification(t, pool, u, "supervision.request_received", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1::uuid`, gone); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM notifications`); n != 1 {
		t.Errorf("notifications after deleting one user = %d, want only the other user's", n)
	}
}

func TestNotifications_InboxAndUnreadCountUseTheirIndexes(t *testing.T) {
	pool := seedPool(t)
	user := insertUser(t, pool, "busy@example.test")
	other := insertUser(t, pool, "quiet@example.test")
	// Many rows for another user and mostly-read rows for this one, so the indexes are worth using.
	for _, u := range []string{user, other} {
		if _, err := pool.Exec(t.Context(), `
			INSERT INTO notifications (user_id, type, created_at, read_at)
			SELECT $1::uuid, 'supervision.ended', timestamptz '2026-10-01 09:00:00+00' + (n / 2) * interval '1 minute',
			       CASE WHEN n % 50 = 0 THEN NULL ELSE timestamptz '2026-11-01 09:00:00+00' END
			FROM generate_series(0, 999) AS n`, u); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ name, query, index string }{
		{"inbox page, newest first", `SELECT id, created_at FROM notifications WHERE user_id = '` + user + `' ORDER BY created_at DESC, id DESC LIMIT 50`, "notifications_user_created_at_idx"},
		{"unread count", `SELECT count(*) FROM notifications WHERE user_id = '` + user + `' AND read_at IS NULL`, "notifications_user_unread_idx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if plan := explainIndexPlan(t, pool, tc.query); !strings.Contains(plan, tc.index) {
				t.Errorf("plan does not use %s:\n%s", tc.index, plan)
			}
		})
	}
	if n := count(t, pool, `SELECT count(*) FROM notifications WHERE user_id = $1::uuid AND read_at IS NULL`, user); n != 20 {
		t.Errorf("unread rows = %d, want 20", n)
	}
}

// explainIndexPlan returns the plan for query with sequential scans and sorts off, so it shows
// whether an index can serve the query on its own.
func explainIndexPlan(t *testing.T, pool *pgxpool.Pool, query string) string {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
	for _, stmt := range []string{`ANALYZE notifications`, `SET LOCAL enable_seqscan = off`, `SET LOCAL enable_sort = off`} {
		if _, err := tx.Exec(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := tx.Query(t.Context(), `EXPLAIN `+query) // nosemgrep: no-string-built-sql
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
