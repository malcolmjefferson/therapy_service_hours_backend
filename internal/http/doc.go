// Package httpapi is the transport layer: chi router, middleware, handlers and the single place
// where errors become RFC 7807 problem+json responses (GO-10, GO-33). It lives in internal/http;
// the package is named httpapi so it does not shadow net/http.
package httpapi
