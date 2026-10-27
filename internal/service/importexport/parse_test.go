package importexport_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/importexport"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const (
	headerRow = "| Date | Group | Category | Client | Hours | On-site | Notes |\n"
	header    = headerRow + "|---|---|---|---|---|---|---|\n"
)

func parse(t *testing.T, text string) importexport.Ledger {
	t.Helper()
	l, err := importexport.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return l
}

// TestParse_Golden parses ledgers in the prototype format and compares the typed result with a
// reviewed golden file. Run `go test ./internal/service/importexport -update` after a deliberate change.
func TestParse_Golden(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("testdata/*.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden inputs: %v", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			t.Parallel()
			in, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.MarshalIndent(parse(t, string(in)), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := strings.TrimSuffix(file, ".md") + ".golden.json"
			if *update {
				if err := os.WriteFile(golden, got, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("parsed %s differs from %s; review the diff and run with -update if intended\n got: %s", file, golden, got)
			}
		})
	}
}

func TestParse_BothLayoutsGiveTheSameTypedRows(t *testing.T) {
	t.Parallel()
	old := parse(t, header+"| 2026-01-05 | Direct Service | Individual Counseling | Pseudonym A | 1.5 | Yes | note |\n")
	timed := parse(t, "| Date | Start | End | Group | Category | Client | Hours | On-site | Notes |\n|---|---|---|---|---|---|---|---|---|\n"+
		"| 2026-01-05 | 09:00 | 10:30 | Direct Service | Individual Counseling | Pseudonym A | 1.5 | Yes | note |\n")

	if old.HasTimeColumns || !timed.HasTimeColumns {
		t.Errorf("HasTimeColumns = %v and %v, want false and true", old.HasTimeColumns, timed.HasTimeColumns)
	}
	o, n := old.Rows[0], timed.Rows[0]
	if o.HasTimes() || !n.HasTimes() {
		t.Errorf("HasTimes = %v and %v, want false and true", o.HasTimes(), n.HasTimes())
	}
	if o.DurationMinutes != 90 || n.DurationMinutes != 90 {
		t.Errorf("DurationMinutes = %d and %d, want 90 and 90", o.DurationMinutes, n.DurationMinutes)
	}
	if n.StartTime != "09:00" || n.EndTime != "10:30" {
		t.Errorf("times = %q-%q, want 09:00-10:30", n.StartTime, n.EndTime)
	}
	if !o.Date.Equal(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)) || o.Group != domain.GroupDirect {
		t.Errorf("date and group = %v %q", o.Date, o.Group)
	}
}

func TestParse_Cells(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		row         string
		wantClients []string
		wantNotes   string
		wantMinutes int
	}{
		{"escaped pipe in notes", "| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | Yes | a \\| b |", []string{"A"}, "a | b", 60},
		{"backslash before an escaped pipe", "| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | Yes | a \\\\| b |", []string{"A"}, "a \\| b", 60},
		{"trailing backslash", "| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | Yes | path\\ |", []string{"A"}, "path\\", 60},
		{"semicolon list", "| 2026-01-05 | Direct Service | Individual Counseling | A; B ;C | 1 | Yes | |", []string{"A", "B", "C"}, "", 60},
		{"em dash means none", "| 2026-01-05 | Direct Service | Individual Counseling | — | 1 | Yes | |", nil, "", 60},
		{"empty means none", "| 2026-01-05 | Direct Service | Individual Counseling |  | 1 | Yes | |", nil, "", 60},
		{"duplicate names ignoring case", "| 2026-01-05 | Direct Service | Individual Counseling | Ana; ANA; ana | 1 | Yes | |", []string{"Ana"}, "", 60},
		{"unicode", "| 2026-01-05 | Direct Service | Individual Counseling | Zoë; 李 | 1 | Yes | Café 😀 日本語 |", []string{"Zoë", "李"}, "Café 😀 日本語", 60},
		{"notes cell left out", "| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | Yes |", []string{"A"}, "", 60},
		{"no closing pipe", "| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | Yes | text", []string{"A"}, "text", 60},
		{"quarter hours", "| 2026-01-05 | Direct Service | Individual Counseling | A | 0.25 | Yes | |", []string{"A"}, "", 15},
		{"two decimals round to a minute", "| 2026-01-05 | Direct Service | Individual Counseling | A | 0.33 | Yes | |", []string{"A"}, "", 20},
		{"full day", "| 2026-01-05 | Direct Service | Individual Counseling | A | 24 | Yes | |", []string{"A"}, "", 1440},
		{"group names without Service", "| 2026-01-05 | direct | Individual Counseling | A | 1 | yes | |", []string{"A"}, "", 60},
		{"formula text is kept as text", "| 2026-01-05 | Direct Service | Individual Counseling | =A1 | 1 | Yes | =1+1 |", []string{"=A1"}, "=1+1", 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := parse(t, header+tt.row+"\n")
			if len(l.Rows) != 1 || len(l.Skipped) != 0 {
				t.Fatalf("rows = %d, skipped = %+v", len(l.Rows), l.Skipped)
			}
			r := l.Rows[0]
			if fmt.Sprint(r.Clients) != fmt.Sprint(tt.wantClients) {
				t.Errorf("Clients = %q, want %q", r.Clients, tt.wantClients)
			}
			if r.Notes != tt.wantNotes {
				t.Errorf("Notes = %q, want %q", r.Notes, tt.wantNotes)
			}
			if r.DurationMinutes != tt.wantMinutes {
				t.Errorf("DurationMinutes = %d, want %d", r.DurationMinutes, tt.wantMinutes)
			}
		})
	}
}

