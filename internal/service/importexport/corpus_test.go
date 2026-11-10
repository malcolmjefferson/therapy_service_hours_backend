package importexport_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// S4-11: a large ledger written the way the prototype writes it, and crafted rows that try to be
// something other than text, both imported through POST /import/markdown over real PostgreSQL.
// Every name and note is a fictional pseudonym or a payload (CLAUDE.md §0.7).

// prototypeCategories are the 12 categories and their groups (CLAUDE.md §8).
var prototypeCategories = []struct{ group, name string }{
	{"Direct Service", "Individual Counseling"},
	{"Direct Service", "Group Facilitation"},
	{"Direct Service", "Intake Interviews"},
	{"Direct Service", "Couples / Family Therapy"},
	{"Direct Service", "Psychoeducation"},
	{"Indirect Service", "Writing Progress Notes / Documentation"},
	{"Indirect Service", "Staff Meetings"},
	{"Indirect Service", "Case Management"},
	{"Indirect Service", "Assessment Scoring"},
	{"Indirect Service", "Professional Development"},
	{"Supervision", "Group Supervision"},
	{"Supervision", "Individual Supervision"},
}

// corpus is a ledger in the prototype's layout (no Start/End columns) and what it should store.
type corpus struct {
	text         string
	rows         int
	minutesByCat map[string]int
	clients      map[string]bool // lower-cased names
}

// prototypeCorpus writes n rows from a fixed seed: every category, one to three clients per row
// (the same people in different letter case, as hand-edited ledgers have), whole, half and
// quarter hours, and notes with an escaped pipe on some rows. Each row has its own note, so none
// is a duplicate of another.
func prototypeCorpus(n int) corpus {
	rng := rand.New(rand.NewPCG(4, 11))
	c := corpus{rows: n, minutesByCat: map[string]int{}, clients: map[string]bool{}}
	people := []string{"Pseudonym A", "Pseudonym B", "Pseudonym C", "Group North", "Group South", "A.B.", "C.D."}
	var b strings.Builder
	b.WriteString("# Clinical Hours Ledger\n\n## Summary\n- Direct Service: 0 / 1200 hours\n\n## Clients / Groups\n")
	for _, p := range people {
		fmt.Fprintf(&b, "- %s\n", p)
		c.clients[strings.ToLower(p)] = true
	}
	b.WriteString("\n## Entries\n| Date | Group | Category | Client | Hours | On-site | Notes |\n|---|---|---|---|---|---|---|\n")
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		cat := prototypeCategories[rng.IntN(len(prototypeCategories))]
		quarters := 1 + rng.IntN(16) // 0.25 to 4 hours
		hours := strconv.FormatFloat(float64(quarters)/4, 'f', -1, 64)
		var clients []string
		for k := rng.IntN(4); k > 0; k-- { // 0 to 3
			p := people[rng.IntN(len(people))]
			if rng.IntN(5) == 0 {
				p = strings.ToUpper(p)
			}
			clients = append(clients, p)
		}
		cell := "—"
		if len(clients) > 0 {
			cell = strings.Join(clients, "; ")
		}
		note := fmt.Sprintf("Session %d", i)
		if i%7 == 0 {
			note += ` with a \| pipe`
		}
		onSite := "No"
		if i%3 == 0 {
			onSite = "Yes"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n", base.AddDate(0, 0, i%365).Format(time.DateOnly), cat.group, cat.name, cell, hours, onSite, note)
		c.minutesByCat[cat.name] += quarters * 15
	}
	c.text = b.String()
	return c
}

func (a *api) minutesByCategory(userID string) map[string]int {
	a.t.Helper()
	rows, err := a.e.conn.Query(a.t.Context(), `
		SELECT c.name, sum(e.duration_minutes)::int FROM entries e JOIN categories c ON c.id = e.category_id
		WHERE e.user_id = $1 GROUP BY c.name`, userID)
	if err != nil {
		a.t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var name string
		var minutes int
		if err := rows.Scan(&name, &minutes); err != nil {
			a.t.Fatal(err)
		}
		got[name] = minutes
	}
	return got
}

// exportedDates reads the whole export, 200 entries a page, and returns the session dates in order.
func (a *api) exportedDates(sub string) []string {
	a.t.Helper()
	var dates []string
	for page := 1; ; page++ {
		res := a.get(sub, "/export/entries", fmt.Sprintf("page=%d&page_size=200", page))
		var body struct {
			Items []struct {
				SessionDate string `json:"session_date"`
			}
			Page struct {
				TotalItems int `json:"total_items"`
			}
		}
		if err := json.Unmarshal([]byte(res.body), &body); err != nil || res.status != http.StatusOK {
			a.t.Fatalf("export page %d = %d %.200s (%v)", page, res.status, res.body, err)
		}
		for _, it := range body.Items {
			dates = append(dates, it.SessionDate)
		}
		if len(dates) >= body.Page.TotalItems || len(body.Items) == 0 {
			return dates
		}
	}
}

