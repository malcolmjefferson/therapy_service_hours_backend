// Package loghoursqueries holds the sqlc-generated queries for categories, clients and entries
// (S2-02) and the sort allowlist that selects one of the fixed list queries (SEC-24, GO-14).
// Run go generate ./... after editing db/queries or db/migrations; do not hand-edit generated files.
package loghoursqueries

//go:generate go tool sqlc generate -f ../../../sqlc.yaml
