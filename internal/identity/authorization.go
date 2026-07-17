package identity

import "context"

type Principal struct {
	UserID    string
	Role      string
	SessionID string
}

func (principal Principal) CanEdit(ownerID string) bool {
	return principal.UserID != "" && (principal.UserID == ownerID || principal.Role == "moderator" || principal.Role == "admin")
}

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}
