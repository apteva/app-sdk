package sdk

import "context"

type appInstallationContextKey struct{}

// WithAppInstallation selects one exact installation for CallAppContext,
// CallAppResultContext and CallAppBatchContext. The server still checks the
// caller's bindings, permissions and project. The selection applies to this
// call context and never changes the installation's routing default.
func WithAppInstallation(ctx context.Context, installID int64) context.Context {
	return context.WithValue(ctx, appInstallationContextKey{}, installID)
}

func appInstallationFromContext(ctx context.Context) int64 {
	id, _ := ctx.Value(appInstallationContextKey{}).(int64)
	return id
}
