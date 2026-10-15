package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatefulNetworkScenarios(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"management-ip", "nil-management-ip"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			f := &fixture{Scenario: scenario}
			response := httptest.NewRecorder()
			path := "/api/nutanix/v3/vms/" + fixtureVMUUID
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
			f.serve(response, request)

			if response.Code != http.StatusOK || !json.Valid(response.Body.Bytes()) {
				t.Fatalf("scenario %s: status=%d response=%s", scenario, response.Code, response.Body.String())
			}
		})
	}
}
