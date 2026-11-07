package observability

import "context"

// RequestMeta carries per-request identifiers for log correlation.
// It is stored as a pointer so middleware further down the chain (for example
// auth, which learns the user id) can fill it in and the access log still sees it.
type RequestMeta struct {
	RequestID string
	UserID    string
}

type requestMetaKey struct{}

// WithRequestMeta stores meta in ctx.
func WithRequestMeta(ctx context.Context, meta *RequestMeta) context.Context {
	return context.WithValue(ctx, requestMetaKey{}, meta)
}

// RequestMetaFrom returns the request metadata stored in ctx, or nil.
func RequestMetaFrom(ctx context.Context) *RequestMeta {
	meta, _ := ctx.Value(requestMetaKey{}).(*RequestMeta)
	return meta
}

// RequestIDFrom returns the request id stored in ctx, or "".
func RequestIDFrom(ctx context.Context) string {
	if meta := RequestMetaFrom(ctx); meta != nil {
		return meta.RequestID
	}
	return ""
}
