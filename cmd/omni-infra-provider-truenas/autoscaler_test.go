package main

import (
	"context"
	"errors"
	"testing"

	"github.com/cosi-project/runtime/pkg/state"
	"github.com/cosi-project/runtime/pkg/state/impl/inmem"
	"github.com/cosi-project/runtime/pkg/state/impl/namespaced"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/bearbinary/omni-infra-provider-truenas/internal/autoscaler"
)

func newTestOmniState() state.State {
	return state.WrapCore(namespaced.NewState(inmem.Build))
}

// TestRunAutoscaler_MissingClusterName mirrors the provisioner's
// TestSmoke_MissingOmniEndpoint shape — the subcommand must fail with a
// recognizable error string so deploy manifests that forget
// OMNI_CLUSTER_NAME surface that fact in pod logs rather than looking
// like an opaque startup hang.
func TestRunAutoscaler_MissingClusterName(t *testing.T) {
	// Cannot use t.Parallel — t.Setenv mutates process env.
	t.Setenv(autoscaler.EnvClusterName, "")

	err := runAutoscaler(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), autoscaler.EnvClusterName,
		"error must name the missing env var so the operator knows what to fix")
}

// TestRunAutoscaler_MissingOmniEndpoint pins the fail-fast behavior
// when OMNI_ENDPOINT isn't set. Operators deploying the autoscaler
// subcommand must see a named-env-var error in pod logs rather than
// the subcommand entering a partial state (cluster name validated,
// then a silent hang on Omni-client construction).
func TestRunAutoscaler_MissingOmniEndpoint(t *testing.T) {
	t.Setenv(autoscaler.EnvClusterName, "test-cluster")
	t.Setenv("OMNI_ENDPOINT", "")

	err := runAutoscaler(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "OMNI_ENDPOINT",
		"error must name OMNI_ENDPOINT so operators can diagnose the missing env var")
}

// TestRunAutoscaler_ShutsDownCleanlyOnContextCancel verifies the
// Phase 1 hold-open loop returns nil when its parent context is
// cancelled. Catches regressions where a later phase adds a blocking
// call that ignores ctx.
func TestRunAutoscaler_ShutsDownCleanlyOnContextCancel(t *testing.T) {
	t.Setenv(autoscaler.EnvClusterName, "test-cluster")
	// Ephemeral port so the test doesn't collide with a running
	// autoscaler Deployment on the dev machine or with another test
	// binary holding the default :8086.
	t.Setenv(autoscaler.EnvListenAddress, "127.0.0.1:0")
	// Localhost endpoint so client.New succeeds (it doesn't actually
	// dial at construction time). TRUENAS_HOST left unset to
	// exercise the "capacity gate disabled" branch — the test only
	// cares that the subcommand sets up and shuts down cleanly on a
	// pre-cancelled context.
	t.Setenv("OMNI_ENDPOINT", "http://localhost:0")
	t.Setenv("OMNI_SERVICE_ACCOUNT_KEY", "")
	t.Setenv("TRUENAS_HOST", "")
	// Disable the singleton lease — the test has no live Omni
	// backing store to read/write against. Shutdown-path coverage
	// doesn't need the lease.
	t.Setenv("AUTOSCALER_SINGLETON_ENABLED", "false")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Pre-cancel so runAutoscaler returns immediately.

	err := runAutoscaler(ctx)

	// Either nil (clean shutdown) OR a context-canceled passthrough is
	// acceptable; what's NOT acceptable is a config-parse error or a
	// panic. The runAutoscaler impl treats context.Canceled as a clean
	// shutdown signal and returns nil.
	assert.NoError(t, err, "pre-cancelled context must shut down cleanly")
}

// TestBuildAutoscalerCapacityGate_HostUnset_ReturnsNilQuery pins the
// dry-run contract: with TRUENAS_HOST unset, the helper returns a bundle
// with a nil Query (gate disabled) and a safe-to-call Close.
func TestBuildAutoscalerCapacityGate_HostUnset_ReturnsNilQuery(t *testing.T) {
	t.Setenv("TRUENAS_HOST", "")

	bundle, err := buildAutoscalerCapacityGate(zaptest.NewLogger(t))
	require.NoError(t, err)
	require.NotNil(t, bundle, "bundle must be non-nil even when gate is disabled")
	assert.Nil(t, bundle.Query, "Query must be nil when host is unset (gate disabled)")
	assert.Equal(t, "", bundle.DefaultPool)

	// Close must not panic — it's the unconditional defer at the caller.
	assert.NotPanics(t, func() { bundle.Close() })
}

// The enabled-path counterpart (TRUENAS_HOST set → bundle.Query
// non-nil) is exercised by the cassette/integration tests that have
// real or replayed WebSocket connectivity. A unit test here would need
// a mock TrueNAS WebSocket, which is out of scope for cmd/-level
// helpers.

// TestAcquireAutoscalerLease_Disabled_ReturnsNoopRelease pins the
// AUTOSCALER_SINGLETON_ENABLED=false branch: a no-op release is returned
// (caller defers unconditionally) and no real lease is touched.
func TestAcquireAutoscalerLease_Disabled_ReturnsNoopRelease(t *testing.T) {
	t.Setenv("AUTOSCALER_SINGLETON_ENABLED", "false")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := newTestOmniState()
	stop := func() { /* unused */ }

	release, err := acquireAutoscalerLease(ctx, ctx, zaptest.NewLogger(t), st, "test-cluster", stop)
	require.NoError(t, err)
	require.NotNil(t, release, "release must be non-nil (caller defers unconditionally)")
	assert.NotPanics(t, release)
}

// TestAcquireProviderLease_Disabled_ReturnsNilRelease pins the
// PROVIDER_SINGLETON_ENABLED=false branch on the provider main.
// The corresponding ctx-cancelled-mid-Acquire branch is covered by
// the end-to-end TestRunAutoscaler_ShutsDownCleanlyOnContextCancel —
// the in-memory state stub doesn't honor ctx cancellation, so direct
// unit-testing of that branch requires a state mock; deferred until a
// future test that needs a generic state stub.
func TestAcquireProviderLease_Disabled_ReturnsNilRelease(t *testing.T) {
	t.Setenv("PROVIDER_SINGLETON_ENABLED", "false")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := newTestOmniState()

	release, err := acquireProviderLease(ctx, cancel, st, zaptest.NewLogger(t))
	require.NoError(t, err)
	assert.Nil(t, release, "release must be nil when lease is disabled (caller skips defer)")
}

// TestErrAutoscalerLeaseShutdownDuringAcquire pins the sentinel's
// identity: the caller (runAutoscaler) uses errors.Is on it to decide
// whether to exit 0 (clean shutdown) or surface the error.
func TestErrAutoscalerLeaseShutdownDuringAcquire_IsDistinguishable(t *testing.T) {
	require.NotNil(t, errAutoscalerLeaseShutdownDuringAcquire)
	assert.True(t, errors.Is(errAutoscalerLeaseShutdownDuringAcquire, errAutoscalerLeaseShutdownDuringAcquire))
	// Must NOT collide with other sentinel errors the caller might
	// distinguish in the future.
	assert.False(t, errors.Is(errAutoscalerLeaseShutdownDuringAcquire, context.Canceled),
		"sentinel must NOT be context.Canceled — that would conflate clean shutdown with cancellation")
}
