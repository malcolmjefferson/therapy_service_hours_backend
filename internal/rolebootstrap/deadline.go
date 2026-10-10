package rolebootstrap

import (
	"context"
	"time"
)

// ResponseMargin is how long before the Lambda deadline the provider stops waiting for the worker,
// so it always has time to send CloudFormation a FAILED response instead of timing out silently
// (CloudFormation would otherwise wait up to an hour for a response that never comes).
const ResponseMargin = 15 * time.Second

// WithResponseMargin returns a context that ends margin before ctx's deadline. Without a deadline,
// or when less than margin remains, it returns ctx unchanged with a no-op cancel.
func WithResponseMargin(ctx context.Context, margin time.Duration) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= margin {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, deadline.Add(-margin))
}
