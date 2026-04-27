package client

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testVMName = "omni-test-vm"

func TestCreateVM_Success(t *testing.T) {
	c := newMockClient(t, func(method string, _ json.RawMessage) (any, *jsonRPCError) {
		assert.Equal(t, "vm.create", method)

		return VM{ID: 42, Name: testVMName, VCPUs: 2, Memory: 4096}, nil
	})

	vm, err := c.CreateVM(context.Background(), CreateVMRequest{
		Name:       testVMName,
		VCPUs:      2,
		Memory:     4096,
		Bootloader: "UEFI",
	})

	require.NoError(t, err)
	assert.Equal(t, 42, vm.ID)
	assert.Equal(t, testVMName, vm.Name)
}

// TestCreateVM_MinMemory_OmitemptyZero pins the wire-format contract for
// the hard-reservation case. TrueNAS rejects a literal `0` for min_memory;
// the field MUST be absent (not present-as-zero). Without this test, a
// struct-tag typo (`min-memory`, `minMemory`) compiles, the existing
// CreateVM_Success test still passes, and every provisioning request
// silently loses the balloon-config knob.
func TestCreateVM_MinMemory_OmitemptyZero(t *testing.T) {
	c := newMockClient(t, func(method string, params json.RawMessage) (any, *jsonRPCError) {
		assert.Equal(t, "vm.create", method)

		// Decode the actual JSON-RPC params and assert min_memory is
		// absent from the keys. Using json.RawMessage round-trip so
		// we're testing what's on the wire, not what's in the Go struct.
		var got map[string]any
		require.NoError(t, json.Unmarshal(params, &got))
		_, present := got["min_memory"]
		assert.False(t, present, "min_memory must be omitted (not present-as-zero) when MinMemory=0")

		return VM{ID: 42}, nil
	})

	_, err := c.CreateVM(context.Background(), CreateVMRequest{
		Name:       testVMName,
		VCPUs:      2,
		Memory:     4096,
		MinMemory:  0,
		Bootloader: "UEFI",
	})
	require.NoError(t, err)
}

// TestCreateVM_MinMemory_SerializesWhenSet pins the balloon-config wire
// format. When MinMemory is non-zero, it must serialize as an integer
// under the `min_memory` JSON key (not `minMemory`, `min-memory`, etc.).
func TestCreateVM_MinMemory_SerializesWhenSet(t *testing.T) {
	c := newMockClient(t, func(_ string, params json.RawMessage) (any, *jsonRPCError) {
		var got map[string]any
		require.NoError(t, json.Unmarshal(params, &got))

		raw, present := got["min_memory"]
		assert.True(t, present, "min_memory key must be present when MinMemory>0")
		assert.EqualValues(t, 2048, raw, "min_memory must be the integer 2048 (not stringified)")

		return VM{ID: 42}, nil
	})

	_, err := c.CreateVM(context.Background(), CreateVMRequest{
		Name:       testVMName,
		VCPUs:      2,
		Memory:     4096,
		MinMemory:  2048,
		Bootloader: "UEFI",
	})
	require.NoError(t, err)
}

func TestGetVM_Success(t *testing.T) {
	c := newMockClient(t, func(method string, _ json.RawMessage) (any, *jsonRPCError) {
		assert.Equal(t, methodVMQuery, method)

		return VM{ID: 42, Name: "omni-test", Status: VMStatus{State: "RUNNING"}}, nil
	})

	vm, err := c.GetVM(context.Background(), 42)
	require.NoError(t, err)
	assert.Equal(t, "RUNNING", vm.Status.State)
}

func TestStartVM_Success(t *testing.T) {
	c := newMockClient(t, func(method string, _ json.RawMessage) (any, *jsonRPCError) {
		assert.Equal(t, "vm.start", method)

		return true, nil
	})

	err := c.StartVM(context.Background(), 42)
	require.NoError(t, err)
}

// TestRunningGuestsMemoryMiB sums RUNNING guests only. STOPPED guests don't
// hold host memory until they boot, so including them in the pre-flight
// would refuse provisioning on hosts that are nominally over-committed but
// fine in practice (the dominant homelab pattern: one or two big VMs
// usually-stopped, plus a fleet of small always-on ones).
func TestRunningGuestsMemoryMiB_OnlyCountsRunning(t *testing.T) {
	c := newMockClient(t, func(method string, _ json.RawMessage) (any, *jsonRPCError) {
		assert.Equal(t, methodVMQuery, method)

		return []VM{
			{ID: 1, Name: "running-a", Memory: 4096, Status: VMStatus{State: "RUNNING"}},
			{ID: 2, Name: "stopped-b", Memory: 8192, Status: VMStatus{State: "STOPPED"}},
			{ID: 3, Name: "running-c", Memory: 2048, Status: VMStatus{State: "RUNNING"}},
		}, nil
	})

	total, err := c.RunningGuestsMemoryMiB(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(6144), total, "should sum 4096 + 2048 (RUNNING only)")
}