// wantReimportSkipsAll imports text again and requires every one of its rows to be a duplicate.
func (a *api) wantReimportSkipsAll(sub, text string, rows int) {
	a.t.Helper()
	again := decodeReport(a.t, a.post(upload{sub: sub, content: []byte(text)}))
	if again.ImportedCount != 0 || again.SkippedCount != rows {
		a.t.Errorf("re-import = %+v, want all %d rows skipped", again, rows)
	}
	for _, s := range again.SkippedRows {
		if s.Reason != "duplicate" {
			a.t.Fatalf("row %d skipped as %q, want duplicate", s.Row, s.Reason)
		}
	}
}

// wantAllImported requires a report with every row imported, none skipped and needsTime flagged.
func wantAllImported(t *testing.T, r reportBody, rows, needsTime int) {
	t.Helper()
	if r.TotalRows != rows || r.ImportedCount != rows || r.SkippedCount != 0 || r.NeedsTimeCount != needsTime {
		t.Fatalf("report = %+v, want %d rows imported, %d needing a time, none skipped (%v)", r, rows, needsTime, r.SkippedRows)
	}
}

func TestCorpus_APrototypeLedgerOfSixHundredRowsImportsEveryRowOnce(t *testing.T) {
	a := newAPI(t)
	me := a.e.user("trainee@example.test")
	sub := a.as(me)
	c := prototypeCorpus(600)

	report := decodeReport(t, a.post(upload{sub: sub, content: []byte(c.text)}))

	wantAllImported(t, report, c.rows, c.rows)
	if got := a.e.entryCount(me); got != c.rows {
		t.Errorf("%d entries stored, want %d", got, c.rows)
	}
	// Hours per category survive exactly, in whole minutes.
	got := a.minutesByCategory(me.ID)
	for name, want := range c.minutesByCat {
		if got[name] != want {
			t.Errorf("%s: %d minutes stored, want %d", name, got[name], want)
		}
	}
	// The same people in other letter case are one saved client each.
	if n := a.e.clientCount(me); n != len(c.clients) {
		t.Errorf("%d clients saved, want %d", n, len(c.clients))
	}
	if n := a.e.count(`SELECT count(*) FROM entries WHERE user_id = $1 AND NOT needs_time`, me.ID); n != 0 {
		t.Errorf("%d imported entries are not flagged needs_time", n)
	}
	if n := a.e.count(`SELECT count(*) FROM entries WHERE user_id = $1 AND strpos(notes, chr(92)) > 0`, me.ID); n != 0 {
		t.Errorf("%d notes keep the escape backslash, want the bare pipe", n)
	}

	// The export pages hold exactly the same entries, oldest first.
	dates := a.exportedDates(sub)
	if len(dates) != c.rows || !sort.StringsAreSorted(dates) {
		t.Fatalf("export returned %d entries (sorted: %v), want %d oldest first", len(dates), sort.StringsAreSorted(dates), c.rows)
	}

	// Importing the same file again adds nothing: every row is reported as a duplicate.
	a.wantReimportSkipsAll(sub, c.text, c.rows)
	if a.e.entryCount(me) != c.rows {
		t.Error("the re-import changed the entry count")
	}
}

// crafted holds cells that a careless implementation would run, expand or match as patterns.
// Each is a note. Client names are in craftedClients and avoid `;`, which separates clients in a cell.
var crafted = []string{
	`O'Brien's "quoted" note`,
	`it's ''double'' and ""double""`,
	"back`tick",
	`C:\temp\new\table`,
	`trailing backslash \`,
	`double \\ and quad \\\\`,
	`escaped quote \' and \"`,
	`100%`,
	`100x`,
	`a_c`,
	`abc`,
	`%`,
	`_`,
	`50% off_everything%`,
	`'; DROP TABLE entries; --`,
	`'); DROP TABLE clients; --`,
	`" OR ""="`,
	`' OR '1'='1`,
	`1; DELETE FROM users WHERE 1=1; --`,
	`UNION SELECT email FROM users --`,
	`'; UPDATE entries SET status = 'approved'; --`,
	`SELECT pg_sleep(5)`,
	`$1 $2 $$ $tag$ x $tag$`,
	`%s %d %v {0} ${x}`,
	`/* comment */ -- trailing`,
	`NULL`,
	`null`,
	`TRUE`,
	`0`,
}