func TestParse_ColumnsAreFoundByName(t *testing.T) {
	t.Parallel()
	l := parse(t, "| Notes | On site | Hours | Clients | Category | Group | End | Start | Date | Extra |\n|---|---|---|---|---|---|---|---|---|---|\n"+
		"| n | Yes | 1 | A | Individual Counseling | Direct Service | 10:00 | 09:00 | 2026-01-05 | ignored |\n")
	if len(l.Rows) != 1 {
		t.Fatalf("rows = %d, skipped = %+v", len(l.Rows), l.Skipped)
	}
	r := l.Rows[0]
	if r.StartTime != "09:00" || r.EndTime != "10:00" || r.Notes != "n" || !r.OnSite {
		t.Errorf("row = %+v", r)
	}
}

func TestParse_RowErrors(t *testing.T) {
	t.Parallel()
	const prefix = "| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | Yes | |"
	tests := []struct {
		name       string
		row        string
		wantReason importexport.SkipReason
		wantField  string
	}{
		{"impossible date", "| 2026-02-30 | Direct Service | Individual Counseling | A | 1 | Yes | |", importexport.ReasonInvalidDate, "date"},
		{"short date", "| 2026-1-5 | Direct Service | Individual Counseling | A | 1 | Yes | |", importexport.ReasonInvalidDate, "date"},
		{"unknown group", "| 2026-01-05 | Teaching | Individual Counseling | A | 1 | Yes | |", importexport.ReasonUnknownGroup, "group"},
		{"blank category", "| 2026-01-05 | Direct Service |  | A | 1 | Yes | |", importexport.ReasonUnknownGroup, "category"},
		{"long category", "| 2026-01-05 | Direct Service | " + strings.Repeat("x", 101) + " | A | 1 | Yes | |", importexport.ReasonUnknownGroup, "category"},
		{"zero hours", "| 2026-01-05 | Direct Service | Individual Counseling | A | 0 | Yes | |", importexport.ReasonInvalidHours, "hours"},
		{"too many hours", "| 2026-01-05 | Direct Service | Individual Counseling | A | 24.5 | Yes | |", importexport.ReasonInvalidHours, "hours"},
		{"negative hours", "| 2026-01-05 | Direct Service | Individual Counseling | A | -1 | Yes | |", importexport.ReasonInvalidHours, "hours"},
		{"exponent hours", "| 2026-01-05 | Direct Service | Individual Counseling | A | 1e2 | Yes | |", importexport.ReasonInvalidHours, "hours"},
		{"NaN hours", "| 2026-01-05 | Direct Service | Individual Counseling | A | NaN | Yes | |", importexport.ReasonInvalidHours, "hours"},
		{"hours without digits", "| 2026-01-05 | Direct Service | Individual Counseling | A | . | Yes | |", importexport.ReasonInvalidHours, "hours"},
		{"huge hours", "| 2026-01-05 | Direct Service | Individual Counseling | A | 99999999999999999999 | Yes | |", importexport.ReasonInvalidHours, "hours"},
		{"empty hours", "| 2026-01-05 | Direct Service | Individual Counseling | A |  | Yes | |", importexport.ReasonInvalidHours, "hours"},
		{"long client", "| 2026-01-05 | Direct Service | Individual Counseling | " + strings.Repeat("c", 65) + " | 1 | Yes | |", importexport.ReasonInvalidClient, "client"},
		{"control character in client", "| 2026-01-05 | Direct Service | Individual Counseling | A\x07B | 1 | Yes | |", importexport.ReasonInvalidClient, "client"},
		{"long notes", "| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | Yes | " + strings.Repeat("n", 2001) + " |", importexport.ReasonInvalidNotes, "notes"},
		{"control character in notes", "| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | Yes | a\x1bb |", importexport.ReasonInvalidNotes, "notes"},
		{"unknown on-site", "| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | Maybe | |", importexport.ReasonMalformedRow, ""},
		{"too few cells", "| 2026-01-05 | Direct Service | Individual Counseling | A |", importexport.ReasonMalformedRow, ""},
		{"too many cells", prefix + " extra |", importexport.ReasonMalformedRow, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := parse(t, header+tt.row+"\n")
			if len(l.Rows) != 0 || len(l.Skipped) != 1 {
				t.Fatalf("rows = %d, skipped = %+v, want one skipped row", len(l.Rows), l.Skipped)
			}
			s := l.Skipped[0]
			if s.Number != 1 || s.Reason != tt.wantReason || s.Field != tt.wantField {
				t.Errorf("skipped = %+v, want row 1, %q, field %q", s, tt.wantReason, tt.wantField)
			}
			if s.Message == "" {
				t.Error("skipped row has no message")
			}
		})
	}
}

