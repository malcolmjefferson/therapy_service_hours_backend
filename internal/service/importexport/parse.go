// Package importexport holds the use cases that bring a user's data in from, and out to, files.
// Parse (S4-02) turns the prototype's Markdown ledger into typed values and per-row errors. It does
// no I/O beyond reading the reader it is given and never touches storage: the import service
// (S4-03) decides what to insert, so no cell is ever interpreted as SQL, a formula or markup (FR-IO-03).
package importexport

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/clients"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
)

const (
	// MaxFileBytes is the largest ledger Parse reads (1 MiB), matching importMarkdown in api/openapi.yaml.
	MaxFileBytes = 1 << 20
	// MaxRows is the most rows the entries table may hold, matching importMarkdown in api/openapi.yaml.
	MaxRows = 5000
	// MaxCategoryLength is the longest category name in characters; the column is varchar(100).
	MaxCategoryLength = 100
	// noneMarker is how the prototype writes "no client".
	noneMarker = "—"
	// maxHoursText bounds the Hours cell so the number cannot overflow or lose precision.
	maxHoursText = 10
	// clockLength is the length of an HH:MM time.
	clockLength = 5
)

// Errors that reject the whole file. The HTTP layer (S4-04) maps them to problems: the first two
// to 413 and the others to 400. Rows with bad values are not errors; they come back in Ledger.Skipped.
var (
	// ErrTooLarge means the file is larger than MaxFileBytes.
	ErrTooLarge = errors.New("ledger is too large")
	// ErrTooManyRows means the entries table has more than MaxRows rows.
	ErrTooManyRows = errors.New("ledger has too many rows")
	// ErrNotText means the file is not UTF-8 text without NUL characters.
	ErrNotText = errors.New("ledger is not UTF-8 text")
	// ErrNoEntriesTable means there is no entries table, or its header lacks a required column.
	ErrNoEntriesTable = errors.New("ledger has no valid entries table")
)

// SkipReason says why a row was not parsed. The values are the SkipReason enum in api/openapi.yaml.
type SkipReason string

// The reasons the parser can give. The import service adds duplicate, overlap and unknown categories.
const (
	ReasonMalformedRow  SkipReason = "malformed-row"
	ReasonInvalidDate   SkipReason = "invalid-date"
	ReasonInvalidTime   SkipReason = "invalid-time"
	ReasonInvalidHours  SkipReason = "invalid-hours"
	ReasonUnknownGroup  SkipReason = "unknown-category"
	ReasonInvalidClient SkipReason = "invalid-client"
	ReasonInvalidNotes  SkipReason = "invalid-notes"
)

// The Field of a RowError: the API name of the column.
const (
	fieldDate     = "date"
	fieldStart    = "start_time"
	fieldEnd      = "end_time"
	fieldGroup    = "group"
	fieldCategory = "category"
	fieldClient   = "client"
	fieldHours    = "hours"
	fieldNotes    = "notes"
)

// Ledger is a parsed file.
type Ledger struct {
	// Clients are the names in the Clients / Groups section, first spelling kept, without
	// case-insensitive duplicates. It is empty for an older file that has no such section.
	Clients []string
	// ClientNotices lists section entries that are not valid client names; they are not clients.
	ClientNotices []Notice
	// Rows are the entries-table rows that parsed, in file order.
	Rows []Row
	// Skipped are the rows that did not parse, in file order, one reason each.
	Skipped []RowError
	// TotalRows is the number of rows in the entries table: len(Rows) + len(Skipped).
	TotalRows int
	// HasTimeColumns is true for the layout with Start and End columns.
	HasTimeColumns bool
}

// Notice points at a line of the file that was ignored. Message never quotes the text.
type Notice struct {
	Line    int
	Message string
}

// Row is one entries-table row as typed values.
type Row struct {
	// Number is the 1-based position in the entries table, not counting the header rows.
	Number int
	// Date is midnight UTC on the session date; only the date part is meaningful.
	Date time.Time
	// StartTime and EndTime are "HH:MM" on 15-minute steps with EndTime after StartTime. Both are
	// empty when the row has no times (a legacy row that imports as Needs time, FR-IO-02).
	StartTime, EndTime string
	Group              domain.CategoryGroup
	// Category is the category name as written, trimmed. The import service matches it to a category.
	Category string
	// Clients are the client names on the row, in order, without case-insensitive duplicates.
	Clients         []string
	DurationMinutes int
	OnSite          bool
	// Notes is trimmed; empty means none.
	Notes string
}

