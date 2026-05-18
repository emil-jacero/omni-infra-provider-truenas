package cleanup

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/bearbinary/omni-infra-provider-truenas/internal/client"
)

func testCleaner(handler client.MockHandler, activeImages map[string]bool) *Cleaner {
	return &Cleaner{
		client: client.NewMockClient(handler),
		config: Config{Pool: "default", CleanupInterval: time.Hour},
		logger: zap.NewNop(),
		activeImageIDs: func() map[string]bool {
			return activeImages
		},
	}
}

// managedZvols creates a []client.ManagedZvol for testing.
func managedZvols(requestIDs ...string) []client.ManagedZvol {
	var result []client.ManagedZvol
	for _, id := range requestIDs {
		result = append(result, client.ManagedZvol{
			Path:      "default/omni-vms/" + id,
			RequestID: id,
		})
	}
	return result
}

// managedDatasetResponse returns a mock pool.dataset.query response with user properties.
// Used by runOnce tests where ListManagedZvols is called internally.
func managedDatasetResponse(requestIDs ...string) []map[string]any {
	var datasets []map[string]any
	for _, id := range requestIDs {
		datasets = append(datasets, map[string]any{
			"id": "default/omni-vms/" + id,
			"user_properties": map[string]any{
				"org.omni:managed":    map[string]any{"value": "true"},
				"org.omni:request-id": map[string]any{"value": id},
			},
		})
	}
	return datasets
}

// --- Run / lifecycle tests ---

func TestCleanerRun_CancelsOnContext(t *testing.T) {
	cl := &Cleaner{
		config:         Config{CleanupInterval: time.Hour},
		logger:         zap.NewNop(),
		activeImageIDs: func() map[string]bool { return nil },
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		cl.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not exit after context cancellation")
	}
}

func TestNew_DefaultIntervals(t *testing.T) {
	c := client.NewMockClient(nil)
	cl := New(c, Config{Pool: "tank"}, zap.NewNop(),
		func() map[string]bool { return nil },
		nil,
	)

	assert.Equal(t, time.Hour, cl.config.CleanupInterval)
	assert.Equal(t, 30*time.Minute, cl.config.OrphanGracePeriod)
}

func TestNew_CustomIntervals(t *testing.T) {
	c := client.NewMockClient(nil)
	cl := New(c, Config{Pool: "tank", CleanupInterval: 10 * time.Minute, OrphanGracePeriod: 5 * time.Minute}, zap.NewNop(),
		func() map[string]bool { return nil },
		nil,
	)

	assert.Equal(t, 10*time.Minute, cl.config.CleanupInterval)
	assert.Equal(t, 5*time.Minute, cl.config.OrphanGracePeriod)
}

// --- cleanupISOs tests ---

func TestCleanupISOs_AllStale_RecreatesDataset(t *testing.T) {
	var recreated bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "filesystem.listdir":
			return []client.FileEntry{
				{Name: "abc123.iso", Type: "FILE"},
				{Name: "def456.iso", Type: "FILE"},
			}, nil
		case "pool.dataset.delete":
			return nil, nil
		case "pool.dataset.create":
			recreated = true
			return &client.Dataset{ID: "default/talos-iso"}, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupISOs(context.Background())
	assert.True(t, recreated, "should recreate dataset when all ISOs are stale")
}

func TestCleanupISOs_SomeActive_SkipsCleanup(t *testing.T) {
	var recreated bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "filesystem.listdir":
			return []client.FileEntry{
				{Name: "abc123.iso", Type: "FILE"},
				{Name: "def456.iso", Type: "FILE"},
			}, nil
		case "pool.dataset.delete":
			recreated = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{"abc123": true})

	cl.cleanupISOs(context.Background())
	assert.False(t, recreated, "should NOT recreate dataset when some ISOs are active")
}

func TestCleanupISOs_NoISOs_DoesNothing(t *testing.T) {
	var recreated bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "filesystem.listdir":
			return []client.FileEntry{}, nil
		case "pool.dataset.delete":
			recreated = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupISOs(context.Background())
	assert.False(t, recreated, "should not recreate dataset when no ISOs exist")
}