func TestParse_TimeErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, start, end, hours string
		wantField               string
		wantReason              importexport.SkipReason
	}{
		{"start only", "09:00", "", "1", "start_time", importexport.ReasonInvalidTime},
		{"end only", "", "10:00", "1", "start_time", importexport.ReasonInvalidTime},
		{"off the quarter hour", "09:10", "10:00", "1", "start_time", importexport.ReasonInvalidTime},
		{"end off the quarter hour", "09:00", "10:10", "1", "end_time", importexport.ReasonInvalidTime},
		{"not a time", "9am", "10:00", "1", "start_time", importexport.ReasonInvalidTime},
		{"hour 24", "09:00", "24:00", "1", "end_time", importexport.ReasonInvalidTime},
		{"crosses midnight", "23:45", "00:00", "1", "end_time", importexport.ReasonInvalidTime},
		{"end equals start", "09:00", "09:00", "1", "end_time", importexport.ReasonInvalidTime},
		{"hours disagree", "09:00", "10:00", "2", "hours", importexport.ReasonInvalidHours},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := parse(t, "| Date | Start | End | Group | Category | Client | Hours | On-site | Notes |\n|---|---|---|---|---|---|---|---|---|\n"+
				"| 2026-01-05 | "+tt.start+" | "+tt.end+" | Direct Service | Individual Counseling | A | "+tt.hours+" | Yes | |\n")
			if len(l.Skipped) != 1 || l.Skipped[0].Reason != tt.wantReason || l.Skipped[0].Field != tt.wantField {
				t.Errorf("skipped = %+v, rows = %d, want %q on %q", l.Skipped, len(l.Rows), tt.wantReason, tt.wantField)
			}
		})
	}
}

func TestParse_HoursMayBeOneMinuteOffTheTimes(t *testing.T) {
	t.Parallel()
	// 20 minutes exported as 0.33 hours is 19.8 minutes: rounding, not a mismatch.
	l := parse(t, "| Date | Start | End | Group | Category | Client | Hours | On-site | Notes |\n|---|---|---|---|---|---|---|---|---|\n"+
		"| 2026-01-05 | 09:00 | 09:15 | Direct Service | Individual Counseling | A | 0.25 | Yes | |\n"+
		"| 2026-01-05 | 10:00 | 10:15 | Direct Service | Individual Counseling | A |  | Yes | |\n")
	if len(l.Rows) != 2 || l.Rows[0].DurationMinutes != 15 || l.Rows[1].DurationMinutes != 15 {
		t.Errorf("rows = %+v, skipped = %+v", l.Rows, l.Skipped)
	}
}

