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
	"sync"
)

type fixture struct {
	mu sync.Mutex

	Scenario string          `json:"scenario"`
	Creates  int             `json:"creates"`
	Payload  json.RawMessage `json:"payload"`
}

const (
	scenarioBodyLimit = 1024
	payloadBodyLimit  = 1 << 20
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
	}

	if request.URL.Path == "/api/nutanix/v3/vms" && request.Method == http.MethodPost {
		f.createVM(writer, request)

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

	f.Scenario, f.Creates, f.Payload = input.Scenario, 0, nil
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
	case "429":
		writer.WriteHeader(http.StatusTooManyRequests)

		response = "{}"
	case "500":
		writer.WriteHeader(http.StatusInternalServerError)

		response = "{}"
	case "malformed":
		response = "{"
	case "missing-metadata":
		response = `{"status":{"execution_context":{"task_uuid":"task"}}}`
	default:
		response = `{"metadata":{"uuid":"fixture-vm"},"status":{"execution_context":{"task_uuid":null}}}`
	}

	writeResponse(writer, []byte(response))
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
