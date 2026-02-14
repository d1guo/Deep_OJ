package worker

import "context"

type attemptIDCtxKey struct{}

func withAttemptID(ctx context.Context, attemptID int64) context.Context {
	return context.WithValue(ctx, attemptIDCtxKey{}, attemptID)
}

func attemptIDFromContext(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	raw := ctx.Value(attemptIDCtxKey{})
	if raw == nil {
		return 0
	}
	attemptID, ok := raw.(int64)
	if !ok {
		return 0
	}
	return attemptID
}