func TestParse_BadRowsDoNotStopTheRest(t *testing.T) {
	t.Parallel()
	l := parse(t, header+
		"| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | Yes | one |\n"+
		"| nope | Direct Service | Individual Counseling | A | 1 | Yes | two |\n"+
		"| 2026-01-07 | Direct Service | Individual Counseling | A | 1 | Yes | three |\n")
	if l.TotalRows != 3 || len(l.Rows) != 2 || len(l.Skipped) != 1 {
		t.Fatalf("total = %d, rows = %d, skipped = %d", l.TotalRows, len(l.Rows), len(l.Skipped))
	}
	if l.Rows[0].Number != 1 || l.Rows[1].Number != 3 || l.Skipped[0].Number != 2 {
		t.Errorf("row numbers = %d, %d and skipped %d, want 1, 3 and 2", l.Rows[0].Number, l.Rows[1].Number, l.Skipped[0].Number)
	}
}

func TestParse_SkippedMessagesNeverQuoteTheCell(t *testing.T) {
	t.Parallel()
	secret := "Zebra-Secret-Value"
	for _, row := range []string{
		"| " + secret + " | Direct Service | Individual Counseling | A | 1 | Yes | |",
		"| 2026-01-05 | " + secret + " | Individual Counseling | A | 1 | Yes | |",
		"| 2026-01-05 | Direct Service | Individual Counseling | A | " + secret + " | Yes | |",
		"| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | " + secret + " | |",
		"| 2026-01-05 | Direct Service | Individual Counseling | " + strings.Repeat(secret, 10) + " | 1 | Yes | |",
	} {
		l := parse(t, header+row+"\n")
		if len(l.Skipped) != 1 || strings.Contains(l.Skipped[0].Message, secret) {
			t.Errorf("row %q: skipped = %+v", row, l.Skipped)
		}
	}
}

func TestParse_ClientsSection(t *testing.T) {
	t.Parallel()
	l := parse(t, "## Clients / Groups\n- Ana\n* Bo\n- ana\n- —\n- \n- "+strings.Repeat("c", 65)+"\n\n## Entries\n"+header+
		"| 2026-01-05 | Direct Service | Individual Counseling | BO; Cy | 1 | Yes | |\n")
	if fmt.Sprint(l.Clients) != "[Ana Bo]" {
		t.Errorf("Clients = %q, want [Ana Bo]", l.Clients)
	}
	if len(l.ClientNotices) != 1 || l.ClientNotices[0].Line != 7 {
		t.Errorf("ClientNotices = %+v, want one at line 7", l.ClientNotices)
	}
	if got := fmt.Sprint(l.ClientNames()); got != "[Ana Bo Cy]" {
		t.Errorf("ClientNames() = %s, want [Ana Bo Cy]", got)
	}
}

func TestParse_OtherTablesAreIgnored(t *testing.T) {
	t.Parallel()
	l := parse(t, "## Summary\n| Goal | Hours |\n|---|---|\n| Direct | 1 |\n\n## Entries\n"+header+
		"| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | Yes | |\n\n## Footer\n| a | b |\n|---|---|\n| 1 | 2 |\n")
	if len(l.Rows) != 1 || len(l.Skipped) != 0 {
		t.Errorf("rows = %d, skipped = %+v", len(l.Rows), l.Skipped)
	}
}

func TestParse_LineEndingsAndBOM(t *testing.T) {
	t.Parallel()
	text := "\xEF\xBB\xBF## Entries\r\n" + strings.ReplaceAll(header, "\n", "\r\n") +
		"| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | Yes | crlf |\r\n"
	l := parse(t, text)
	if len(l.Rows) != 1 || l.Rows[0].Notes != "crlf" {
		t.Errorf("rows = %+v, skipped = %+v", l.Rows, l.Skipped)
	}
}

func TestParse_LongCellsAreHandled(t *testing.T) {
	t.Parallel()
	// 100,000 characters in one cell: refused for that row, and the following row is still read.
	l := parse(t, header+
		"| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | Yes | "+strings.Repeat("é", 100_000)+" |\n"+
		"| 2026-01-06 | Direct Service | Individual Counseling | A | 1 | Yes | "+strings.Repeat("é", 2000)+" |\n")
	if len(l.Skipped) != 1 || l.Skipped[0].Reason != importexport.ReasonInvalidNotes || len(l.Rows) != 1 {
		t.Errorf("skipped = %+v, rows = %d", l.Skipped, len(l.Rows))
	}
}

