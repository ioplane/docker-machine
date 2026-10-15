// Package main serves a disposable Prism v3 contract fixture, without external credentials.
package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
)

type fixture struct {
	sync.Mutex
	Scenario string          `json:"scenario"`
	Creates  int             `json:"creates"`
	Payload  json.RawMessage `json:"payload"`
}

func main() {
	f := &fixture{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	listener, err := net.Listen("tcp", ":8443")
	if err != nil {
		log.Fatal(err)
	}
	if err = server.Listener.Close(); err != nil {
		log.Fatal(err)
	}
	server.Listener = listener
	server.StartTLS()
	defer server.Close()
	select {}
}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		w.WriteHeader(http.StatusOK)
		return
	}
	f.Lock()
	defer f.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/scenario" {
		var input struct {
			Scenario string `json:"scenario"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil {
			http.Error(w, "invalid scenario", http.StatusBadRequest)
			return
		}
		f.Scenario, f.Creates, f.Payload = input.Scenario, 0, nil
		return
	}
	if r.URL.Path == "/capture" {
		if err := json.NewEncoder(w).Encode(f); err != nil {
			log.Print(err)
		}
		return
	}
	if r.URL.Path == "/api/nutanix/v3/vms" && r.Method == http.MethodPost {
		f.Creates++
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&f.Payload); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		switch f.Scenario {
		case "429":
			w.WriteHeader(http.StatusTooManyRequests)
		case "500":
			w.WriteHeader(http.StatusInternalServerError)
		case "malformed":
			if _, err := w.Write([]byte("{")); err != nil {
				log.Print(err)
			}
			return
		case "missing-metadata":
			if _, err := w.Write([]byte(`{"status":{"execution_context":{"task_uuid":"task"}}}`)); err != nil {
				log.Print(err)
			}
			return
		default:
			if _, err := w.Write([]byte(`{"metadata":{"uuid":"fixture-vm"},"status":{"execution_context":{"task_uuid":null}}}`)); err != nil {
				log.Print(err)
			}
			return
		}
		if _, err := w.Write([]byte("{}")); err != nil {
			log.Print(err)
		}
		return
	}
	names := map[string]string{
		"/api/nutanix/v3/clusters/list": "clusters.json",
		"/api/nutanix/v3/subnets/list":  "subnets.json",
		"/api/nutanix/v3/images/list":   "images.json",
	}
	if name, ok := names[r.URL.Path]; ok {
		data, err := os.ReadFile("/fixtures/" + name)
		if err != nil {
			http.Error(w, "fixture missing", http.StatusInternalServerError)
			return
		}
		if _, err := w.Write(data); err != nil {
			log.Print(err)
		}
		return
	}
	http.Error(w, "unexpected fixture path", http.StatusNotFound)
}
