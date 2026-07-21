package identity

import "context"

type Principal struct {
	UserID    string
	Role      string
	SessionID string
}

// CanEdit reports whether the authenticated principal owns the resource or has a moderator or admin role; principals with an empty user ID cannot edit.
func (principal Principal) CanEdit(ownerID string) bool {
	return principal.UserID != "" && (principal.UserID == ownerID || principal.Role == "moderator" || principal.Role == "admin")
}

type principalContextKey struct{}

// WithPrincipal returns a child context carrying principal for later retrieval by PrincipalFromContext.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext returns the Principal stored by WithPrincipal and reports whether a value of that type was present.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}