func TestParse_Limits(t *testing.T) {
	t.Parallel()
	row := "| 2026-01-05 | Direct Service | Individual Counseling | A | 1 | Yes | |\n"
	t.Run("5000 rows are accepted", func(t *testing.T) {
		t.Parallel()
		l := parse(t, header+strings.Repeat(row, importexport.MaxRows))
		if l.TotalRows != importexport.MaxRows {
			t.Errorf("TotalRows = %d", l.TotalRows)
		}
	})
	t.Run("5001 rows are refused", func(t *testing.T) {
		t.Parallel()
		_, err := importexport.Parse(strings.NewReader(header + strings.Repeat(row, importexport.MaxRows+1)))
		if !errors.Is(err, importexport.ErrTooManyRows) {
			t.Errorf("error = %v, want ErrTooManyRows", err)
		}
	})
	t.Run("a file over 1 MiB is refused", func(t *testing.T) {
		t.Parallel()
		_, err := importexport.Parse(strings.NewReader(strings.Repeat("a", importexport.MaxFileBytes+1)))
		if !errors.Is(err, importexport.ErrTooLarge) {
			t.Errorf("error = %v, want ErrTooLarge", err)
		}
	})
	t.Run("a file of exactly 1 MiB is read", func(t *testing.T) {
		t.Parallel()
		text := header + row
		text += strings.Repeat(" ", importexport.MaxFileBytes-len(text))
		if _, err := importexport.Parse(strings.NewReader(text)); err != nil {
			t.Errorf("error = %v", err)
		}
	})
}

func TestParse_RejectsAFileThatIsNotALedger(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, text string
		want       error
	}{
		{"empty", "", importexport.ErrNoEntriesTable},
		{"prose", "hello\nworld\n", importexport.ErrNoEntriesTable},
		{"no table", "## Entries\n- nothing\n", importexport.ErrNoEntriesTable},
		{"missing column", "| Date | Group |\n|---|---|\n| 2026-01-05 | Direct Service |\n", importexport.ErrNoEntriesTable},
		{"duplicate column", "| Date | Date | Group | Category | Client | Hours | On-site | Notes |\n|---|---|---|---|---|---|---|---|\n", importexport.ErrNoEntriesTable},
		{"start without end", "| Date | Start | Group | Category | Client | Hours | On-site | Notes |\n|---|---|---|---|---|---|---|---|\n", importexport.ErrNoEntriesTable},
		{"header without separator", headerRow + "| 2026-01-05 | x |\n", importexport.ErrNoEntriesTable},
		{"header at end of file", strings.TrimSuffix(headerRow, "\n"), importexport.ErrNoEntriesTable},
		{"invalid UTF-8", header + "| 2026-01-05 | \xff\xfe |\n", importexport.ErrNotText},
		{"NUL byte", header + "\x00", importexport.ErrNotText},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := importexport.Parse(strings.NewReader(tt.text))
			if !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestParse_ReadErrorIsReturned(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	if _, err := importexport.Parse(errReader{boom}); !errors.Is(err, boom) {
		t.Errorf("error = %v, want boom", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// FuzzParse checks that no input panics and that the result is internally consistent. The seeds are
// the golden files, so plain `go test` replays them; `go test -fuzz=FuzzParse` explores further.
func FuzzParse(f *testing.F) {
	files, _ := filepath.Glob("testdata/*.md")
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte(header + "| 2026-01-05 | Direct Service | Individual Counseling | A\\| | 1 | Yes | \\\\| |\n"))
	f.Add([]byte("|\n|\n|\\|"))
	f.Add([]byte("## Clients\n- \n-\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		l, err := importexport.Parse(bytes.NewReader(data))
		if err != nil {
			return
		}
		if l.TotalRows != len(l.Rows)+len(l.Skipped) || l.TotalRows > importexport.MaxRows {
			t.Fatalf("TotalRows = %d with %d rows and %d skipped", l.TotalRows, len(l.Rows), len(l.Skipped))
		}
		for _, r := range l.Rows {
			if r.DurationMinutes < 1 || r.DurationMinutes > 1440 || r.Number < 1 {
				t.Fatalf("row out of range: %+v", r)
			}
			if r.HasTimes() != (r.EndTime != "") {
				t.Fatalf("times are not paired: %+v", r)
			}
		}
	})
}
