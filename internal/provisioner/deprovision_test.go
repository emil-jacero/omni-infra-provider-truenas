package provisioner

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bearbinary/omni-infra-provider-truenas/internal/client"
)

func TestCleanupVM_GracefulShutdown_VMStopsInTime(t *testing.T) {
	var stopCalls atomic.Int32
	var forceStopped atomic.Bool

	p := NewProvisioner(client.NewMockClient(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "vm.stop":
			call := stopCalls.Add(1)
			// Check if force flag is set
			if call == 1 {
				// First call: graceful (force=false)
				return true, nil
			}

			forceStopped.Store(true)

			return true, nil
		case "vm.query":
			// Return STOPPED after graceful signal
			return managedVM(42, "STOPPED"), nil
		case "vm.delete":
			return true, nil
		default:
			return nil, nil
		}
	}), ProviderConfig{
		DefaultPool:             "tank",
		GracefulShutdownTimeout: 100 * time.Millisecond,
		PollInterval:            10 * time.Millisecond,
	})

	err := p.cleanupVM(context.Background(), testLogger(), 42)
	require.NoError(t, err)
	assert.False(t, forceStopped.Load(), "should not force stop if VM stopped gracefully")
}

func TestCleanupVM_GracefulShutdown_Timeout_ForcesStop(t *testing.T) {
	var forceStopped atomic.Bool

	p := NewProvisioner(client.NewMockClient(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "vm.stop":
			// Check params for force flag
			var raw []json.RawMessage
			json.Unmarshal(params, &raw) //nolint:errcheck
			if len(raw) >= 2 {
				var opts map[string]any
				json.Unmarshal(raw[1], &opts) //nolint:errcheck
				if force, ok := opts["force"].(bool); ok && force {
					forceStopped.Store(true)
				}
			}

			return true, nil
		case "vm.query":
			// VM never stops gracefully — stays RUNNING
			return managedVM(42, "RUNNING"), nil
		case "vm.delete":
			return true, nil
		default:
			return nil, nil
		}
	}), ProviderConfig{
		DefaultPool:             "tank",
		GracefulShutdownTimeout: 100 * time.Millisecond,
		PollInterval:            10 * time.Millisecond,
	})

	err := p.cleanupVM(context.Background(), testLogger(), 42)
	require.NoError(t, err)
	assert.True(t, forceStopped.Load(), "should force stop after graceful timeout")
}

func TestCleanupVM_ContextCancelled_DuringGraceful(t *testing.T) {
	p := NewProvisioner(client.NewMockClient(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.stop":
			return true, nil
		case "vm.query":
			return managedVM(42, "RUNNING"), nil
		case "vm.delete":
			return true, nil
		default:
			return nil, nil
		}
	}), ProviderConfig{
		DefaultPool:             "tank",
		GracefulShutdownTimeout: 200 * time.Millisecond,
		PollInterval:            10 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel after 50ms — should not wait the full graceful timeout
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_ = p.cleanupVM(ctx, testLogger(), 42) // May error due to cancelled context on subsequent calls
	elapsed := time.Since(start)

	// Key assertion: should NOT wait the full graceful timeout
	assert.Less(t, elapsed, 2*time.Second, "should exit quickly when context cancelled")
}

// TestCleanupVM_DeleteRunsEvenAfterParentCancel pins the
// context.WithoutCancel contract: when the parent ctx is cancelled
// MID-deprovision (after ownership check passes), cleanupVM MUST still
// call DeleteVM with a context that is not yet cancelled. A regression
// to `cleanupCtx := ctx` would silently skip the force-stop and delete,
// leaving orphan VMs to be picked up on the next cleanup cycle. The
// CHANGELOG promises this contract; this test pins it.
//
// The cancellation must fire AFTER GetVM (ownership check) returns,
// because the rate-limit semaphore in client.call(ctx) short-circuits on
// pre-cancelled ctx — that's the correct behavior for ownership-check
// time, and is not what WithoutCancel is protecting.
func TestCleanupVM_DeleteRunsEvenAfterParentCancel(t *testing.T) {
	var (
		queryCalls   atomic.Int32
		deleteCalls  atomic.Int32
		deleteCtxOK  atomic.Bool
		stopForceCtx atomic.Bool
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := NewProvisioner(client.NewMockClientCtx(func(callCtx context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			// Cancel the parent ctx as soon as the ownership check
			// completes — simulates an in-flight deprovision interrupted
			// by a shutdown signal mid-call.
			queryCalls.Add(1)
			cancel()
			return managedVM(99, "RUNNING"), nil
		case "vm.stop":
			// Force-stop runs on cleanupCtx (WithoutCancel); record
			// whether the ctx visible at the mock is alive.
			if callCtx.Err() == nil {
				stopForceCtx.Store(true)
			}
			return true, nil
		case "vm.delete":
			deleteCalls.Add(1)
			if callCtx.Err() == nil {
				deleteCtxOK.Store(true)
			}
			return true, nil
		default:
			return nil, nil
		}
	}), ProviderConfig{
		DefaultPool:             "tank",
		GracefulShutdownTimeout: 50 * time.Millisecond,
		PollInterval:            10 * time.Millisecond,
	})

	_ = p.cleanupVM(ctx, testLogger(), 99)

	assert.Equal(t, int32(1), deleteCalls.Load(), "DeleteVM must run even when parent ctx is cancelled mid-flight")
	assert.True(t, deleteCtxOK.Load(), "DeleteVM ctx must NOT be cancelled (proves WithoutCancel contract)")
	assert.True(t, stopForceCtx.Load(), "force-StopVM ctx must NOT be cancelled either")
}

func TestCleanupVM_VMAlreadyStopped(t *testing.T) {
	p := NewProvisioner(client.NewMockClient(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.stop":
			return true, nil
		case "vm.query":
			return managedVM(42, "STOPPED"), nil
		case "vm.delete":
			return true, nil
		default:
			return nil, nil
		}
	}), ProviderConfig{DefaultPool: "tank", PollInterval: 10 * time.Millisecond})

	err := p.cleanupVM(context.Background(), testLogger(), 42)
	require.NoError(t, err)
}

func TestCleanupVM_VMNotFound_DuringPoll(t *testing.T) {
	p := NewProvisioner(client.NewMockClient(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.stop":
			return true, nil
		case "vm.query":
			return nil, &client.APIError{Code: client.ErrCodeNotFound, Message: "not found"}
		case "vm.delete":
			return true, nil
		default:
			return nil, nil
		}
	}), ProviderConfig{DefaultPool: "tank", PollInterval: 10 * time.Millisecond})

	err := p.cleanupVM(context.Background(), testLogger(), 42)
	require.NoError(t, err, "should succeed if VM disappears during poll")
}
