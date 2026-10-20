package driver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	prismv3 "github.com/nutanix-cloud-native/prism-go-client/v3"
	"github.com/nutanix-cloud-native/prism-go-client/v3/models"
	"github.com/nutanix/docker-machine/machine/driver"
)

const (
	foreignVMMode = "foreign-vm"
	namedNICMode  = "nic-named"
)

const (
	matchedNICMode       = "nic-matched"
	journalSubnetUUIDKey = "subnetUuid"
	unknownAbsentMode    = "unknown-absent"
	metadataUUIDKey      = "uuid"
	unmatchedNICAPICalls = 12
	matchedNICAPICalls   = 6
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
	nics        []*prismv3.VMNic
	server      *httptest.Server
}

func TestCreateDurableRecovery(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{
		"lost-create-response", "lost-allocation-response", "IP-timeout", "failed-task", unknownAbsentMode, foreignVMMode,
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

			wantError := mode == "failed-task" || mode == unknownAbsentMode || mode == foreignVMMode
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
		if f.mode == foreignVMMode {
			name = "foreign"
		}

		if f.mode == "IP-timeout" {
			f.mode = "recovered"
			endpoints = nil
		}

		nics := f.statusNICs(endpoints)

		writeRemovalResponse(t, writer, map[string]any{
			"metadata": map[string]string{metadataUUIDKey: durableVMUUID},
			"spec":     map[string]any{"name": name, "cluster_reference": map[string]string{metadataUUIDKey: "cluster-id"}},
			"status":   map[string]any{"state": "COMPLETE", "resources": map[string]any{"power_state": "ON", "nic_list": nics}},
		})
	default:
		responses := map[string][]byte{
			"/api/nutanix/v3/clusters/list": fixtureJSON(t, "clusters.json"),
			"/api/nutanix/v3/subnets/list":  fixtureJSON(t, "subnets.json"),
			"/api/nutanix/v3/images/list":   fixtureJSON(t, "images.json"),
		}
		if f.mode == namedNICMode {
			responses["/api/nutanix/v3/subnets/list"] = []byte(strings.NewReplacer(
				fixtureVLANSubnet, fixtureDirectSubnet, fixtureOverlaySubnet, managementSubnet,
			).Replace(string(fixtureJSON(t, "subnets.json"))))
		}

		servePayloadFixture(t, make(chan createPayload, 1), responses, writer, request)
	}
}

func (f *durableFixture) statusNICs(endpoints []map[string]string) []any {
	if !strings.HasPrefix(f.mode, "nic-") {
		return []any{map[string]any{
			"subnet_reference": map[string]string{metadataUUIDKey: fixtureDirectSubnet}, "ip_endpoint_list": endpoints,
		}}
	}

	nics := make([]any, 0, len(f.nics))
	for index, nic := range slices.Backward(f.nics) {
		item := map[string]any{
			"subnet_reference": nic.SubnetReference, "num_queues": nic.NumQueues,
			"ip_endpoint_list": []map[string]string{{"ip": []string{"10.0.0.11", "10.0.0.12"}[index]}},
		}
		if index == 0 {
			switch f.mode {
			case "nic-nil-queues":
				delete(item, "num_queues")
			case "nic-mismatch":
				item["num_queues"] = 1
			case "nic-missing":
				continue
			case "nic-duplicate":
				item["subnet_reference"] = f.nics[1].SubnetReference
			case "nic-nil":
				nics = append(nics, nil)

				continue
			default: // Matched readback echoes the decoded request value.
			}
		}

		nics = append(nics, item)
	}

	return nics
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

	f.nics = intent.Spec.Resources.NicList

	f.present = f.mode != unknownAbsentMode
	if f.mode == "lost-create-response" || f.mode == unknownAbsentMode || f.mode == foreignVMMode {
		loseCreateResponse(t, writer)

		return
	}

	writeRemovalResponse(t, writer, map[string]any{
		"metadata": map[string]any{metadataUUIDKey: durableVMUUID},
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

const managementSubnet = "ffffffff-ffff-ffff-ffff-ffffffffffff"

func TestCreateManagementIntent(t *testing.T) {
	t.Parallel()

	for _, selector := range []string{"", managementSubnet, strings.ToUpper(managementSubnet)} {
		t.Run("selector="+selector, func(t *testing.T) {
			t.Parallel()
			fixture := newDurableFixture(t, matchedNICMode)
			node := payloadDriver(t, fixture.server, "")
			node.Subnet = []string{fixtureDirectSubnet, managementSubnet}

			data, err := json.Marshal(map[string]string{"VMManagementNetwork": selector})
			if err != nil {
				t.Fatal(err)
			}

			decodePayloadJSON(t, data, node)

			err = node.Create()
			if err != nil {
				t.Fatal(err)
			}

			wantSubnet, wantIP := managementSubnet, "10.0.0.12"
			if selector == "" {
				wantSubnet, wantIP = fixtureDirectSubnet, "10.0.0.11"
			}

			journal := readIntentJournal(t, node)
			if journal[journalSubnetUUIDKey] != wantSubnet || node.IPAddress != wantIP {
				t.Fatalf("management subnet=%v IP=%s", journal[journalSubnetUUIDKey], node.IPAddress)
			}

			fixture.mu.Lock()
			defer fixture.mu.Unlock()

			if *fixture.nics[0].SubnetReference.UUID != fixtureDirectSubnet ||
				*fixture.nics[1].SubnetReference.UUID != managementSubnet || fixture.calls != matchedNICAPICalls {
				t.Fatalf("NIC order or API budget changed: calls=%d nics=%v", fixture.calls, fixture.nics)
			}
		})
	}
}

func readIntentJournal(t *testing.T, node *driver.NutanixDriver) map[string]any {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(node.StorePath, "machines", node.MachineName, "nutanix-create.json"))
	if err != nil {
		t.Fatal(err)
	}

	var journal map[string]any
	decodePayloadJSON(t, data, &journal)

	return journal
}

func TestCreateManagementIntentRejectedBeforeWrites(t *testing.T) {
	t.Parallel()

	for _, selector := range []string{"invalid", managementSubnet, fixtureDirectSubnet} {
		t.Run(selector, func(t *testing.T) {
			t.Parallel()
			fixture := newDurableFixture(t, matchedNICMode)
			node := payloadDriver(t, fixture.server, "")

			node.Subnet = []string{fixtureDirectSubnet}
			if selector == fixtureDirectSubnet {
				node.Subnet = append(node.Subnet, fixtureDirectSubnet)
			}

			data, err := json.Marshal(map[string]string{"VMManagementNetwork": selector})
			if err != nil {
				t.Fatal(err)
			}

			decodePayloadJSON(t, data, node)

			err = node.Create()
			if err == nil {
				t.Error("invalid selector accepted")
			}

			fixture.mu.Lock()
			defer fixture.mu.Unlock()

			if fixture.allocations != 0 || fixture.creates != 0 || selector == "invalid" && fixture.calls != 0 {
				t.Fatalf("invalid intent reached writes: alloc=%d VM=%d calls=%d",
					fixture.allocations, fixture.creates, fixture.calls)
			}
		})
	}
}

func TestCreateQueuePostconditionRecovery(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{
		matchedNICMode, namedNICMode, "nic-nil-queues", "nic-mismatch", "nic-missing", "nic-duplicate", "nic-nil",
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture := newDurableFixture(t, mode)
			node := payloadDriver(t, fixture.server, "2")
			node.Timeout = 1

			node.Subnet = []string{fixtureDirectSubnet, managementSubnet}
			if mode == namedNICMode {
				node.Subnet = []string{"vlan", "overlay"}
			}

			decodePayloadJSON(t, []byte(`{"VMManagementNetwork":"ffffffff-ffff-ffff-ffff-ffffffffffff"}`), node)

			err := node.Create()

			matched := mode == matchedNICMode || mode == namedNICMode
			if matched {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertQueueRecovery(t, fixture, node, err)
			}

			fixture.mu.Lock()
			defer fixture.mu.Unlock()

			wantCalls := matchedNICAPICalls
			if !matched {
				wantCalls = unmatchedNICAPICalls
			}

			if mode == namedNICMode {
				wantCalls++
			}

			assertQueueAPIBudget(t, fixture, wantCalls)
		})
	}
}

