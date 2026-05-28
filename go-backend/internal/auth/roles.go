package auth

// Role constants centralise the role string literals used across the codebase.
// Handlers, middleware, and stores should refer to these instead of bare
// string literals so a typo turns into a compile-time error.
const (
	RoleUser       = "user"
	RoleAdmin      = "admin"
	RoleSuperAdmin = "superadmin"
	RoleAPIUser    = "api-user"
)
