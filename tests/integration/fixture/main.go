// Package main serves a disposable Prism v3 contract fixture, without external credentials.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"
)

type fixture struct {
	mu sync.Mutex

	Scenario        string          `json:"scenario"`
	Creates         int             `json:"creates"`
	Payload         json.RawMessage `json:"payload"`
	VMReads         int             `json:"vm_reads"` //nolint:tagliatelle // Fixture capture schema.
	ClientID        string          `json:"clientId"`
	Allocations     int             `json:"allocations"`
	Deletes         int             `json:"deletes"`
	RecoveryVMReads []string        `json:"recoveryVmReads"`
}

const (
	managementIPScenario = "management-ip"
	fixtureVMUUID        = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	crashScenario        = "crash"
	recoveryScenario     = "recovery"
	scenarioBodyLimit    = 1024
	payloadBodyLimit     = 1 << 20
)

type scenarioInput struct {
	Scenario string `json:"scenario"`
}

func main() {
	f := new(fixture)
	server := httptest.NewUnstartedServer(http.HandlerFunc(f.serve))

	var listenConfig net.ListenConfig

	// The disposable container must accept its published test port; no external credentials are served.
	listener, err := listenConfig.Listen(context.Background(), "tcp", ":8443")
	if err != nil {
		log.Fatal(err)
	}

	err = server.Listener.Close()
	if err != nil {
		log.Fatal(err)
	}

	server.Listener = listener
	server.StartTLS()

	defer server.Close()

	select {}
}

func (f *fixture) serve(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/health" {
		writer.WriteHeader(http.StatusOK)

		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	writer.Header().Set("Content-Type", "application/json")

	switch request.URL.Path {
	case "/scenario":
		f.setScenario(writer, request)

		return
	case "/capture":
		err := json.NewEncoder(writer).Encode(f)
		if err != nil {
			log.Print(err)
		}

		return
	case "/resume":
		f.Scenario = recoveryScenario

		return
	}

	if strings.HasPrefix(request.URL.Path, "/api/nutanix/v3/idempotence_identifiers") {
		f.allocate(writer, request)

		return
	}

	if request.URL.Path == "/api/nutanix/v3/vms" && request.Method == http.MethodPost {
		f.createVM(writer, request)

		return
	}

	if strings.HasPrefix(request.URL.Path, "/api/nutanix/v3/vms/") {
		if request.Method == http.MethodDelete {
			f.Deletes++

			http.Error(writer, "unexpected fixture VM deletion", http.StatusConflict)

			return
		}

		if f.Scenario == recoveryScenario && request.Method == http.MethodGet {
			f.RecoveryVMReads = append(f.RecoveryVMReads, strings.TrimPrefix(request.URL.Path, "/api/nutanix/v3/vms/"))
		}
	}

	if f.serveNetworkScenario(writer, request) || f.serveLifecycleScenario(writer, request) {
		return
	}

	serveLookup(writer, request)
}

func (f *fixture) setScenario(writer http.ResponseWriter, request *http.Request) {
	var input scenarioInput

	err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, scenarioBodyLimit)).Decode(&input)
	if err != nil {
		http.Error(writer, "invalid scenario", http.StatusBadRequest)

		return
	}

	f.Scenario, f.Creates, f.Payload, f.VMReads, f.Allocations, f.ClientID = input.Scenario, 0, nil, 0, 0, ""
	f.Deletes, f.RecoveryVMReads = 0, nil
}

func (f *fixture) createVM(writer http.ResponseWriter, request *http.Request) {
	f.Creates++

	err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, payloadBodyLimit)).Decode(&f.Payload)
	if err != nil {
		http.Error(writer, "invalid payload", http.StatusBadRequest)

		return
	}

	var response string

	switch f.Scenario {
	case managementIPScenario, "nil-management-ip", recoveryScenario:
		response = `{"metadata":{"uuid":"fixture-vm"},"status":{"execution_context":{"task_uuid":"task"}}}`
	case "429":
		writer.WriteHeader(http.StatusTooManyRequests)

		response = "{}"
	case "500":
		writer.WriteHeader(http.StatusInternalServerError)

		response = "{}"
	case "malformed", crashScenario:
		response = "{"
	case "missing-metadata":
		response = `{"status":{"execution_context":{"task_uuid":"task"}}}`
	default:
		response = `{"metadata":{"uuid":"fixture-vm"},"status":{"execution_context":{"task_uuid":null}}}`
	}

	writeResponse(writer, []byte(strings.ReplaceAll(response, "fixture-vm", fixtureVMUUID)))
}

