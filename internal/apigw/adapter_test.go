package apigw_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
)

type ctxKey struct{}

func setID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

func event(method, path string) events.APIGatewayV2HTTPRequest {
	e := events.APIGatewayV2HTTPRequest{RawPath: path}
	e.RequestContext.HTTP.Method = method
	e.RequestContext.RequestID = "apigw-req-1"
	e.RequestContext.DomainName = "example.execute-api.us-west-2.amazonaws.com"
	e.RequestContext.HTTP.SourceIP = "192.0.2.10"
	return e
}

func TestHandler_ConvertsTheEventIntoAnHTTPRequest(t *testing.T) {
	t.Parallel()
	var got struct {
		method, path, query, accept, cookie, body, requestID, host, remote string
		length                                                             int64
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.path, got.query = r.Method, r.URL.Path, r.URL.RawQuery
		got.accept, got.cookie, got.body = r.Header.Get("Accept"), r.Header.Get("Cookie"), string(b)
		got.requestID, _ = r.Context().Value(ctxKey{}).(string)
		got.host, got.remote, got.length = r.Host, r.RemoteAddr, r.ContentLength
		w.WriteHeader(http.StatusNoContent)
	})

	e := event(http.MethodPost, "/api/v1/entries")
	e.RawQueryString = "page=2&sort=date"
	e.Headers = map[string]string{"accept": "application/json"}
	e.Cookies = []string{"a=1", "b=2"}
	e.Body = base64.StdEncoding.EncodeToString([]byte(`{"x":1}`))
	e.IsBase64Encoded = true

	res, err := apigw.Handler(h, setID)(context.Background(), e)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d", res.StatusCode)
	}
	checks := map[string][2]string{
		"method":     {got.method, http.MethodPost},
		"path":       {got.path, "/api/v1/entries"},
		"query":      {got.query, "page=2&sort=date"},
		"accept":     {got.accept, "application/json"},
		"cookie":     {got.cookie, "a=1; b=2"},
		"body":       {got.body, `{"x":1}`},
		"request ID": {got.requestID, "apigw-req-1"},
		"host":       {got.host, "example.execute-api.us-west-2.amazonaws.com"},
		"remote":     {got.remote, "192.0.2.10"},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", name, c[0], c[1])
		}
	}
	if got.length != 7 {
		t.Errorf("content length = %d, want 7", got.length)
	}
}

func TestHandler_ConvertsTheResponse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		contentType string
		body        []byte
		wantBody    string
		wantBase64  bool
	}{
		{"JSON stays text", "application/json", []byte(`{"ok":true}`), `{"ok":true}`, false},
		{"problem+json stays text", "application/problem+json; charset=utf-8", []byte(`{"status":404}`), `{"status":404}`, false},
		{"binary is base64", "application/pdf", []byte{0x25, 0x50, 0x00, 0xff}, base64.StdEncoding.EncodeToString([]byte{0x25, 0x50, 0x00, 0xff}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.Header().Add("Vary", "Accept")
				w.Header().Add("Vary", "Origin")
				w.Header().Add("Set-Cookie", "s=1; Secure")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write(tt.body)
			})
			res, err := apigw.Handler(h, setID)(context.Background(), event(http.MethodGet, "/x"))
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if res.StatusCode != http.StatusAccepted || res.Body != tt.wantBody || res.IsBase64Encoded != tt.wantBase64 {
				t.Errorf("response = %+v", res)
			}
			if res.Headers["Vary"] != "Accept,Origin" {
				t.Errorf("Vary = %q", res.Headers["Vary"])
			}
			if _, ok := res.Headers["Set-Cookie"]; ok || len(res.Cookies) != 1 || res.Cookies[0] != "s=1; Secure" {
				t.Errorf("cookies = %v, headers = %v", res.Cookies, res.Headers)
			}
		})
	}
}

func TestHandler_RejectsAnUndecodableBody(t *testing.T) {
	t.Parallel()
	e := event(http.MethodPost, "/x")
	e.Body = "not base64!"
	e.IsBase64Encoded = true
	called := false
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	if _, err := apigw.Handler(h, setID)(context.Background(), e); err == nil {
		t.Error("expected an error")
	}
	if called {
		t.Error("handler must not run for a malformed event")
	}
}