func assertQueueAPIBudget(t *testing.T, fixture *durableFixture, wantCalls int) {
	t.Helper()

	if fixture.allocations != 1 || fixture.creates != 1 || fixture.deletes != 0 || fixture.calls != wantCalls {
		t.Fatalf("API budget: allocations=%d creates=%d deletes=%d calls=%d want=%d",
			fixture.allocations, fixture.creates, fixture.deletes, fixture.calls, wantCalls)
	}
}

func assertQueueRecovery(t *testing.T, fixture *durableFixture, node *driver.NutanixDriver, createErr error) {
	t.Helper()

	if createErr == nil || !strings.Contains(createErr.Error(), "NIC queues postcondition") {
		t.Fatalf("missing postcondition error: %v", createErr)
	}

	if node.VMId != durableVMUUID || readIntentJournal(t, node)["phase"] == "complete" {
		t.Fatal("identity lost or premature completion")
	}

	public, err := json.Marshal(node) //nolint:gosec,musttag // Dummy persisted RPC configuration.
	if err != nil {
		t.Fatal(err)
	}

	restored := new(driver.NutanixDriver)
	decodePayloadJSON(t, public, restored)

	err = restored.Create()
	if err == nil {
		t.Fatal("recovery accepted unchanged bad queues")
	}

	fixture.mu.Lock()
	fixture.mode = matchedNICMode
	fixture.mu.Unlock()

	err = restored.Create()
	if err != nil {
		t.Fatal(err)
	}

	if restored.VMId != durableVMUUID || restored.IPAddress != "10.0.0.12" {
		t.Fatal("recovery changed VM or management identity")
	}
}

func TestManagementSelectorChangedAfterReserve(t *testing.T) {
	t.Parallel()
	fixture := newDurableFixture(t, matchedNICMode)
	node := payloadDriver(t, fixture.server, "")
	node.Subnet = []string{fixtureDirectSubnet, managementSubnet}
	decodePayloadJSON(t, []byte(`{"VMManagementNetwork":"ffffffff-ffff-ffff-ffff-ffffffffffff"}`), node)

	err := node.PreCreateCheck()
	if err != nil {
		t.Fatal(err)
	}

	before := readIntentJournal(t, node)
	decodePayloadJSON(t, []byte(`{"VMManagementNetwork":"11111111-1111-1111-1111-111111111111"}`), node)

	for _, operation := range []func() error{
		node.Create, node.PreCreateCheck, node.Start, node.Stop, node.Restart, node.Remove,
	} {
		err = operation()
		if err == nil {
			t.Error("changed management selector accepted")
		}
	}

	_, err = node.GetState()
	if err == nil {
		t.Error("GetState accepted changed management selector")
	}

	after := readIntentJournal(t, node)
	if after["binding"] != before["binding"] || after[journalSubnetUUIDKey] != before[journalSubnetUUIDKey] {
		t.Fatal("journal rebound")
	}

	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	const preparationCalls = 3
	if fixture.allocations != 1 || fixture.creates != 0 || fixture.calls != preparationCalls {
		t.Fatalf("changed selector reached API: calls=%d", fixture.calls)
	}
}
