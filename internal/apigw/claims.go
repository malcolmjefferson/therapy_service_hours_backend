package apigw

import (
	"context"
	"maps"
)

type claimsKey struct{}

// JWTClaimsFrom returns a copy of claims validated by the API Gateway JWT authorizer.
// Only the Lambda adapter can attach claims; HTTP headers never supply identity.
// The copy prevents downstream code from changing the identity seen by other handlers.
func JWTClaimsFrom(ctx context.Context) (map[string]string, bool) {
	claims, ok := ctx.Value(claimsKey{}).(map[string]string)
	return maps.Clone(claims), ok && len(claims) > 0
}
