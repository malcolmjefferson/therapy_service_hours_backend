package domain

// Actor is the current database-backed identity. Token roles never populate it.
type Actor struct {
	ID, OrganizationID, OrganizationName string
	Email, FirstName, LastName           string
	Active, Admin, Supervisor            bool
}

// Identity contains validated ID-token attributes, not permissions.
type Identity struct {
	Subject, Email, FirstName, LastName string
}