// HasTimes reports whether the row carries start and end times.
func (r Row) HasTimes() bool { return r.StartTime != "" }

// RowError is a row that was skipped. Message never quotes the cell (see SkippedRow in the spec).
type RowError struct {
	// Number is the 1-based position in the entries table, not counting the header rows.
	Number  int
	Reason  SkipReason
	Field   string
	Message string
}

// ClientNames returns every distinct client name in the file, ignoring case: the Clients / Groups
// section first, then the rows, each in order of first appearance with its first spelling.
func (l Ledger) ClientNames() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(name string) {
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	for _, name := range l.Clients {
		add(name)
	}
	for _, row := range l.Rows {
		for _, name := range row.Clients {
			add(name)
		}
	}
	return out
}

// Parse reads a ledger in either layout (CLAUDE.md section 12.3). It reads at most MaxFileBytes.
// Columns are found by their header names, so their order does not matter. A bad row is reported in
// Skipped and the rest are kept (the policy in api/openapi.yaml). Parse never panics on any input.
func Parse(r io.Reader) (Ledger, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxFileBytes+1))
	if err != nil {
		return Ledger{}, fmt.Errorf("read ledger: %w", err)
	}
	if len(data) > MaxFileBytes {
		return Ledger{}, ErrTooLarge
	}
	if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		return Ledger{}, ErrNotText
	}
	p := &parser{}
	text := strings.TrimPrefix(string(data), "\uFEFF")
	for i, line := range strings.Split(text, "\n") {
		if err := p.line(i+1, strings.TrimSpace(line)); err != nil {
			return Ledger{}, err
		}
	}
	if p.table < tableRows {
		return Ledger{}, ErrNoEntriesTable
	}
	p.ledger.TotalRows = len(p.ledger.Rows) + len(p.ledger.Skipped)
	return p.ledger, nil
}

type section int

const (
	sectionNone section = iota
	sectionClients
	sectionEntries
	sectionOther
)

type tableState int

const (
	tableNone tableState = iota
	tableHeader
	tableRows
	tableDone
)

type parser struct {
	ledger  Ledger
	section section
	table   tableState
	cols    columns
	width   int
	number  int
	seen    map[string]struct{}
}

func (p *parser) line(n int, line string) error {
	switch {
	case strings.HasPrefix(line, "#"):
		p.heading(line)
	case p.section == sectionClients && isBullet(line):
		p.client(n, line)
	case strings.HasPrefix(line, "|"):
		return p.tableLine(line)
	case p.table == tableHeader:
		// A header with no separator row below it is not a table.
		return fmt.Errorf("%w: the header row is not followed by a separator row", ErrNoEntriesTable)
	case p.table == tableRows:
		p.table = tableDone
	}
	return nil
}

func (p *parser) heading(line string) {
	name := strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "#")))
	switch {
	case strings.HasPrefix(name, "client"):
		p.section = sectionClients
	case strings.HasPrefix(name, "entries"):
		p.section = sectionEntries
	default:
		p.section = sectionOther
	}
	if p.table == tableRows {
		p.table = tableDone
	}
}

func isBullet(line string) bool {
	return strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ")
}

// client records one bullet of the Clients / Groups section.
func (p *parser) client(n int, line string) {
	name := strings.TrimSpace(line[2:])
	if name == "" || name == noneMarker {
		return
	}
	if msg := checkClientName(name); msg != "" {
		p.ledger.ClientNotices = append(p.ledger.ClientNotices, Notice{Line: n, Message: "Client name " + msg})
		return
	}
	if p.seen == nil {
		p.seen = map[string]struct{}{}
	}
	key := strings.ToLower(name)
	if _, dup := p.seen[key]; dup {
		return
	}
	p.seen[key] = struct{}{}
	p.ledger.Clients = append(p.ledger.Clients, name)
}

