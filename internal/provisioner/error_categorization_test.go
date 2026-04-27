package provisioner

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cosi-project/runtime/pkg/controller"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/bearbinary/omni-infra-provider-truenas/internal/client"
)

func TestRecordProvisionError_Categories(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		err     error
		wantCat string
	}{
		{"pool not found", fmt.Errorf(`pool "tank" not found`), "pool_not_found"},
		{"pool full ENOSPC", fmt.Errorf("ENOSPC: pool is full"), "pool_full"},
		{"nic invalid nic_attach", fmt.Errorf("nic_attach: br999 not found"), "nic_invalid"},
		{"nic invalid NIC", fmt.Errorf("invalid NIC configuration"), "nic_invalid"},
		{"network_interface invalid", fmt.Errorf("network_interface contains unsafe characters"), "nic_invalid"},
		{"connection reconnect", fmt.Errorf("reconnect failed after 3 attempts"), "connection"},
		{"connection unreachable", fmt.Errorf("TrueNAS is unreachable"), "connection"},
		{"auth permission", fmt.Errorf("permission denied"), "auth"},
		{"auth EACCES", fmt.Errorf("EACCES: access denied"), "auth"},
		{"timeout", fmt.Errorf("context deadline exceeded: timeout"), "timeout"},
		{"memory oversized class", fmt.Errorf("MachineClass exceeds host RAM: host has 8192 MiB total but VM ceiling is 32768 MiB"), "memory"},
		{"memory RAM", fmt.Errorf("not enough RAM"), "memory"},
		// `host_oom` cases — runtime ENOMEM from vm.start (TrueNAS host RAM
		// is full). Categorizer uses errors.Is(err, ErrHostOOM) and
		// client.IsNoMemory(err) as primary classifiers; raw libvirt
		// strings fall through to a substring backstop.
		{"host_oom raw libvirt", fmt.Errorf("truenas api error (code 12): [ENOMEM] Cannot guarantee memory for guest omni_test"), "host_oom"},
		{"host_oom typed APIError", &client.APIError{Code: client.ErrCodeNoMemory, Message: "kvm enomem"}, "host_oom"},
		{"host_oom wrapped APIError", fmt.Errorf("vm.start failed: %w", &client.APIError{Code: client.ErrCodeNoMemory, Message: "kvm enomem"}), "host_oom"},
		{"host_oom translated (sentinel-wrapped)", fmt.Errorf("TrueNAS host out of memory: cannot start VM 700 (omni_test) requesting 4096 MiB: %w", ErrHostOOM), "host_oom"},
		{"host_oom permanent (sentinel-wrapped)", fmt.Errorf("TrueNAS host out of memory after 5 attempts: cannot start VM 700: %w", ErrHostOOM), "host_oom"},
		{"host_oom preflight (sentinel-wrapped)", fmt.Errorf("TrueNAS host has 1024 MiB free (8192 total minus 7168 MiB committed to RUNNING guests): %w", ErrHostOOM), "host_oom"},
		// Negative case (QA F7): a future preflight error reusing the
		// "TrueNAS host has" prefix without ErrHostOOM wrapping must NOT
		// be misrouted to host_oom. Pins the new contract: substring is
		// no longer the primary classifier. (Picked a phrase that
		// doesn't accidentally match other arms — "NIC", "memory",
		// etc. — so the test is unambiguously about ErrHostOOM
		// wrapping being load-bearing.)
		{"not host_oom — TrueNAS-host-has prefix without sentinel", fmt.Errorf("TrueNAS host has unexpected disk count"), "unknown"},
		{"image schematic", fmt.Errorf("failed to generate schematic"), "image"},
		{"image ISO", fmt.Errorf("failed to download ISO"), "image"},
		{"unknown error", fmt.Errorf("something completely different"), "unknown"},
		// `config_invalid` — MachineClass validation failures wrapped via
		// "invalid MachineClass config: %w" must NOT route to nic_invalid even
		// when the inner message mentions additional_nics. Regression guard
		// against "operator typo pages the same alert as hypervisor regression".
		{"config_invalid disk size", fmt.Errorf(`invalid MachineClass config: disk_size must be >= 20 GiB`), "config_invalid"},
		{"config_invalid too many NICs", fmt.Errorf(`invalid MachineClass config: additional_nics: at most 16 NICs supported (got 17)`), "config_invalid"},
		{"config_invalid duplicate NIC", fmt.Errorf(`invalid MachineClass config: additional_nics[1]: duplicate network_interface "br200" — each NIC must use a different interface`), "config_invalid"},
		// `config_patch` — CreateConfigPatch failures across all five patch
		// kinds. Without this, they fall to "unknown" and on-call can't
		// attribute which patch broke.
		{"config_patch build nic-interfaces", fmt.Errorf("failed to build additional-NIC interfaces config patch: invalid MAC"), "config_patch"},
		{"config_patch apply nic-interfaces", fmt.Errorf("failed to apply additional-NIC interfaces config patch: resource conflict"), "config_patch"},
		{"config_patch apply data-volumes", fmt.Errorf("failed to apply data-volumes config patch: connection refused"), "config_patch"},
		{"config_patch apply longhorn-ops", fmt.Errorf("failed to apply longhorn-ops config patch: timeout"), "config_patch"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cat := categorizeError(tc.err)
			assert.Equal(t, tc.wantCat, cat)
		})
	}
}

func TestRecordProvisionError_NilError(t *testing.T) {
	t.Parallel()
	// Should not panic
	recordProvisionError(context.Background(), nil, nil)
}

// TestRecordProvisionError_RequeueUnwrap verifies that RequeueError is handled
// correctly. Without unwrapping, every step-wait signal would land as an
// Error-level log line with error_category=unknown and bump the
// truenas_provision_errors_total counter — a regression introduced in the
// initial v0.15.0 recordProvisionError change and fixed in v0.15.1.
func TestRecordProvisionError_RequeueUnwrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantLogs int    // number of error-level log lines expected
		wantMsg  string // substring that must appear in the one log line, when wantLogs==1
	}{
		{
			name:     "pure requeue with nil inner",
			err:      controller.NewRequeueError(nil, 15*time.Second),
			wantLogs: 0,
		},
		{
			name:     "requeue wrapping a real error",
			err:      controller.NewRequeueError(errors.New("pool \"tank\" not found"), 15*time.Second),
			wantLogs: 1,
			wantMsg:  `pool "tank" not found`,
		},
		{
			name:     "non-requeue error passes through",
			err:      errors.New("failed to delete VM 42"),
			wantLogs: 1,
			wantMsg:  "failed to delete VM 42",
		},
		{
			name:     "context.Canceled alone is treated as shutdown, not failure",
			err:      context.Canceled,
			wantLogs: 0,
		},
		{
			name:     "context.Canceled wrapped in RequeueError is also shutdown",
			err:      controller.NewRequeueError(context.Canceled, 15*time.Second),
			wantLogs: 0,
		},
		{
			name:     "wrapped context.Canceled with more context — still shutdown",
			err:      fmt.Errorf("cleanupVM: %w", context.Canceled),
			wantLogs: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			core, sink := observer.New(zap.ErrorLevel)
			logger := zap.New(core)

			recordProvisionError(context.Background(), logger, tc.err)

			entries := sink.FilterMessage("provision error").All()
			assert.Len(t, entries, tc.wantLogs)

			if tc.wantLogs == 1 {
				assert.Contains(t, entries[0].ContextMap()["error"], tc.wantMsg)
			}
		})
	}
}
