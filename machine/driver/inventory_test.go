package driver //nolint:testpackage // GPU selection and the existing UUID matcher are private behavior boundaries.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	prism "github.com/nutanix-cloud-native/prism-go-client/v3"
)

const (
	gpuFixtureName           = "A"
	gpuFixtureDeviceA  int64 = 11
	gpuFixtureDeviceB  int64 = 22
	uuidAllocationRuns       = 1000
)

func TestGPUInventoryBudget(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name             string
		names            []string
		ids              []int64
		calls            int
		errorText        string
		direct           bool
		cancelAfterFetch bool
	}{
		{name: "empty"},
		{name: "first empty", names: []string{""}, errorText: "gpu name"},
		{name: "later empty", names: []string{gpuFixtureName, ""}, calls: 1, errorText: "gpu name"},
		{
			name: "two names", names: []string{gpuFixtureName, "B"},
			ids: []int64{gpuFixtureDeviceA, gpuFixtureDeviceB}, calls: 1,
		},
		{
			name: "duplicate selection retained", names: []string{gpuFixtureName, gpuFixtureName},
			ids: []int64{gpuFixtureDeviceA, gpuFixtureDeviceA}, calls: 1,
		},
		{name: "prior failure", names: []string{"absent", ""}, calls: 1, errorText: "matches required GPU name"},
		{
			name: "cancellation between names", names: []string{gpuFixtureName, "B"}, calls: 1,
			errorText: "context canceled", cancelAfterFetch: true,
		},
		{name: "case sensitive", names: []string{"a"}, calls: 1, errorText: "matches required GPU name"},
		{name: "busy ignored", names: []string{"busy"}, calls: 1, errorText: "matches required GPU name"},
		{name: "missing device ID list", names: []string{"broken"}, calls: 1, errorText: "device ID"},
		{name: "missing device ID single", names: []string{"broken"}, calls: 1, errorText: "device ID", direct: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			calls := 0
			body := gpuInventoryFixture(t)
			conn := lifecycleClient(t, func(request *http.Request) (*http.Response, error) {
				calls++

				if testCase.cancelAfterFetch {
					cancel()
				}

				if request.URL.Path != "/api/nutanix/v3/hosts/list" {
					t.Errorf("unexpected inventory path: %s", request.URL.Path)
				}

				return lifecycleResponse(request, http.StatusOK, body), nil
			})

			var (
				result []*prism.VMGpu
				err    error
			)
			if testCase.direct {
				_, err = GetGPU(ctx, conn, "pe", testCase.names[0])
			} else {
				result, err = GetGPUList(ctx, conn, testCase.names, "pe")
			}

			if calls != testCase.calls {
				t.Errorf("host inventory requests=%d, want %d", calls, testCase.calls)
			}

			if testCase.cancelAfterFetch && !errors.Is(err, context.Canceled) {
				t.Errorf("cancellation cause lost: %v", err)
			}

			assertGPUResult(t, result, err, testCase.ids, testCase.errorText)
		})
	}
}

func assertGPUResult(t *testing.T, result []*prism.VMGpu, err error, want []int64, errorText string) {
	t.Helper()

	if errorText != "" {
		if err == nil || !strings.Contains(err.Error(), errorText) {
			t.Errorf("selection error=%v, want %q", err, errorText)
		}

		return
	}

	if err != nil {
		t.Fatal(err)
	}

	ids := make([]int64, 0, len(result))
	for _, gpu := range result {
		if gpu.DeviceID == nil || gpu.Mode == nil || *gpu.Mode != "PASSTHROUGH" ||
			gpu.Vendor == nil || *gpu.Vendor != "NVIDIA" {
			t.Fatalf("GPU selection lost fields: %+v", gpu)
		}

		ids = append(ids, *gpu.DeviceID)
	}

	if !slices.Equal(ids, want) {
		t.Errorf("device IDs=%v, want %v", ids, want)
	}

	if result == nil {
		t.Error("empty GPU list changed from an allocated slice to nil")
	}
}

func gpuInventoryFixture(t *testing.T) string {
	t.Helper()

	gpus := []*prism.GPU{
		nil,
		{Name: gpuFixtureName, Status: "IN_USE"},
		{Name: gpuFixtureName, Status: "UNUSED", DeviceID: new(gpuFixtureDeviceA), Mode: "PASSTHROUGH", Vendor: "NVIDIA"},
		{Name: "B", Status: "UNUSED", DeviceID: new(gpuFixtureDeviceB), Mode: "PASSTHROUGH", Vendor: "NVIDIA"},
		{Name: "broken", Status: "UNUSED"},
		{Name: "busy", Status: "IN_USE"},
	}
	inventory := prism.HostListResponse{Entities: []*prism.HostResponse{
		nil,
		{Status: &prism.HostStatus{
			ClusterReference: &prism.ReferenceValues{UUID: "another-pe"},
			Resources: &prism.HostResources{GPUList: []*prism.GPU{{
				Name: gpuFixtureName, Status: "UNUSED", DeviceID: new(gpuFixtureDeviceB),
				Mode: "PASSTHROUGH", Vendor: "NVIDIA",
			}}},
		}},
		{Status: &prism.HostStatus{
			ClusterReference: &prism.ReferenceValues{UUID: "pe"},
			Resources:        &prism.HostResources{GPUList: gpus},
		}},
	}}

	data, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestUUIDMatcherSemantics(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		value string
		valid bool
	}{
		{value: "01234567-89ab-cdef-0123-456789abcdef", valid: true},
		{value: "01234567-89AB-CDEF-0123-456789ABCDEF", valid: true},
		{value: "00000000-0000-0000-0000-000000000000", valid: true},
		{value: "01234567-89aB-cDeF-0123-456789aBcDeF", valid: true},
		{value: ""},
		{value: "{01234567-89ab-cdef-0123-456789abcdef}"},
		{value: "urn:uuid:01234567-89ab-cdef-0123-456789abcdef"},
		{value: "01234567-89ab-cdef-0123-456789abcdeg"},
		{value: "01234567-89ab-cdef-0123-456789abcdef\n"},
		{value: "\n01234567-89ab-cdef-0123-456789abcdef"},
		{value: "0123456789abcdef0123456789abcdef"},
	} {
		t.Run(testCase.value, func(t *testing.T) {
			t.Parallel()

			if actual := isUUID(testCase.value); actual != testCase.valid {
				t.Errorf("isUUID(%q)=%t, want %t", testCase.value, actual, testCase.valid)
			}
		})
	}
}

//nolint:paralleltest // AllocsPerRun temporarily changes GOMAXPROCS for its measurement.
func TestUUIDMatcherAllocations(t *testing.T) {
	for _, value := range []string{"01234567-89ab-cdef-0123-456789abcdef", "not-a-uuid"} {
		matched := false
		allocations := testing.AllocsPerRun(uuidAllocationRuns, func() { matched = isUUID(value) })
		t.Logf("value=%q allocations/run=%.0f matched=%t", value, allocations, matched)

		if allocations != 0 {
			t.Errorf("UUID matcher allocates %.0f times per call, want 0", allocations)
		}
	}
}
