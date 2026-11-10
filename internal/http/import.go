package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"regexp"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/http/gen"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/importexport"
)

const (
	// uploadPart is the one multipart part the import accepts (ImportMarkdownRequest in api/openapi.yaml).
	uploadPart = "file"
	// importPath is the route of the import (it is matched before routing, by limitBody).
	importPath = BasePath + "/import/markdown"
	// multipartOverhead is what a multipart body may hold beyond the file itself: the boundary lines
	// and the part's headers. The file's own limit (importexport.MaxFileBytes) is checked exactly
	// while it is read, so this only has to keep a request from carrying much more than a file.
	multipartOverhead = 64 << 10
	// maxImportBodyBytes is the largest body POST /import/markdown reads (SEC-29). It sits far below
	// the 6 MB a synchronous Lambda invocation accepts, even after API Gateway's base64 encoding.
	maxImportBodyBytes = importexport.MaxFileBytes + multipartOverhead
)

// uploadMediaTypes are the content types the contract allows for the file part. A part without a
// Content-Type is text/plain (RFC 7578 section 4.4).
var uploadMediaTypes = map[string]bool{"text/markdown": true, "text/plain": true}

// ImportService is the import use case the handler calls (S4-04). It authorizes the actor as
// themselves and stores the ledger as that actor's own data; handlers only translate (GO-10).
type ImportService interface {
	// Import parses a ledger and stores it in one transaction. A file that is refused whole returns
	// one of the importexport.Err* values; check them with errors.Is.
	Import(ctx context.Context, actor domain.Actor, r io.Reader) (importexport.Report, error)
}

// ImportMarkdown imports the caller's own Markdown ledger. The multipart body holds exactly one
// part, `file`, with a text content type; the size and row limits and the file's contents are
// checked before anything is stored. The Idempotency-Key header is required by the contract, and
// the middleware replays the report for a retry of the same upload (S2-09).
func (s *server) ImportMarkdown(ctx context.Context, req gen.ImportMarkdownRequestObject) (gen.ImportMarkdownResponseObject, error) {
	if req.Body == nil {
		return nil, errors.New("import markdown: body was not decoded")
	}
	file, err := readUpload(req.Body)
	if err != nil {
		return nil, err
	}
	report, err := s.imports.Import(ctx, ActorFrom(ctx), bytes.NewReader(file))
	if err != nil {
		return nil, err //nolint:wrapcheck // WriteError maps domain and import errors to problems (GO-33).
	}
	return gen.ImportMarkdown200JSONResponse(importReportDTO(report)), nil
}

// readUpload returns the contents of the `file` part. Anything else in the body is a 400 that names
// the field and never echoes what was sent; a file over the limit is the 413 of importexport.ErrTooLarge.
func readUpload(mr *multipart.Reader) ([]byte, error) {
	part, err := mr.NextPart()
	if errors.Is(err, io.EOF) {
		return nil, uploadProblem(uploadPart, "is required")
	}
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, err //nolint:wrapcheck // problemFor answers 413.
		}
		return nil, uploadProblem("body", "is not a valid multipart upload")
	}
	defer func() { _ = part.Close() }()
	if part.FormName() != uploadPart {
		return nil, uploadProblem(partFieldName(part.FormName()), "is not allowed")
	}
	if !uploadMediaTypes[mediaType(part.Header.Get("Content-Type"))] {
		return nil, uploadProblem(uploadPart, "must be a Markdown or plain text file")
	}
	// One byte past the limit tells a file of exactly 1 MiB from one that is too big.
	data, err := io.ReadAll(io.LimitReader(part, importexport.MaxFileBytes+1))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, err //nolint:wrapcheck // problemFor answers 413.
		}
		return nil, uploadProblem("body", "is not a valid multipart upload")
	}
	if len(data) > importexport.MaxFileBytes {
		return nil, importexport.ErrTooLarge
	}
	// The file must be the whole upload: the contract forbids any other part.
	next, err := mr.NextPart()
	switch {
	case errors.Is(err, io.EOF):
		return data, nil
	case err != nil:
		return nil, uploadProblem("body", "is not a valid multipart upload")
	default:
		defer func() { _ = next.Close() }()
		return nil, uploadProblem(partFieldName(next.FormName()), "is not allowed")
	}
}

// safePartName limits what a problem repeats of a client-chosen part name.
var safePartName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func partFieldName(name string) string {
	if safePartName.MatchString(name) {
		return name
	}
	return "body"
}

func mediaType(header string) string {
	if header == "" {
		return "text/plain"
	}
	mt, _, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	return mt
}

func uploadProblem(field, message string) error {
	return domain.NewValidationError(domain.FieldError{Field: field, Message: message})
}

// importReportDTO maps the service report to the contract. The report holds counts, reasons and client
// names only (the cells of skipped rows are never quoted), so there is nothing to filter out.
func importReportDTO(r importexport.Report) gen.ImportReport {
	merged := make([]gen.MergedClient, 0, len(r.MergedClients))
	for _, m := range r.MergedClients {
		merged = append(merged, gen.MergedClient{Client: clientDTO(m.Client), Created: m.Created})
	}
	skipped := make([]gen.SkippedRow, 0, len(r.SkippedRows))
	for _, s := range r.SkippedRows {
		row := gen.SkippedRow{Row: int32(s.Row), Reason: gen.SkipReason(s.Reason), Message: s.Message} //nolint:gosec // G115: at most 5,000 rows.
		if s.Field != "" {
			field := s.Field
			row.Field = &field
		}
		skipped = append(skipped, row)
	}
	return gen.ImportReport{
		TotalRows:      int32(r.TotalRows),      //nolint:gosec // G115: at most 5,000 rows.
		ImportedCount:  int32(r.ImportedCount),  //nolint:gosec // G115: at most 5,000 rows.
		NeedsTimeCount: int32(r.NeedsTimeCount), //nolint:gosec // G115: at most 5,000 rows.
		MergedClients:  merged,
		SkippedCount:   int32(len(skipped)), //nolint:gosec // G115: at most 5,000 rows.
		SkippedRows:    skipped,
	}
}

// importRefusal maps the errors that refuse a whole file to their problems; ok is false for any other error.
func importRefusal(err error) (Problem, bool) {
	switch {
	case errors.Is(err, importexport.ErrTooLarge):
		return Problem{Type: ProblemBodyTooLarge, Title: "File too large", Status: http.StatusRequestEntityTooLarge,
			Detail: fmt.Sprintf("The file must be at most %d bytes.", importexport.MaxFileBytes)}, true
	case errors.Is(err, importexport.ErrTooManyRows):
		return Problem{Type: ProblemImportTooManyRows, Title: "Too many rows", Status: http.StatusRequestEntityTooLarge,
			Detail: fmt.Sprintf("The entries table must have at most %d rows.", importexport.MaxRows)}, true
	case errors.Is(err, importexport.ErrNotText):
		return Problem{Type: ProblemValidation, Title: "Some fields are invalid", Status: http.StatusBadRequest,
			Errors: []ProblemField{{Field: uploadPart, Message: "must be UTF-8 text"}}}, true
	case errors.Is(err, importexport.ErrNoEntriesTable):
		return Problem{Type: ProblemValidation, Title: "Some fields are invalid", Status: http.StatusBadRequest,
			Errors: []ProblemField{{Field: uploadPart, Message: "must contain an entries table with the ledger's columns"}}}, true
	}
	return Problem{}, false
}
