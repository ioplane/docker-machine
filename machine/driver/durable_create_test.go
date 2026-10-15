package driver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	prismv3 "github.com/nutanix-cloud-native/prism-go-client/v3"
	"github.com/nutanix-cloud-native/prism-go-client/v3/models"
	"github.com/nutanix/docker-machine/machine/driver"
)

const durableVMUUID = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"

type durableFixture struct {
	mu          sync.Mutex
	mode        string
	clientID    string
	allocations int
	creates     int
	deletes     int
	calls       int
	present     bool
	server      *httptest.Server
}

func TestCreateDurableRecovery(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{
		"lost-create-response", "lost-allocation-response", "IP-timeout", "failed-task", "unknown-absent", "foreign-vm",
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture := newDurableFixture(t, mode)
			node := payloadDriver(t, fixture.server, "")
			node.Timeout = 1

			before, err := json.Marshal(node) //nolint:gosec,musttag // Dummy RPC config retains implicit JSON keys.
			if err != nil {
				t.Fatal(err)
			}

			err = node.Create()
			if err == nil {
				t.Fatal("first interrupted operation unexpectedly succeeded")
			}

			restored := new(driver.NutanixDriver)
			decodePayloadJSON(t, before, restored)
			err = restored.Create()

			fixture.mu.Lock()
			defer fixture.mu.Unlock()

			wantCreates := 1

			wantError := mode == "failed-task" || mode == "unknown-absent" || mode == "foreign-vm"
			if (err != nil) != wantError || fixture.allocations != 1 || fixture.creates != wantCreates || fixture.deletes != 0 {
				t.Fatalf("restart: alloc=%d create=%d delete=%d vm=%q err=%v",
					fixture.allocations, fixture.creates, fixture.deletes, restored.VMId, err)
			}

			if restored.VMId != durableVMUUID {
				t.Fatalf("durable VM identity lost: %q", restored.VMId)
			}
		})
	}
}

func newDurableFixture(t *testing.T, mode string) *durableFixture {
	t.Helper()

	fixture := &durableFixture{mode: mode}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()

		fixture.calls++

		fixture.serve(t, writer, request)
	}))
	t.Cleanup(fixture.server.Close)

	return fixture
}

func TestCreateJournalTrustBoundary(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"corrupt", "intent", "key"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture := newDurableFixture(t, "lost-create-response")

			node := payloadDriver(t, fixture.server, "")

			err := node.Create()
			if err == nil {
				t.Fatal("fixture must interrupt Create")
			}

			node.VMId = ""

			switch mode {
			case "corrupt":
				writeFixtureKey(t, filepath.Join(node.StorePath, "machines", node.MachineName, "nutanix-create.json"), "{")
			case "intent":
				node.VMMem++
			case "key":
				writeFixtureKey(t, node.SSHKeyPath+".pub", "changed-bootstrap-key")
			default:
				t.Fatal("unknown case")
			}

			fixture.mu.Lock()
			calls := fixture.calls
			fixture.mu.Unlock()

			for _, operation := range []func() error{node.Create, node.Start, node.Remove} {
				err = operation()
				if err == nil {
					t.Fatal("mismatched or corrupt intent accepted")
				}
			}

			fixture.mu.Lock()
			defer fixture.mu.Unlock()

			if fixture.calls != calls {
				t.Fatalf("unsafe journal reached API: calls=%d want=%d", fixture.calls, calls)
			}

			data, err := os.ReadFile(filepath.Join(node.StorePath, "machines", node.MachineName, "nutanix-create.json"))
			if err != nil || strings.Contains(string(data), fixturePublicKey) ||
				strings.Contains(string(data), "fixture-password") {
				t.Fatalf("journal contains bootstrap or credential data: %v", err)
			}
		})
	}
}