func TestRunningGuestsMemoryMiB_EmptyHost(t *testing.T) {
	c := newMockClient(t, func(_ string, _ json.RawMessage) (any, *jsonRPCError) {
		return []VM{}, nil
	})

	total, err := c.RunningGuestsMemoryMiB(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
}

// TestRunningGuestsMemoryMiB_QueryError pins the contract that pre-flight
// callers rely on: a vm.query failure propagates the error so the caller
// can degrade explicitly (single-VM ceiling fallback). Without this test,
// a future "swallow error and return 0" refactor would silently disable
// the free-RAM safety net and the pre-flight would happily admit VMs into
// a full host.
func TestRunningGuestsMemoryMiB_QueryError(t *testing.T) {
	c := newMockClient(t, func(_ string, _ json.RawMessage) (any, *jsonRPCError) {
		return nil, &jsonRPCError{Code: 99, Message: "transport hiccup"}
	})

	total, err := c.RunningGuestsMemoryMiB(context.Background())
	require.Error(t, err)
	assert.Equal(t, int64(0), total)
}

// TestRunningGuestsMemoryMiB_TransitionalStates pins behavior for
// non-RUNNING states. STARTING / SUSPENDED / ERROR / "" must NOT count
// toward the host-RAM commitment because TrueNAS only locks guest pages
// once the guest is fully RUNNING. Counting them would cause spurious
// free-RAM rejections during transitional storms (e.g., autoscaler
// bursting many VMs simultaneously, all in STARTING for ~30s).
func TestRunningGuestsMemoryMiB_TransitionalStates(t *testing.T) {
	c := newMockClient(t, func(_ string, _ json.RawMessage) (any, *jsonRPCError) {
		return []VM{
			{ID: 1, Memory: 4096, Status: VMStatus{State: "RUNNING"}},
			{ID: 2, Memory: 8192, Status: VMStatus{State: "STARTING"}},
			{ID: 3, Memory: 2048, Status: VMStatus{State: "SUSPENDED"}},
			{ID: 4, Memory: 1024, Status: VMStatus{State: "ERROR"}},
			{ID: 5, Memory: 512, Status: VMStatus{State: ""}},
		}, nil
	})

	total, err := c.RunningGuestsMemoryMiB(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(4096), total, "only RUNNING (4096) counts; STARTING/SUSPENDED/ERROR/empty must be excluded")
}

// TestIsNoMemory pins both detection paths: TrueNAS error code 12 (the
// well-formed case) and the message-based fallback (observed in the wild
// on TrueNAS 25.04, where libvirt errors arrive with a different code but
// the [ENOMEM] string intact).
func TestIsNoMemory(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"code 12 ENOMEM", &APIError{Code: ErrCodeNoMemory, Message: "kvm out of memory"}, true},
		{"message [ENOMEM]", &APIError{Code: 1, Message: "[ENOMEM] Cannot guarantee memory for guest foo"}, true},
		{"message Cannot guarantee", &APIError{Code: 1, Message: "Cannot guarantee memory for guest foo"}, true},
		{"code 28 ENOSPC is not ENOMEM", &APIError{Code: ErrCodeNoSpace, Message: "no space"}, false},
		{"non-API error", assert.AnError, false},
		{"nil", nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsNoMemory(tc.err))
		})
	}
}

func TestStopVM_Force(t *testing.T) {
	c := newMockClient(t, func(method string, params json.RawMessage) (any, *jsonRPCError) {
		assert.Equal(t, "vm.stop", method)

		// params should be [42, {"force": true}]
		assert.Contains(t, string(params), `"force":true`)

		return true, nil
	})

	err := c.StopVM(context.Background(), 42, true)
	require.NoError(t, err)
}

func TestDeleteVM_Success(t *testing.T) {
	c := newMockClient(t, func(method string, params json.RawMessage) (any, *jsonRPCError) {
		assert.Equal(t, "vm.delete", method)

		// Pin the exact param shape TrueNAS 25.10 accepts. A prior version
		// shipped {"force":true, "force_after_timeout":true} and was rejected
		// with EINVAL "Extra inputs are not permitted", stopping every VM it
		// tried to delete without ever completing the delete.
		var payload []json.RawMessage
		require.NoError(t, json.Unmarshal(params, &payload))
		require.Len(t, payload, 2, "vm.delete expects [id, opts]")
		assert.JSONEq(t, `{"force":true}`, string(payload[1]))

		return true, nil
	})

	err := c.DeleteVM(context.Background(), 42)
	require.NoError(t, err)
}

func TestDeleteVM_NotFound(t *testing.T) {
	c := newMockClient(t, func(_ string, _ json.RawMessage) (any, *jsonRPCError) {
		return nil, notFoundErr()
	})

	err := c.DeleteVM(context.Background(), 42)
	require.NoError(t, err) // not found is not an error on delete
}

func TestFindVMByName_Found(t *testing.T) {
	c := newMockClient(t, func(method string, _ json.RawMessage) (any, *jsonRPCError) {
		assert.Equal(t, methodVMQuery, method)

		return []VM{{ID: 2, Name: "omni-target"}}, nil
	})

	vm, err := c.FindVMByName(context.Background(), "omni-target")
	require.NoError(t, err)
	require.NotNil(t, vm)
	assert.Equal(t, 2, vm.ID)
}

func TestFindVMByName_NotFound(t *testing.T) {
	c := newMockClient(t, func(_ string, _ json.RawMessage) (any, *jsonRPCError) {
		return []VM{}, nil
	})

	vm, err := c.FindVMByName(context.Background(), "omni-missing")
	require.NoError(t, err)
	assert.Nil(t, vm)
}

func TestListVMs(t *testing.T) {
	c := newMockClient(t, func(method string, _ json.RawMessage) (any, *jsonRPCError) {
		assert.Equal(t, methodVMQuery, method)

		return []VM{{ID: 1, Name: "vm-1"}, {ID: 2, Name: "vm-2"}}, nil
	})

	vms, err := c.ListVMs(context.Background())
	require.NoError(t, err)
	assert.Len(t, vms, 2)
}
