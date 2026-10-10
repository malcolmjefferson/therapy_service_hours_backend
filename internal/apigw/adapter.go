// Package apigw adapts API Gateway HTTP API (payload format 2.0) events to a standard http.Handler,
// so the same router serves Lambda and local HTTP.
package apigw

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

// RequestIDSetter stores an upstream request ID in the request context.
type RequestIDSetter func(ctx context.Context, id string) context.Context

// Handler returns a Lambda handler that serves API Gateway events with h.
// API Gateway's request ID is passed on through setRequestID, so logs correlate across services.
func Handler(h http.Handler, setRequestID RequestIDSetter) func(context.Context, events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	return func(ctx context.Context, event events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
		if id := event.RequestContext.RequestID; id != "" {
			ctx = setRequestID(ctx, id)
		}
		req, err := toHTTPRequest(ctx, event)
		if err != nil {
			// A malformed event is API Gateway's fault, not the caller's; Lambda reports it as an error.
			return events.APIGatewayV2HTTPResponse{}, err
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return toEventResponse(rec.Result())
	}
}

func toHTTPRequest(ctx context.Context, event events.APIGatewayV2HTTPRequest) (*http.Request, error) {
	body := []byte(event.Body)
	if event.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(event.Body)
		if err != nil {
			return nil, fmt.Errorf("decode base64 body: %w", err)
		}
		body = decoded
	}

	path := event.RawPath
	if path == "" {
		path = "/"
	}
	u := &url.URL{Path: path, RawQuery: event.RawQueryString}
	method := event.RequestContext.HTTP.Method
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request %s: %w", method, err)
	}
	for name, value := range event.Headers {
		// Payload 2.0 joins repeated headers with commas; that is also valid HTTP.
		req.Header.Set(name, value)
	}
	if len(event.Cookies) > 0 {
		req.Header.Set("Cookie", strings.Join(event.Cookies, "; "))
	}
	req.ContentLength = int64(len(body))
	req.RemoteAddr = event.RequestContext.HTTP.SourceIP
	req.Host = event.RequestContext.DomainName
	return req, nil
}

func toEventResponse(res *http.Response) (events.APIGatewayV2HTTPResponse, error) {
	defer func() { _ = res.Body.Close() }() // Recorder bodies are in memory; Close cannot fail.
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return events.APIGatewayV2HTTPResponse{}, fmt.Errorf("read response body: %w", err)
	}

	out := events.APIGatewayV2HTTPResponse{
		StatusCode: res.StatusCode,
		Headers:    make(map[string]string, len(res.Header)),
		Cookies:    res.Header.Values("Set-Cookie"),
	}
	for name, values := range res.Header {
		if name == "Set-Cookie" {
			continue
		}
		out.Headers[name] = strings.Join(values, ",")
	}
	if isText(res.Header.Get("Content-Type")) {
		out.Body = string(body)
	} else {
		out.Body = base64.StdEncoding.EncodeToString(body)
		out.IsBase64Encoded = true
	}
	return out, nil
}

// isText reports whether a response with this content type can be returned as a plain string.
func isText(contentType string) bool {
	if contentType == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/json" ||
		strings.HasSuffix(mediaType, "+json") ||
		mediaType == "application/yaml"
}