func (f *durableFixture) serve(t *testing.T, writer http.ResponseWriter, request *http.Request) {
	t.Helper()

	path := strings.TrimPrefix(request.URL.Path, "/api/nutanix/v3/")
	switch {
	case strings.HasPrefix(path, "idempotence_identifiers"):
		f.allocate(t, writer, request)
	case path == "vms" && request.Method == http.MethodPost:
		f.create(t, writer, request)
	case path == "tasks/task":
		status := "SUCCEEDED"
		if f.mode == "failed-task" {
			status = "FAILED"
		}

		writeRemovalResponse(t, writer, map[string]string{"status": status})
	case path == "vms/"+durableVMUUID:
		if request.Method == http.MethodDelete {
			f.deletes++
			f.present = false
		}

		if !f.present {
			writer.WriteHeader(http.StatusNotFound)

			return
		}

		endpoints := []map[string]string{{"ip": "10.0.0.10"}}

		name := "test"
		if f.mode == "foreign-vm" {
			name = "foreign"
		}

		if f.mode == "IP-timeout" {
			f.mode = "recovered"
			endpoints = nil
		}

		writeRemovalResponse(t, writer, map[string]any{
			"metadata": map[string]string{"uuid": durableVMUUID},
			"spec":     map[string]any{"name": name, "cluster_reference": map[string]string{"uuid": "cluster-id"}},
			"status": map[string]any{"state": "COMPLETE", "resources": map[string]any{"nic_list": []any{
				map[string]any{"subnet_reference": map[string]string{"uuid": fixtureDirectSubnet}, "ip_endpoint_list": endpoints},
			}}},
		})
	default:
		responses := map[string][]byte{
			"/api/nutanix/v3/clusters/list": fixtureJSON(t, "clusters.json"),
			"/api/nutanix/v3/subnets/list":  fixtureJSON(t, "subnets.json"),
			"/api/nutanix/v3/images/list":   fixtureJSON(t, "images.json"),
		}
		servePayloadFixture(t, make(chan createPayload, 1), responses, writer, request)
	}
}

func (f *durableFixture) allocate(t *testing.T, writer http.ResponseWriter, request *http.Request) {
	t.Helper()

	if request.Method == http.MethodPost {
		f.allocations++

		var input models.IdempotenceIdentifiersInput

		err := json.NewDecoder(request.Body).Decode(&input)
		if err != nil {
			t.Error(err)
		}

		f.clientID = input.ClientIdentifier
		if f.mode == "lost-allocation-response" {
			loseCreateResponse(t, writer)

			return
		}
	}

	response := map[string]any{
		"client_identifier": f.clientID, "count": 1,
		"uuid_list": []string{durableVMUUID}, "expiration_time": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	switch f.mode {
	case "allocator-count":
		response["count"] = 2
	case "allocator-uuid":
		response["uuid_list"] = []string{"invalid"}
	case "allocator-expired":
		response["expiration_time"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	case "allocator-no-expiry":
		delete(response, "expiration_time")
	case "allocator-client":
		response["client_identifier"] = "other-client"
	case "allocator-no-client":
		delete(response, "client_identifier")
	default: // Other scenarios return a complete allocator response.
	}

	writeRemovalResponse(t, writer, response)
}

func (f *durableFixture) create(t *testing.T, writer http.ResponseWriter, request *http.Request) {
	t.Helper()

	f.creates++

	var intent prismv3.VMIntentInput

	err := json.NewDecoder(request.Body).Decode(&intent)
	if err != nil {
		t.Error(err)
	}

	if intent.Metadata == nil || intent.Metadata.UUID == nil || *intent.Metadata.UUID != durableVMUUID {
		t.Error("VM POST did not use allocated UUID")
	}

	f.present = f.mode != "unknown-absent"
	if f.mode == "lost-create-response" || f.mode == "unknown-absent" || f.mode == "foreign-vm" {
		loseCreateResponse(t, writer)

		return
	}

	writeRemovalResponse(t, writer, map[string]any{
		"metadata": map[string]any{"uuid": durableVMUUID},
		"status":   map[string]any{"execution_context": map[string]any{"task_uuid": "task"}},
	})
}

func loseCreateResponse(t *testing.T, writer http.ResponseWriter) {
	t.Helper()

	connection, _, err := http.NewResponseController(writer).Hijack()
	if err != nil {
		t.Error(err)

		return
	}

	err = connection.Close()
	if err != nil {
		t.Error(err)
	}
}
