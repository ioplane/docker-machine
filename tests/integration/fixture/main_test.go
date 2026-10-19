package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const omittedScenario = "omitted"

func TestStatefulNetworkScenarios(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"management-ip", "nil-management-ip"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			fixtureState := &fixture{Scenario: scenario, Payload: json.RawMessage(
				`{"spec":{"resources":{"nic_list":[{"subnet_reference":{"uuid":"33333333-3333-3333-3333-333333333333"}}]}}}`,
			)}
			response := httptest.NewRecorder()
			path := "/api/nutanix/v3/vms/" + fixtureVMUUID
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
			fixtureState.serve(response, request)

			if response.Code != http.StatusOK || !json.Valid(response.Body.Bytes()) {
				t.Fatalf("scenario %s: status=%d response=%s", scenario, response.Code, response.Body.String())
			}
		})
	}
}

func TestRequestDerivedNICReadback(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"management-ip", "nil-management-ip", "crash", omittedScenario} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			fixtureState := &fixture{Scenario: scenario}
			if scenario == omittedScenario {
				fixtureState.Scenario = "recovery"
			}

			identities := []string{
				fixtureOverlayUUID, "11111111-1111-1111-1111-111111111111", fixtureVLANUUID,
			}
			if scenario == omittedScenario {
				identities = identities[:1]
			}

			nics := make([]map[string]any, 0, len(identities))
			for _, id := range identities {
				nic := map[string]any{"subnet_reference": map[string]string{"uuid": id}}
				if scenario != omittedScenario {
					nic["num_queues"] = 2
				}

				nics = append(nics, nic)
			}

			payload, err := json.Marshal(map[string]any{"spec": map[string]any{"resources": map[string]any{"nic_list": nics}}})
			if err != nil {
				t.Fatal(err)
			}

			fixtureCall(t, fixtureState, http.MethodPost, "/api/nutanix/v3/idempotence_identifiers",
				[]byte(`{"client_identifier":"fixture-client"}`))
			fixtureCall(t, fixtureState, http.MethodPost, "/api/nutanix/v3/vms", payload)

			for read := range 2 {
				if scenario == "crash" && read == 1 {
					fixtureCall(t, fixtureState, http.MethodPost, "/resume", []byte(`{}`))
				}

				response := fixtureCall(t, fixtureState, http.MethodGet, "/api/nutanix/v3/vms/"+fixtureVMUUID, nil)
				assertReadbackNICs(t, response, identities, scenario, read)
			}

			if fixtureState.Creates != 1 || fixtureState.Allocations != 1 ||
				fixtureState.Deletes != 0 || fixtureState.VMReads != 2 {
				t.Fatalf("mutations/read budget: creates=%d allocations=%d deletes=%d reads=%d",
					fixtureState.Creates, fixtureState.Allocations, fixtureState.Deletes, fixtureState.VMReads)
			}
		})
	}
}

func fixtureCall(t *testing.T, fixtureState *fixture, method, path string, body []byte) []byte {
	t.Helper()

	response := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(body))
	fixtureState.serve(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("%s %s: status=%d", method, path, response.Code)
	}

	return response.Body.Bytes()
}

func assertReadbackNICs(t *testing.T, body []byte, identities []string, scenario string, read int) {
	t.Helper()

	var result struct {
		Status map[string]json.RawMessage `json:"status"`
	}

	err := json.Unmarshal(body, &result)
	if err != nil {
		t.Fatal(err)
	}

	var resources struct {
		NICs []map[string]json.RawMessage `json:"nic_list"` //nolint:tagliatelle // Prism v3 fixture schema.
	}

	err = json.Unmarshal(result.Status["resources"], &resources)
	if err != nil {
		t.Fatal(err)
	}

	nics := resources.NICs
	if len(nics) != len(identities) {
		t.Fatalf("readback NIC count=%d want=%d", len(nics), len(identities))
	}

	for index, nic := range nics {
		var reference struct {
			UUID string `json:"uuid"`
		}

		err = json.Unmarshal(nic["subnet_reference"], &reference)
		if err != nil {
			t.Fatal(err)
		}

		if reference.UUID != identities[len(identities)-index-1] {
			t.Fatalf("NIC %d identity=%s", index, reference.UUID)
		}

		q, present := nic["num_queues"]
		if scenario == omittedScenario && present || scenario != omittedScenario && string(q) != "2" {
			t.Fatalf("queues fabricated or lost: present=%v value=%s", present, q)
		}

		wantIP := reference.UUID == identities[0] && scenario != "nil-management-ip" &&
			(read == 1 || scenario == omittedScenario)

		hasIP := bytes.Contains(nic["ip_endpoint_list"], []byte(`10.0.0.20`))
		if wantIP != hasIP {
			t.Fatalf("management IP gate: scenario=%s read=%d identity=%s endpoints=%s",
				scenario, read, reference.UUID, nic["ip_endpoint_list"])
		}
	}
}