// tableLine handles a line that starts with a pipe. Only the first table under Entries (or the first
// table of a file with no headings) is the entries table; any other table is ignored.
func (p *parser) tableLine(line string) error {
	cells := splitCells(line)
	switch p.table {
	case tableNone:
		if p.section != sectionEntries && p.section != sectionNone {
			return nil
		}
		cols, err := readHeader(cells)
		if err != nil {
			return err
		}
		p.cols, p.width, p.table = cols, len(cells), tableHeader
		p.ledger.HasTimeColumns = cols.start >= 0
	case tableHeader:
		if !isSeparator(cells) {
			return fmt.Errorf("%w: the header row is not followed by a separator row", ErrNoEntriesTable)
		}
		p.table = tableRows
	case tableRows:
		return p.row(cells)
	case tableDone:
	}
	return nil
}

func (p *parser) row(cells []string) error {
	p.number++
	if p.number > MaxRows {
		return ErrTooManyRows
	}
	// A row may leave out a trailing Notes cell entirely.
	if len(cells) == p.width-1 && p.cols.notes == p.width-1 {
		cells = append(cells, "")
	}
	if len(cells) != p.width {
		p.skip(RowError{Number: p.number, Reason: ReasonMalformedRow,
			Message: fmt.Sprintf("The row has %d columns; the table has %d.", len(cells), p.width)})
		return nil
	}
	row, bad := p.cols.parse(cells)
	if bad != nil {
		bad.Number = p.number
		p.skip(*bad)
		return nil
	}
	row.Number = p.number
	p.ledger.Rows = append(p.ledger.Rows, row)
	return nil
}

func (p *parser) skip(e RowError) { p.ledger.Skipped = append(p.ledger.Skipped, e) }

// splitCells splits a table row on unescaped pipes. `\|` is a literal pipe. The leading pipe and one
// closing pipe are borders, not cells. Splitting bytes is safe: `\` and `|` never occur inside a
// multi-byte UTF-8 sequence.
func splitCells(line string) []string {
	s := strings.TrimPrefix(strings.TrimSpace(line), "|")
	var (
		cells []string
		cur   strings.Builder
	)
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == '|':
			cur.WriteByte('|')
			i++
		case s[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}
	if rest := strings.TrimSpace(cur.String()); rest != "" {
		cells = append(cells, rest)
	}
	return cells
}

// isSeparator reports whether every cell looks like `---`, `:--`, `--:` or `:-:`.
func isSeparator(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		core := strings.Trim(c, ":")
		if core == "" || strings.Trim(core, "-") != "" {
			return false
		}
	}
	return true
}

// columns holds the index of each column, or -1 when the layout has none.
type columns struct {
	date, start, end, group, category, client, hours, onSite, notes int
}

// readHeader finds the columns by name, ignoring case, spaces and hyphens, so `On-site` and
// `on site` are the same. Start and End must come together.
func readHeader(cells []string) (columns, error) {
	c := columns{-1, -1, -1, -1, -1, -1, -1, -1, -1}
	targets := map[string]*int{
		"date": &c.date, "start": &c.start, "end": &c.end, "group": &c.group,
		"category": &c.category, "client": &c.client, "clients": &c.client, "hours": &c.hours,
		"onsite": &c.onSite, "notes": &c.notes,
	}
	for i, cell := range cells {
		name := strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(cell))
		dst, known := targets[name]
		if !known {
			continue
		}
		if *dst >= 0 {
			return columns{}, fmt.Errorf("%w: a column appears twice", ErrNoEntriesTable)
		}
		*dst = i
	}
	for name, idx := range map[string]int{"Date": c.date, "Group": c.group, "Category": c.category,
		"Client": c.client, "Hours": c.hours, "On-site": c.onSite, "Notes": c.notes} {
		if idx < 0 {
			return columns{}, fmt.Errorf("%w: the %s column is missing", ErrNoEntriesTable, name)
		}
	}
	if (c.start >= 0) != (c.end >= 0) {
		return columns{}, fmt.Errorf("%w: Start and End must both be present", ErrNoEntriesTable)
	}
	return c, nil
}

