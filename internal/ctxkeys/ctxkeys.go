// Package ctxkeys defines typed context keys shared across packages.
package ctxkeys

type contextKey string

const (
	UserIDKey    contextKey = "user_id"
	RequestIDKey contextKey = "request_id"
	APIKeyKey    contextKey = "api_key"
	PrincipalKey contextKey = "principal"
)