func (f *fixture) allocate(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPost {
		var input map[string]json.RawMessage

		err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, scenarioBodyLimit)).Decode(&input)
		if err == nil {
			err = json.Unmarshal(input["client_identifier"], &f.ClientID)
		}

		if err != nil {
			http.Error(writer, "invalid allocator request", http.StatusBadRequest)

			return
		}

		f.Allocations++
	}

	err := json.NewEncoder(writer).Encode(map[string]any{
		"client_identifier": f.ClientID, "count": 1, "uuid_list": []string{fixtureVMUUID},
		"expiration_time": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		log.Print(err)
	}
}

func (f *fixture) serveNetworkScenario(writer http.ResponseWriter, request *http.Request) bool {
	if request.URL.Path == "/api/nutanix/v3/subnets/list" && strings.HasPrefix(f.Scenario, "duplicate-") {
		data, err := os.ReadFile("/fixtures/subnets.json")
		if err != nil {
			http.Error(writer, "fixture missing", http.StatusInternalServerError)

			return true
		}

		var inventory struct {
			Entities []json.RawMessage `json:"entities"`
			Metadata map[string]int64  `json:"metadata"`
		}

		err = json.Unmarshal(data, &inventory)
		if err != nil {
			http.Error(writer, "fixture invalid", http.StatusInternalServerError)

			return true
		}

		index := 0
		identity := "vlan-id"

		if f.Scenario == "duplicate-overlay" {
			index = 1
			identity = "overlay-id"
			inventory.Entities[index] = json.RawMessage(strings.ReplaceAll(string(inventory.Entities[index]),
				`"subnet_type":"OVERLAY"`, `"subnet_type":"OVERLAY","vpc_reference":{"uuid":"first-vpc"}`))
		}

		duplicate := strings.ReplaceAll(string(inventory.Entities[index]), identity, "duplicate-"+identity)
		duplicate = strings.ReplaceAll(duplicate, "first-vpc", "second-vpc")
		inventory.Entities = append(inventory.Entities, json.RawMessage(duplicate))
		inventory.Metadata["length"] = int64(len(inventory.Entities))
		inventory.Metadata["total_matches"] = int64(len(inventory.Entities))

		err = json.NewEncoder(writer).Encode(inventory)
		if err != nil {
			log.Print(err)
		}

		return true
	}

	return false
}

func (f *fixture) serveLifecycleScenario(writer http.ResponseWriter, request *http.Request) bool {
	if f.Scenario != managementIPScenario && f.Scenario != "nil-management-ip" &&
		f.Scenario != crashScenario && f.Scenario != recoveryScenario {
		return false
	}

	switch request.URL.Path {
	case "/api/nutanix/v3/tasks/task":
		writeResponse(writer, []byte(`{"status":"SUCCEEDED"}`))
	case "/api/nutanix/v3/vms/" + fixtureVMUUID:
		f.VMReads++

		endpoints := "null"
		if (f.Scenario == managementIPScenario && f.VMReads > 1) || f.Scenario == recoveryScenario {
			endpoints = `[null,{}, {"ip":"fd00::2"},{"ip":"10.0.0.20"},{"ip":"::ffff:10.0.0.10"}]`
		}

		writeResponse(writer, []byte(`{"metadata":{"uuid":"`+fixtureVMUUID+`"},
			"spec":{"name":"test","cluster_reference":{"uuid":"cluster-id"}},
			"status":{"state":"COMPLETE","resources":{"nic_list":[
			{"subnet_reference":{"uuid":"vlan-id"},"ip_endpoint_list":[{"ip":"10.99.0.1"}]},null,
			{"subnet_reference":{"uuid":"overlay-id"},"ip_endpoint_list":`+endpoints+`}]}}}`))
	default:
		return false
	}

	return true
}

func serveLookup(writer http.ResponseWriter, request *http.Request) {
	names := map[string]string{
		"/api/nutanix/v3/clusters/list": "clusters.json",
		"/api/nutanix/v3/subnets/list":  "subnets.json",
		"/api/nutanix/v3/images/list":   "images.json",
	}

	if name, ok := names[request.URL.Path]; ok {
		data, err := os.ReadFile("/fixtures/" + name)
		if err != nil {
			http.Error(writer, "fixture missing", http.StatusInternalServerError)

			return
		}

		writeResponse(writer, data)

		return
	}

	http.Error(writer, "unexpected fixture path", http.StatusNotFound)
}

func writeResponse(writer http.ResponseWriter, data []byte) {
	_, err := writer.Write(data)
	if err != nil {
		log.Print(err)
	}
}