// parse converts one row's cells to typed values, or says why it cannot.
func (c columns) parse(cells []string) (Row, *RowError) {
	var (
		row Row
		bad *RowError
	)
	if row.Date, bad = parseDate(cells[c.date]); bad != nil {
		return Row{}, bad
	}
	if row.Group, row.Category, bad = parseCategory(cells[c.group], cells[c.category]); bad != nil {
		return Row{}, bad
	}
	var startText, endText string
	if c.start >= 0 {
		startText, endText = cells[c.start], cells[c.end]
	}
	var startMin, endMin int
	if row.StartTime, row.EndTime, startMin, endMin, bad = parseTimes(startText, endText); bad != nil {
		return Row{}, bad
	}
	if row.DurationMinutes, bad = parseDuration(cells[c.hours], row.HasTimes(), endMin-startMin); bad != nil {
		return Row{}, bad
	}
	if row.Clients, bad = parseClients(cells[c.client]); bad != nil {
		return Row{}, bad
	}
	if row.OnSite, bad = parseOnSite(cells[c.onSite]); bad != nil {
		return Row{}, bad
	}
	if row.Notes, bad = parseNotes(cells[c.notes]); bad != nil {
		return Row{}, bad
	}
	return row, nil
}

func parseDate(s string) (time.Time, *RowError) {
	if len(s) == len(time.DateOnly) {
		if t, err := time.Parse(time.DateOnly, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, &RowError{Reason: ReasonInvalidDate, Field: fieldDate, Message: "The date must be a real date written YYYY-MM-DD."}
}

var groupNames = map[string]domain.CategoryGroup{
	"direct service":   domain.GroupDirect,
	"direct":           domain.GroupDirect,
	"indirect service": domain.GroupIndirect,
	"indirect":         domain.GroupIndirect,
	"supervision":      domain.GroupSupervision,
}

func parseCategory(group, category string) (domain.CategoryGroup, string, *RowError) {
	g, ok := groupNames[strings.ToLower(group)]
	if !ok {
		return "", "", &RowError{Reason: ReasonUnknownGroup, Field: fieldGroup, Message: "The group must be Direct Service, Indirect Service or Supervision."}
	}
	if category == "" || utf8.RuneCountInString(category) > MaxCategoryLength || hasControl(category, false) {
		return "", "", &RowError{Reason: ReasonUnknownGroup, Field: fieldCategory,
			Message: fmt.Sprintf("The category must be 1 to %d characters of plain text.", MaxCategoryLength)}
	}
	return g, category, nil
}

// parseTimes reads the Start and End cells. Both empty (or the none marker) means no times.
func parseTimes(start, end string) (startText, endText string, startMin, endMin int, bad *RowError) {
	start, end = noneToEmpty(start), noneToEmpty(end)
	if start == "" && end == "" {
		return "", "", 0, 0, nil
	}
	if start == "" || end == "" {
		return "", "", 0, 0, &RowError{Reason: ReasonInvalidTime, Field: fieldStart, Message: "Start and End must both be given or both be empty."}
	}
	s, okS := parseClock(start)
	e, okE := parseClock(end)
	switch {
	case !okS:
		return "", "", 0, 0, &RowError{Reason: ReasonInvalidTime, Field: fieldStart, Message: "The start time must be HH:MM on a 15-minute step."}
	case !okE:
		return "", "", 0, 0, &RowError{Reason: ReasonInvalidTime, Field: fieldEnd, Message: "The end time must be HH:MM on a 15-minute step."}
	case e <= s:
		return "", "", 0, 0, &RowError{Reason: ReasonInvalidTime, Field: fieldEnd, Message: "The end time must be after the start time."}
	}
	return start, end, s, e, nil
}

func noneToEmpty(s string) string {
	if s == noneMarker {
		return ""
	}
	return s
}

// parseClock accepts HH:MM on a 15-minute step and returns minutes after midnight.
func parseClock(s string) (int, bool) {
	if len(s) != clockLength || s[2] != ':' {
		return 0, false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	h, m := int(s[0]-'0')*10+int(s[1]-'0'), int(s[3]-'0')*10+int(s[4]-'0')
	if h > 23 || m > 59 || m%entries.StepMinutes != 0 {
		return 0, false
	}
	return h*60 + m, true
}

// parseDuration returns the session length in minutes. With times it is their difference and the
// Hours cell may be empty; a cell that disagrees by more than rounding is an error. Without times
// the Hours cell is the only source (FR-IO-02).
func parseDuration(cell string, hasTimes bool, fromTimes int) (int, *RowError) {
	if hasTimes && cell == "" {
		return fromTimes, nil
	}
	minutes, ok := minutesFromHours(cell)
	if !ok {
		return 0, &RowError{Reason: ReasonInvalidHours, Field: fieldHours, Message: "The hours must be a number greater than 0 and at most 24."}
	}
	if hasTimes && abs(minutes-fromTimes) > 1 {
		return 0, &RowError{Reason: ReasonInvalidHours, Field: fieldHours, Message: "The hours do not match the start and end times."}
	}
	if hasTimes {
		return fromTimes, nil
	}
	return minutes, nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// minutesFromHours reads a plain decimal such as 1.5 and rounds it to whole minutes. Signs,
// exponents, NaN and infinities are not numbers here. The result is within 1 to 1440 (the column's range).
func minutesFromHours(s string) (int, bool) {
	whole, fraction, scale, ok := splitDecimal(s)
	if !ok {
		return 0, false
	}
	// Integer arithmetic: minutes = (whole + fraction/scale) * 60, rounded half up.
	minutes := (whole*60*scale + fraction*60 + scale/2) / scale
	if minutes < 1 || minutes > 24*60 {
		return 0, false
	}
	return minutes, true
}

// splitDecimal reads digits with at most one inner dot into the whole part, the fraction digits
// as an integer, and scale, the power of ten that the fraction is divided by.
func splitDecimal(s string) (whole, fraction, scale int, ok bool) {
	if s == "" || len(s) > maxHoursText {
		return 0, 0, 0, false
	}
	scale = 1
	seenDot := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '.' && !seenDot && i > 0 && i < len(s)-1:
			seenDot = true
		case c < '0' || c > '9':
			return 0, 0, 0, false
		case seenDot:
			fraction, scale = fraction*10+int(c-'0'), scale*10
		default:
			whole = whole*10 + int(c-'0')
		}
	}
	return whole, fraction, scale, true
}

// parseClients splits the Client cell on `;`. The none marker or an empty cell means no clients.
func parseClients(cell string) ([]string, *RowError) {
	var out []string
	seen := map[string]struct{}{}
	for _, part := range strings.Split(noneToEmpty(cell), ";") {
		name := strings.TrimSpace(part)
		if name == "" || name == noneMarker {
			continue
		}
		if msg := checkClientName(name); msg != "" {
			return nil, &RowError{Reason: ReasonInvalidClient, Field: fieldClient, Message: "A client name " + msg}
		}
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
		if len(out) > entries.MaxClients {
			return nil, &RowError{Reason: ReasonInvalidClient, Field: fieldClient, Message: fmt.Sprintf("A row can have at most %d clients.", entries.MaxClients)}
		}
	}
	return out, nil
}

// checkClientName returns the rest of a message when the name cannot be saved, using the same limits as the clients service.
func checkClientName(name string) string {
	switch {
	case utf8.RuneCountInString(name) > clients.MaxNameLength:
		return fmt.Sprintf("must be at most %d characters.", clients.MaxNameLength)
	case hasControl(name, false):
		return "must not contain control characters."
	}
	return ""
}

func parseOnSite(cell string) (bool, *RowError) {
	switch strings.ToLower(cell) {
	case "yes", "true":
		return true, nil
	case "no", "false", "":
		return false, nil
	}
	return false, &RowError{Reason: ReasonMalformedRow, Message: "The On-site value must be Yes or No."}
}

func parseNotes(cell string) (string, *RowError) {
	if hasControl(cell, true) || utf8.RuneCountInString(cell) > entries.MaxNotesLength {
		return "", &RowError{Reason: ReasonInvalidNotes, Field: fieldNotes,
			Message: fmt.Sprintf("The notes must be at most %d characters of plain text.", entries.MaxNotesLength)}
	}
	return cell, nil
}

// hasControl reports whether s has a control character. Notes may keep a tab.
func hasControl(s string, allowTab bool) bool {
	for _, r := range s {
		if unicode.IsControl(r) && (!allowTab || r != '\t') {
			return true
		}
	}
	return false
}