var craftedClients = []string{
	`O'Brien`,
	`Pseudonym_A`,
	`PseudonymXA`,
	`P%`,
	`Pxyz`,
	`x' OR '1'='1' --`,
	`" OR ""="`,
	`C:\temp\new`,
	`SELECT * FROM users`,
}

func craftedLedger() string {
	var b strings.Builder
	b.WriteString("# Clinical Hours Ledger\n\n## Clients / Groups\n")
	for _, c := range craftedClients {
		fmt.Fprintf(&b, "- %s\n", strings.ReplaceAll(c, "|", `\|`))
	}
	b.WriteString("\n## Entries\n| Date | Group | Category | Client | Hours | On-site | Notes |\n|---|---|---|---|---|---|---|\n")
	base := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	for i, note := range crafted {
		// Every note shares one date, category, length and client, so only the note tells rows apart.
		fmt.Fprintf(&b, "| %s | Direct Service | Individual Counseling | %s | 1 | No | %s |\n", base.Format(time.DateOnly), craftedClients[i%len(craftedClients)], note)
	}
	return b.String()
}

// wantStoredOnce requires each value to be stored exactly once, unchanged: as a note when notes is
// true, otherwise as a client name.
func (a *api) wantStoredOnce(userID string, notes bool, values []string) {
	a.t.Helper()
	query := `SELECT count(*) FROM clients WHERE user_id = $1 AND name = $2`
	if notes {
		query = `SELECT count(*) FROM entries WHERE user_id = $1 AND notes = $2`
	}
	for _, want := range values {
		if n := a.e.count(query, userID, want); n != 1 {
			a.t.Errorf("%q stored %d times, want once exactly as written", want, n)
		}
	}
}

// wantTablesIntact requires that no payload dropped or changed another table.
func (a *api) wantTablesIntact(userID string) {
	a.t.Helper()
	for _, table := range []string{"entries", "clients", "users", "categories", "organizations"} {
		if n := a.e.count(`SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1`, table); n != 1 {
			a.t.Errorf("table %s is gone", table)
		}
	}
	if a.e.count(`SELECT count(*) FROM users`) != 1 || a.e.count(`SELECT count(*) FROM categories`) != 12 {
		a.t.Error("an injected statement changed another table")
	}
	if n := a.e.count(`SELECT count(*) FROM entries WHERE user_id = $1 AND status <> 'pending'`, userID); n != 0 {
		a.t.Errorf("%d entries are not pending", n)
	}
}

// wantExportedNotes requires the export to hand back every note exactly as stored.
func (a *api) wantExportedNotes(sub string, want []string) {
	a.t.Helper()
	res := a.get(sub, "/export/entries", "page_size=200")
	var body struct{ Items []struct{ Notes string } }
	if err := json.Unmarshal([]byte(res.body), &body); err != nil || res.status != http.StatusOK {
		a.t.Fatalf("export = %d %.200s (%v)", res.status, res.body, err)
	}
	got := map[string]bool{}
	for _, it := range body.Items {
		got[it.Notes] = true
	}
	for _, n := range want {
		if !got[n] {
			a.t.Errorf("export lacks note %q", n)
		}
	}
}

func TestCorpus_CraftedRowsAreImportedLiterally(t *testing.T) {
	a := newAPI(t)
	me := a.e.user("trainee@example.test")
	sub := a.as(me)
	text := craftedLedger()

	report := decodeReport(t, a.post(upload{sub: sub, content: []byte(text)}))

	// The shared date, category and length do not make a row a duplicate of another: `100%` and `100x`,
	// `a_c` and `abc`, `%` and every other note stay distinct, so nothing is matched as a pattern.
	wantAllImported(t, report, len(crafted), len(crafted))
	a.wantStoredOnce(me.ID, true, crafted)
	a.wantStoredOnce(me.ID, false, craftedClients)
	// `_` and `%` in a name make no other client the same one.
	if n := a.e.clientCount(me); n != len(craftedClients) {
		t.Errorf("%d clients saved, want %d separate ones", n, len(craftedClients))
	}
	a.wantTablesIntact(me.ID)
	a.wantExportedNotes(sub, crafted)

	// Importing the file again matches every row by value, so none is added.
	a.wantReimportSkipsAll(sub, text, len(crafted))
	if a.e.entryCount(me) != len(crafted) {
		t.Error("the re-import changed the entry count")
	}
}
