package access

import "context"

type Scope struct {
	TenantID  string
	ProjectID string
}

type scopeKey struct{}

type ProjectReadCheck func(ctx context.Context, userID string, scope Scope) (bool, error)

type projectReadKey struct{}

type projectRead struct {
	userID string
	scope  Scope
}

func WithScope(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

func FromContext(ctx context.Context) (Scope, bool) {
	scope, ok := ctx.Value(scopeKey{}).(Scope)
	return scope, ok
}

func WithProjectRead(ctx context.Context, userID string, scope Scope) context.Context {
	return context.WithValue(ctx, projectReadKey{}, projectRead{userID: userID, scope: scope})
}

func HasProjectRead(ctx context.Context, userID string, scope Scope) bool {
	grant, ok := ctx.Value(projectReadKey{}).(projectRead)
	return ok && grant.userID == userID && grant.scope == scope
}
