package domain

import "fmt"

// Client is one of a user's saved clients or groups. The name is a pseudonym or initials only
// (CLAUDE.md §0.7).
type Client struct {
	ID   string
	Name string
}

// ClientPage is one page of a user's clients with the total across all pages.
type ClientPage struct {
	Items    []Client
	Page     int32
	PageSize int32
	Total    int64
}

// ClientNameTakenError means the user already has a client with that name, ignoring case.
// It carries the existing client so the form can select it. It matches ErrConflict.
type ClientNameTakenError struct {
	Existing Client
}

// Error reports the conflict without the name, which is user data.
func (e *ClientNameTakenError) Error() string { return "client name already saved" }

// Is makes the error match ErrConflict.
func (e *ClientNameTakenError) Is(target error) bool { return target == ErrConflict }

// ClientInUseError means entries still use the client and the caller did not confirm detaching it.
// It matches ErrConflict.
type ClientInUseError struct {
	EntryCount int32
}

// Error reports how many entries use the client.
func (e *ClientInUseError) Error() string {
	return fmt.Sprintf("client is used by %d entries", e.EntryCount)
}

// Is makes the error match ErrConflict.
func (e *ClientInUseError) Is(target error) bool { return target == ErrConflict }