func TestCleanupISOs_IgnoresNonISOFiles(t *testing.T) {
	var recreated bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "filesystem.listdir":
			return []client.FileEntry{
				{Name: "readme.txt", Type: "FILE"},
				{Name: "subdir", Type: "DIRECTORY"},
			}, nil
		case "pool.dataset.delete":
			recreated = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupISOs(context.Background())
	assert.False(t, recreated, "should ignore non-ISO files")
}

func TestCleanupISOs_NilActiveIDs_Skips(t *testing.T) {
	cl := &Cleaner{
		client: client.NewMockClient(func(method string, _ json.RawMessage) (any, error) {
			if method == "filesystem.listdir" {
				return []client.FileEntry{{Name: "abc.iso", Type: "FILE"}}, nil
			}
			return nil, nil
		}),
		config:         Config{Pool: "default"},
		logger:         zap.NewNop(),
		activeImageIDs: func() map[string]bool { return nil },
	}

	cl.cleanupISOs(context.Background())
}

// --- cleanupOrphanVMs tests ---

func TestCleanupOrphanVMs_DeletesOrphans(t *testing.T) {
	var deleted []int
	cl := testCleaner(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{
				{ID: 1, Name: "omni_active_vm", Description: "Managed by Omni infra provider (request-id: active-vm)"},
				{ID: 2, Name: "omni_orphan_vm", Description: "Managed by Omni infra provider (request-id: orphan-vm)"},
				{ID: 3, Name: "not_omni_vm"},
			}, nil
		case "vm.stop":
			return nil, nil
		case "vm.delete":
			var args []json.RawMessage
			_ = json.Unmarshal(params, &args)
			var id int
			_ = json.Unmarshal(args[0], &id)
			deleted = append(deleted, id)
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanVMs(context.Background(), managedZvols("active-vm"), nil)
	assert.Equal(t, []int{2}, deleted, "should only delete orphan omni_ VMs whose zvol is gone")
}

func TestCleanupOrphanVMs_SkipsNonOmniVMs(t *testing.T) {
	var deleteCalled bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{
				{ID: 1, Name: "manual_vm"},
				{ID: 2, Name: "plex"},
			}, nil
		case "vm.delete":
			deleteCalled = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanVMs(context.Background(), managedZvols(), nil)
	assert.False(t, deleteCalled, "should not delete VMs without omni_ prefix")
}

func TestCleanupOrphanVMs_AllHaveZvols_NoneDeleted(t *testing.T) {
	var deleteCalled bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{
				{ID: 1, Name: "omni_vm_1"},
				{ID: 2, Name: "omni_vm_2"},
			}, nil
		case "vm.delete":
			deleteCalled = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanVMs(context.Background(), managedZvols("vm-1", "vm-2"), nil)
	assert.False(t, deleteCalled, "should not delete VMs when all have backing zvols")
}

func TestCleanupOrphanVMs_StopFails_StillDeletes(t *testing.T) {
	var deleted bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{{ID: 1, Name: "omni_orphan", Description: "Managed by Omni infra provider (request-id: orphan)"}}, nil
		case "vm.stop":
			return nil, &client.APIError{Code: 99, Message: "stop failed"}
		case "vm.delete":
			deleted = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanVMs(context.Background(), managedZvols(), nil) // No zvols → orphan
	assert.True(t, deleted, "should still delete VM even if stop fails")
}

func TestCleanupOrphanVMs_ProviderRestart_DoesNotDeleteActiveVMs(t *testing.T) {
	var deleteCalled bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{
				{ID: 1, Name: "omni_talos_cp_1"},
				{ID: 2, Name: "omni_talos_worker_1"},
			}, nil
		case "vm.delete":
			deleteCalled = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanVMs(context.Background(), managedZvols("talos-cp-1", "talos-worker-1"), nil)
	assert.False(t, deleteCalled, "must NOT delete VMs after restart when zvols exist")
}

func TestCleanupOrphanVMs_ListVMsFails_SkipsCleanup(t *testing.T) {
	var deleteCalled bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return nil, &client.APIError{Code: 99, Message: "vm query failed"}
		case "vm.delete":
			deleteCalled = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanVMs(context.Background(), managedZvols(), nil)
	assert.False(t, deleteCalled, "must not delete VMs when ListVMs fails")
}

// --- cleanupOrphanZvols tests ---

func TestCleanupOrphanZvols_DeletesOrphans(t *testing.T) {
	var deleted []string
	cl := testCleaner(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{{ID: 1, Name: "omni_active_request_123"}}, nil
		case "pool.dataset.delete":
			var args []json.RawMessage
			_ = json.Unmarshal(params, &args)
			var path string
			_ = json.Unmarshal(args[0], &path)
			deleted = append(deleted, path)
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanZvols(context.Background(), managedZvols("active-request-123", "orphan-request-456"), nil)

	require.Len(t, deleted, 1)
	assert.Equal(t, "default/omni-vms/orphan-request-456", deleted[0])
}

func TestCleanupOrphanZvols_AllHaveVMs_NoneDeleted(t *testing.T) {
	var deleteCalled bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{{ID: 1, Name: "omni_req_1"}}, nil
		case "pool.dataset.delete":
			deleteCalled = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanZvols(context.Background(), managedZvols("req-1"), nil)
	assert.False(t, deleteCalled, "should not delete zvols that have corresponding VMs")
}

func TestCleanupOrphanZvols_DatasetPrefix_FindsZvols(t *testing.T) {
	var deleted []string
	cl := testCleaner(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{}, nil
		case "pool.dataset.delete":
			var args []json.RawMessage
			_ = json.Unmarshal(params, &args)
			var path string
			_ = json.Unmarshal(args[0], &path)
			deleted = append(deleted, path)
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	deepZvol := []client.ManagedZvol{{Path: "default/previewk8/omni-vms/deep-orphan", RequestID: "deep-orphan"}}
	cl.cleanupOrphanZvols(context.Background(), deepZvol, nil)

	require.Len(t, deleted, 1)
	assert.Equal(t, "default/previewk8/omni-vms/deep-orphan", deleted[0])
}

func TestCleanupOrphanZvols_ListVMsFails_SkipsCleanup(t *testing.T) {
	var deleteCalled bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return nil, &client.APIError{Code: 99, Message: "vm query failed"}
		case "pool.dataset.delete":
			deleteCalled = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanZvols(context.Background(), managedZvols("some-req"), nil)
	assert.False(t, deleteCalled, "must not delete zvols when VM query fails")
}

func TestCleanupOrphanZvols_EmptyManagedZvols_Noop(t *testing.T) {
	var vmQueryCalled bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		if method == "vm.query" {
			vmQueryCalled = true
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanZvols(context.Background(), nil, nil)
	assert.False(t, vmQueryCalled, "should skip VM query when no managed zvols exist")
}

func TestCleanupOrphanZvols_HyphenToUnderscoreMapping(t *testing.T) {
	tests := []struct {
		zvolID string
		vmName string
	}{
		{"default/omni-vms/talos-test-workers-abc", "omni_talos_test_workers_abc"},
		{"default/omni-vms/simple", "omni_simple"},
		{"tank/omni-vms/a-b-c-d", "omni_a_b_c_d"},
	}

	for _, tt := range tests {
		t.Run(tt.zvolID, func(t *testing.T) {
			parts := strings.Split(tt.zvolID, "/")
			requestID := parts[len(parts)-1]
			vmName := "omni_" + strings.ReplaceAll(requestID, "-", "_")
			assert.Equal(t, tt.vmName, vmName)
		})
	}
}

// --- Partial deprovision crash scenarios ---

func TestCleanupOrphanVMs_CrashAfterZvolDelete_CleansUpVM(t *testing.T) {
	var vmDeleted bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{{ID: 42, Name: "omni_crashed_provision", Description: "Managed by Omni infra provider (request-id: crashed-provision)"}}, nil
		case "vm.stop":
			return nil, nil
		case "vm.delete":
			vmDeleted = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanVMs(context.Background(), managedZvols(), nil) // No zvols → VM is orphan
	assert.True(t, vmDeleted, "should delete VM when its zvol was already deleted by deprovision")
}

func TestCleanupOrphanZvols_CrashAfterVMDelete_CleansUpZvol(t *testing.T) {
	var zvolDeleted bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{}, nil // No VMs
		case "pool.dataset.delete":
			zvolDeleted = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanZvols(context.Background(), managedZvols("crashed-provision"), nil)
	assert.True(t, zvolDeleted, "should delete zvol when its VM was already deleted by deprovision")
}

func TestCleanupOrphanVMs_FullDeprovisionSuccess_Noop(t *testing.T) {
	var deleteCalled bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{}, nil
		case "vm.delete":
			deleteCalled = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanVMs(context.Background(), managedZvols(), nil)
	assert.False(t, deleteCalled, "nothing to clean when deprovision succeeded fully")
}

func TestCleanupOrphanVMs_ManuallyCreatedOmniVM_NotDeleted(t *testing.T) {
	var deleteCalled bool
	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{{ID: 1, Name: "omni_manual_test"}}, nil
		case "vm.delete":
			deleteCalled = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanVMs(context.Background(), managedZvols("manual-test"), nil)
	assert.False(t, deleteCalled, "should not delete VM when matching zvol exists")
}

// --- Live-MachineRequest cross-reference (the f9xkk2 fix) ---

// TestCleanupOrphanVMs_LiveCrossRef_DoubleOrphan pins the fix for the
// "both VM and zvol alive, no MachineRequest in Omni" case the partial-
// orphan heuristic alone misses. Pre-fix (the f9xkk2 incident, 2026-04-28):
// a VM whose zvol still existed survived every cleanup cycle for 24+
// hours despite its MachineRequest being deleted. The live cross-reference
// catches this on the next cycle.
func TestCleanupOrphanVMs_LiveCrossRef_DoubleOrphan(t *testing.T) {
	var deleted []int
	cl := testCleaner(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{
				{ID: 1, Name: "omni_truenas_live_one", Description: "Managed by Omni infra provider (request-id: live-one)"},
				{ID: 2, Name: "omni_truenas_dead_one", Description: "Managed by Omni infra provider (request-id: dead-one)"},
			}, nil
		case "vm.stop":
			return nil, nil
		case "vm.delete":
			var p []int
			_ = json.Unmarshal(params, &p)
			if len(p) > 0 {
				deleted = append(deleted, p[0])
			}
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	// Both VMs have backing zvols (partial-orphan heuristic alone would
	// skip them). But Omni only knows about live-one, so dead-one is the
	// double-orphan we want to catch.
	zvols := managedZvols("live-one", "dead-one")
	live := map[string]bool{"live-one": true}

	cl.cleanupOrphanVMs(context.Background(), zvols, live)

	assert.Equal(t, []int{2}, deleted, "should delete only the VM whose request-id is absent from the live MachineRequest set")
}

// TestCleanupOrphanZvols_LiveCrossRef_DoubleOrphan mirrors the VM test for
// the zvol path: a managed zvol whose VM is still present but whose
// MachineRequest is gone must be detected and deleted via the live
// cross-reference.
func TestCleanupOrphanZvols_LiveCrossRef_DoubleOrphan(t *testing.T) {
	var deleted []string
	cl := testCleaner(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			// Both VMs still alive — partial-orphan heuristic would
			// skip both zvols.
			return []client.VM{
				{ID: 1, Name: "omni_truenas_live_two"},
				{ID: 2, Name: "omni_truenas_dead_two"},
			}, nil
		case "pool.dataset.delete":
			var p []string
			_ = json.Unmarshal(params, &p)
			if len(p) > 0 {
				deleted = append(deleted, p[0])
			}
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	zvols := managedZvols("live-two", "dead-two")
	live := map[string]bool{"live-two": true}

	cl.cleanupOrphanZvols(context.Background(), zvols, live)

	require.Len(t, deleted, 1)
	assert.Contains(t, deleted[0], "dead-two", "should delete only the zvol whose request-id is absent from the live MachineRequest set")
}

// TestCleanupOrphanVMs_LiveNil_FallsBackToPartialHeuristic verifies that
// passing nil for liveRequests preserves legacy behavior (partial-orphan
// heuristic only). This is the safe fallback when the Omni read fails —
// must NOT mass-delete based on missing input.
func TestCleanupOrphanVMs_LiveNil_FallsBackToPartialHeuristic(t *testing.T) {
	var deleted []int
	cl := testCleaner(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{
				{ID: 1, Name: "omni_truenas_with_zvol", Description: "Managed by Omni infra provider (request-id: with-zvol)"},
				{ID: 2, Name: "omni_truenas_no_zvol", Description: "Managed by Omni infra provider (request-id: no-zvol)"},
			}, nil
		case "vm.stop":
			return nil, nil
		case "vm.delete":
			var p []int
			_ = json.Unmarshal(params, &p)
			if len(p) > 0 {
				deleted = append(deleted, p[0])
			}
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	// Only one zvol present. With liveRequests=nil, partial-orphan
	// heuristic deletes the VM whose zvol is missing AND keeps the VM
	// whose zvol exists. Mass deletion (if liveRequests=nil were treated
	// as "Omni knows about nothing") must not happen.
	cl.cleanupOrphanVMs(context.Background(), managedZvols("with-zvol"), nil)

	assert.Equal(t, []int{2}, deleted, "with nil live set, only the partial-orphan (no-zvol) is deleted; with-zvol is preserved")
}

// --- runOnce integration tests ---
// These test the full cleanup cycle including the shared ListManagedZvols query.

func TestRunOnce_CallsAllCleanupFunctions(t *testing.T) {
	var listFilesCalled, vmQueryCalled atomic.Bool

	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "filesystem.listdir":
			listFilesCalled.Store(true)
			return []client.FileEntry{}, nil
		case "vm.query":
			vmQueryCalled.Store(true)
			return []client.VM{}, nil
		case "pool.dataset.query":
			return managedDatasetResponse(), nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.runOnce(context.Background())

	assert.True(t, listFilesCalled.Load(), "should call ListFiles for ISO cleanup")
	assert.True(t, vmQueryCalled.Load(), "should call vm.query for orphan cleanup")
}

func TestRunOnce_ManagedZvolQueryFails_SkipsBothOrphanCleanups(t *testing.T) {
	var vmDeleteCalled, zvolDeleteCalled bool

	cl := testCleaner(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "filesystem.listdir":
			return []client.FileEntry{}, nil
		case "pool.dataset.query":
			return nil, &client.APIError{Code: 99, Message: "query failed"}
		case "vm.delete":
			vmDeleteCalled = true
			return nil, nil
		case "pool.dataset.delete":
			zvolDeleteCalled = true
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.runOnce(context.Background())

	assert.False(t, vmDeleteCalled, "must not delete VMs when managed zvol query fails")
	assert.False(t, zvolDeleteCalled, "must not delete zvols when managed zvol query fails")
}

func TestRunOnce_MixedScenario_CorrectCleanup(t *testing.T) {
	// Realistic mixed scenario:
	// - omni_active_cp: has matching zvol → keep
	// - omni_active_worker: has matching zvol → keep
	// - omni_orphan_vm: NO matching zvol (deprovision deleted zvol but not VM) → delete VM
	// - plex_server: not omni_ → ignore
	// - zvol for crashed-worker: NO matching VM (deprovision deleted VM but not zvol) → delete zvol
	// - zvol at deep path (default/previewk8/omni-vms/deep-active): has VM → keep

	var deletedVMs []int
	var deletedZvols []string

	cl := testCleaner(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "filesystem.listdir":
			return []client.FileEntry{}, nil
		case "pool.dataset.query":
			// Single query returns all managed zvols
			return []map[string]any{
				{
					"id": "default/omni-vms/active-cp",
					"user_properties": map[string]any{
						"org.omni:managed":    map[string]any{"value": "true"},
						"org.omni:request-id": map[string]any{"value": "active-cp"},
					},
				},
				{
					"id": "default/omni-vms/active-worker",
					"user_properties": map[string]any{
						"org.omni:managed":    map[string]any{"value": "true"},
						"org.omni:request-id": map[string]any{"value": "active-worker"},
					},
				},
				{
					"id": "default/omni-vms/crashed-worker",
					"user_properties": map[string]any{
						"org.omni:managed":    map[string]any{"value": "true"},
						"org.omni:request-id": map[string]any{"value": "crashed-worker"},
					},
				},
				{
					"id": "default/previewk8/omni-vms/deep-active",
					"user_properties": map[string]any{
						"org.omni:managed":    map[string]any{"value": "true"},
						"org.omni:request-id": map[string]any{"value": "deep-active"},
					},
				},
			}, nil
		case "vm.query":
			return []client.VM{
				// Descriptions follow omniVMDescription format; cleanup reads
				// request-id from description (not from name) post-v0.15.3.
				{ID: 1, Name: "omni_active_cp", Description: "Managed by Omni infra provider (request-id: active-cp)"},
				{ID: 2, Name: "omni_active_worker", Description: "Managed by Omni infra provider (request-id: active-worker)"},
				{ID: 3, Name: "omni_orphan_vm", Description: "Managed by Omni infra provider (request-id: orphan-vm)"}, // No zvol → orphan
				{ID: 4, Name: "plex_server"}, // Not omni_ → ignore
				{ID: 5, Name: "omni_deep_active", Description: "Managed by Omni infra provider (request-id: deep-active)"}, // Has zvol → keep
			}, nil
		case "vm.stop":
			return nil, nil
		case "vm.delete":
			var args []json.RawMessage
			_ = json.Unmarshal(params, &args)
			var id int
			_ = json.Unmarshal(args[0], &id)
			deletedVMs = append(deletedVMs, id)
			return nil, nil
		case "pool.dataset.delete":
			var args []json.RawMessage
			_ = json.Unmarshal(params, &args)
			var path string
			_ = json.Unmarshal(args[0], &path)
			deletedZvols = append(deletedZvols, path)
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.runOnce(context.Background())

	// Only the orphan VM should be deleted (ID 3)
	assert.Equal(t, []int{3}, deletedVMs, "should only delete orphan VM whose zvol is gone")

	// Only the crashed-worker zvol should be deleted (no matching VM)
	assert.Equal(t, []string{"default/omni-vms/crashed-worker"}, deletedZvols,
		"should only delete zvol whose VM is gone")
}

// TestMaybeDeleteOrphanVM_LegacyVMNoRequestID_Skipped pins the v0.15.3
// fix that protected against the mass-delete incident: VMs whose
// description doesn't carry the `(request-id: ...)` suffix must be
// skipped from this cleanup path entirely. A regression here previously
// killed 7+ freshly-created cluster members minutes after provision
// finished (CHANGELOG v0.15.3) because Name-based request-id derivation
// silently miscomputed the id on v0.15+ namespaced VMs.
func TestMaybeDeleteOrphanVM_LegacyVMNoRequestID_Skipped(t *testing.T) {
	var deleted []int
	cl := testCleaner(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			// Two omni-named VMs without a parseable request-id in the
			// description: one bare prefix (legacy v0.14 shape), one
			// look-alike with operator-typed notes.
			return []client.VM{
				{ID: 10, Name: "omni_truenas_legacy_one", Description: "Managed by Omni infra provider"},
				{ID: 11, Name: "omni_truenas_lookalike", Description: "some manual notes"},
			}, nil
		case "vm.stop":
			return nil, nil
		case "vm.delete":
			var args []json.RawMessage
			_ = json.Unmarshal(params, &args)
			var id int
			_ = json.Unmarshal(args[0], &id)
			deleted = append(deleted, id)
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	cl.cleanupOrphanVMs(context.Background(), managedZvols(), nil)

	assert.Empty(t, deleted,
		"VMs without parseable request-id MUST NOT be deleted — that was the v0.15.3 mass-delete incident root cause")
}

// TestMaybeDeleteOrphanVM_RequestIDNotInLiveSet_Deleted is the
// counter-positive: a properly-tagged VM whose request-id is NOT in the
// live MachineRequest set must be deleted as an orphan. Without this,
// the "v0.15.3 fix" reading too broadly would skip legitimate orphans.
func TestMaybeDeleteOrphanVM_RequestIDNotInLiveSet_Deleted(t *testing.T) {
	var deleted []int
	cl := testCleaner(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "vm.query":
			return []client.VM{
				{ID: 42, Name: "omni_truenas_ghost", Description: "Managed by Omni infra provider (request-id: ghost-machine)"},
			}, nil
		case "vm.stop":
			return nil, nil
		case "vm.delete":
			var args []json.RawMessage
			_ = json.Unmarshal(params, &args)
			var id int
			_ = json.Unmarshal(args[0], &id)
			deleted = append(deleted, id)
			return nil, nil
		}
		return nil, nil
	}, map[string]bool{})

	// liveRequests does NOT contain "ghost-machine" — Omni has forgotten
	// this MachineRequest, so the VM is a confirmed orphan.
	cl.cleanupOrphanVMs(context.Background(), managedZvols("ghost-machine"), map[string]bool{})

	assert.Equal(t, []int{42}, deleted,
		"VM whose request-id is absent from the live MachineRequest set must be deleted")
}
