package driver_test

import (
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nutanix/docker-machine/machine/driver"
	"go.yaml.in/yaml/v3"
)

const (
	payloadMaxBodyBytes   = 1 << 20
	payloadCreateAPICalls = 5
)

const (
	fixturePublicKey     = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDtqJ7zOtqQtYqOo0CpvDXNlMhV3HeJDpjrASKGLWdop"
	fixtureDirectSubnet  = "11111111-1111-1111-1111-111111111111"
	fixtureVLANSubnet    = "22222222-2222-2222-2222-222222222222"
	fixtureOverlaySubnet = "33333333-3333-3333-3333-333333333333"
	fixtureKeyMode       = 0o600
	fixtureMemoryMiB     = 2048
	fixtureQueues        = "2"
	fixtureDescription   = "offline payload fixture"
)

type createPayload map[string]json.RawMessage

type payloadFixture struct {
	server   *httptest.Server
	requests chan createPayload
	calls    atomic.Int64
}

type cloudInitUser struct {
	Name  string
	Shell string
	Keys  []string `yaml:"ssh_authorized_keys"` //nolint:tagliatelle // This is the cloud-init schema key.
}

type cloudInitConfig struct {
	Users []cloudInitUser
}

func TestCreateQueuePayload(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "2", "3"} {
		t.Run("queues="+value, func(t *testing.T) {
			t.Parallel()

			responseName := ""
			if value == "3" {
				responseName = "missing-task.json"
			}

			fixture := newPayloadFixture(t, responseName)
			d := payloadDriver(t, fixture.server, value)

			err := d.Create()
			if err == nil {
				t.Fatal("fixture must stop create before any VM lifecycle")
			}

			if value == "3" && !strings.Contains(err.Error(), "task UUID") {
				t.Fatalf("missing task UUID returned unexpected error: %v", err)
			}

			payload := capturedPayload(t, fixture)
			assertQueuePayload(t, payload, value, []string{fixtureDirectSubnet, fixtureVLANSubnet, fixtureOverlaySubnet})
			assertCloudMetadata(t, payload)
			assertCorePayload(t, payload)
			assertDefaultCloudInit(t, payloadResources(t, payload))

			if calls := fixture.calls.Load(); calls != payloadCreateAPICalls {
				t.Errorf("create fixture received %d API calls, want three lookups, allocator and create", calls)
			}
		})
	}
}

func TestCreateCloudInitPayload(t *testing.T) {
	t.Parallel()

	fixture := newPayloadFixture(t, "")
	d := payloadDriver(t, fixture.server, fixtureQueues)
	d.CloudInit = "#cloud-config\nusers:\n  - name: declared-user\n    shell: /bin/bash\n"

	err := d.Create()
	if err == nil {
		t.Fatal("fixture must stop create before any VM lifecycle")
	}

	resources := payloadResources(t, capturedPayload(t, fixture))
	userData := decodedCloudInitField(t, resources, "user_data")
	assertCloudInitUsers(t, userData)
}

func TestCreateMalformedCloudInit(t *testing.T) {
	t.Parallel()

	fixture := newPayloadFixture(t, "")
	d := payloadDriver(t, fixture.server, fixtureQueues)
	d.CloudInit = "#cloud-config\nusers: [\n"

	err := d.Create()
	if err == nil {
		t.Fatal("Create accepted malformed cloud-init")
	}

	select {
	case <-fixture.requests:
		t.Fatal("malformed cloud-init reached the VM create API")
	default:
	}
}

func TestCreatePersistedInvalidQueuesBeforeAPI(t *testing.T) {
	t.Parallel()

	fixture := newPayloadFixture(t, "")
	d := payloadDriver(t, fixture.server, "")
	decodePayloadJSON(t, []byte(`{"VMNicQueues":"0"}`), d)

	err := d.Create()
	if err == nil {
		t.Fatal("Create accepted invalid persisted queue configuration")
	}

	if calls := fixture.calls.Load(); calls != 0 {
		t.Fatalf("invalid persisted queues made %d API requests before rejection", calls)
	}
}

func fixtureJSON(t *testing.T, name string) []byte {
	t.Helper()

	data, err := fs.ReadFile(os.DirFS("testdata/queue-payload"), name)
	if err != nil {
		t.Fatalf("read API fixture %s: %v", name, err)
	}

	return data
}

func newPayloadFixture(t *testing.T, responseName string) *payloadFixture {
	t.Helper()

	fixture := &payloadFixture{requests: make(chan createPayload, 1)}

	responses := map[string][]byte{
		"/api/nutanix/v3/clusters/list": fixtureJSON(t, "clusters.json"),
		"/api/nutanix/v3/subnets/list":  fixtureJSON(t, "subnets.json"),
		"/api/nutanix/v3/images/list":   fixtureJSON(t, "images.json"),
	}
	if responseName != "" {
		responses["/api/nutanix/v3/vms"] = fixtureJSON(t, responseName)
	}

	fixture.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.calls.Add(1)
		servePayloadFixture(t, fixture.requests, responses, w, r)
	}))
	t.Cleanup(fixture.server.Close)

	return fixture
}

func servePayloadFixture(t *testing.T, requests chan<- createPayload, responses map[string][]byte,
	writer http.ResponseWriter, request *http.Request,
) {
	t.Helper()

	if request.Method != http.MethodPost {
		t.Errorf("unexpected fixture method %s", request.Method)
		http.Error(writer, "unexpected method", http.StatusMethodNotAllowed)

		return
	}

	writer.Header().Set("Content-Type", "application/json")

	if request.URL.Path == "/api/nutanix/v3/idempotence_identifiers" {
		var input map[string]any

		err := json.NewDecoder(request.Body).Decode(&input)
		if err != nil {
			t.Error(err)

			return
		}

		writeRemovalResponse(t, writer, map[string]any{
			"client_identifier": input["client_identifier"], "count": 1,
			"uuid_list": []string{durableVMUUID}, "expiration_time": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})

		return
	}

	if request.URL.Path == "/api/nutanix/v3/vms" {
		captureCreatePayload(t, requests, writer, request)
	}

	response, found := responses[request.URL.Path]
	if !found {
		if request.URL.Path != "/api/nutanix/v3/vms" {
			t.Errorf("unexpected fixture API path %s", request.URL.Path)
		}

		http.Error(writer, `{"message":"fixture stops at create"}`, http.StatusBadRequest)

		return
	}

	_, err := writer.Write(response)
	if err != nil {
		t.Errorf("write API fixture: %v", err)
	}
}

func captureCreatePayload(t *testing.T, requests chan<- createPayload,
	writer http.ResponseWriter, request *http.Request,
) {
	t.Helper()

	var payload createPayload

	err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, payloadMaxBodyBytes)).Decode(&payload)
	if err != nil {
		t.Errorf("decode create payload: %v", err)

		return
	}

	select {
	case requests <- payload:
	default:
		t.Error("unexpected duplicate VM create request")
	}
}

func payloadDriver(t *testing.T, server *httptest.Server, queues string) *driver.NutanixDriver {
	t.Helper()

	nodeDriver := driver.NewDriver("test", t.TempDir())

	err := nodeDriver.SetConfigFromFlags(queueOptions{queues: queues, sockets: 2, cores: 2})
	if err != nil {
		t.Fatal(err)
	}

	nodeDriver.Endpoint, nodeDriver.Port, err = net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	nodeDriver.Insecure = true // Only the ephemeral TLS fixture uses an untrusted certificate.
	nodeDriver.VMMem = fixtureMemoryMiB
	nodeDriver.Description = fixtureDescription
	nodeDriver.Subnet = []string{fixtureDirectSubnet, "vlan", "overlay"}
	nodeDriver.SSHKeyPath = filepath.Join(t.TempDir(), "id_rsa")
	writeFixtureKey(t, nodeDriver.SSHKeyPath, fixturePrivateKey(t, 0))
	writeFixtureKey(t, nodeDriver.SSHKeyPath+".pub", fixturePublicKey)

	return nodeDriver
}

func writeFixtureKey(t *testing.T, path, content string) {
	t.Helper()

	err := os.WriteFile(path, []byte(content), fixtureKeyMode)
	if err != nil {
		t.Fatalf("write fixture key: %v", err)
	}
}

func capturedPayload(t *testing.T, fixture *payloadFixture) createPayload {
	t.Helper()

	select {
	case payload := <-fixture.requests:
		return payload
	default:
		t.Fatal("driver never reached create payload fixture")

		return nil
	}
}

func decodePayloadJSON(t *testing.T, raw []byte, target any) {
	t.Helper()

	err := json.Unmarshal(raw, target)
	if err != nil {
		t.Fatalf("decode payload JSON: %v", err)
	}
}

func payloadResources(t *testing.T, payload createPayload) createPayload {
	t.Helper()

	var spec createPayload

	decodePayloadJSON(t, payload["spec"], &spec)

	var resources createPayload

	decodePayloadJSON(t, spec["resources"], &resources)

	return resources
}

func assertQueuePayload(t *testing.T, payload createPayload, queues string, wantSubnets []string) {
	t.Helper()

	resources := payloadResources(t, payload)

	var nics []createPayload

	decodePayloadJSON(t, resources["nic_list"], &nics)

	if len(nics) != len(wantSubnets) {
		t.Fatalf("expected UUID, VLAN and OVERLAY NICs, got %d", len(nics))
	}

	for index, nic := range nics {
		assertNICQueues(t, nic, queues)

		var reference createPayload

		decodePayloadJSON(t, nic["subnet_reference"], &reference)

		var subnet string

		decodePayloadJSON(t, reference["uuid"], &subnet)

		if subnet != wantSubnets[index] {
			t.Errorf("NIC %d subnet = %q, want %q", index, subnet, wantSubnets[index])
		}
	}
}

func assertNICQueues(t *testing.T, nic createPayload, want string) {
	t.Helper()

	queues, present := nic["num_queues"]
	if want == "" && present || want != "" && string(queues) != want {
		t.Errorf("queue JSON = %s, present = %v, want %q", queues, present, want)
	}
}

func assertCorePayload(t *testing.T, payload createPayload) {
	t.Helper()

	var expected map[string]createPayload

	decodePayloadJSON(t, fixtureJSON(t, "core-payload.json"), &expected)

	var metadata, spec createPayload

	decodePayloadJSON(t, payload["metadata"], &metadata)
	decodePayloadJSON(t, payload["spec"], &spec)

	actual := map[string]createPayload{
		"metadata":  metadata,
		"spec":      spec,
		"resources": payloadResources(t, payload),
	}

	for section, fields := range expected {
		for field, expectedJSON := range fields {
			var got, want any

			decodePayloadJSON(t, actual[section][field], &got)
			decodePayloadJSON(t, expectedJSON, &want)

			if !reflect.DeepEqual(got, want) {
				t.Errorf("create payload %s.%s = %#v, want %#v", section, field, got, want)
			}
		}
	}
}

func assertDefaultCloudInit(t *testing.T, resources createPayload) {
	t.Helper()

	var config cloudInitConfig

	userData := decodedCloudInitField(t, resources, "user_data")

	err := yaml.Unmarshal(userData, &config)
	if err != nil {
		t.Fatalf("decode default cloud-init: %v", err)
	}

	if len(config.Users) != 1 {
		t.Fatalf("default cloud-init has %d users, want root", len(config.Users))
	}

	root := config.Users[0]
	if root.Name != "root" || len(root.Keys) != 1 || root.Keys[0] != fixturePublicKey {
		t.Fatal("default cloud-init lost the root SSH key")
	}
}

func decodedCloudInitField(t *testing.T, resources createPayload, field string) []byte {
	t.Helper()

	var customization createPayload

	decodePayloadJSON(t, resources["guest_customization"], &customization)

	var cloudInit createPayload

	decodePayloadJSON(t, customization["cloud_init"], &cloudInit)

	var encoded string

	decodePayloadJSON(t, cloudInit[field], &encoded)

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode SDK cloud-init payload: %v", err)
	}

	return decoded
}

func assertCloudMetadata(t *testing.T, payload createPayload) {
	t.Helper()

	metadata := decodedCloudInitField(t, payloadResources(t, payload), "meta_data")

	var fields map[string]string

	decodePayloadJSON(t, metadata, &fields)

	if fields["hostname"] != "test" || len(fields) != 2 {
		t.Fatalf("metadata lost hostname or changed keys: %v", fields)
	}

	_, err := uuid.Parse(fields["uuid"])
	if err != nil {
		t.Fatalf("metadata uuid is invalid: %v", err)
	}
}

func assertCloudInitUsers(t *testing.T, userData []byte) {
	t.Helper()

	var config cloudInitConfig

	err := yaml.Unmarshal(userData, &config)
	if err != nil {
		t.Fatalf("decode merged cloud-init: %v", err)
	}

	if len(config.Users) != 2 {
		t.Fatalf("merged cloud-init has %d users, want declared and root", len(config.Users))
	}

	declared, root := config.Users[0], config.Users[1]
	if declared.Name != "declared-user" || declared.Shell != "/bin/bash" {
		t.Fatal("merged cloud-init lost the declared user or shell")
	}

	if root.Name != "root" || len(root.Keys) != 1 || root.Keys[0] != fixturePublicKey {
		t.Fatal("merged cloud-init lost the injected root SSH key")
	}
}

func TestCreateMissingMetadataReturnsError(t *testing.T) {
	t.Parallel()
	fixture := newPayloadFixture(t, "missing-metadata.json")
	d := payloadDriver(t, fixture.server, fixtureQueues)

	err := d.Create()
	if err == nil {
		t.Fatal("accepted missing VM metadata")
	}
}

func TestCreateAcceptedIdentityRetained(t *testing.T) {
	t.Parallel()
	fixture := newPayloadFixture(t, "missing-task.json")
	nodeDriver := payloadDriver(t, fixture.server, fixtureQueues)

	err := nodeDriver.Create()
	if err == nil {
		t.Fatal("accepted missing task")
	}

	if nodeDriver.VMId != durableVMUUID {
		t.Fatalf("accepted identity lost: VMId=%q", nodeDriver.VMId)
	}
}

func TestCreateMissingAdditionalSubnet(t *testing.T) {
	t.Parallel()
	fixture := newPayloadFixture(t, "")
	d := payloadDriver(t, fixture.server, fixtureQueues)

	d.Subnet = append(d.Subnet, "absent")

	err := d.Create()
	if err == nil {
		t.Fatal("accepted partial NIC profile")
	}

	select {
	case <-fixture.requests:
		t.Fatal("partial NIC profile reached VM create API")
	default:
	}
}

func TestCreateNICInputOrder(t *testing.T) {
	t.Parallel()

	fixture := newPayloadFixture(t, "")
	node := payloadDriver(t, fixture.server, fixtureQueues)
	node.Subnet = []string{"overlay", fixtureDirectSubnet, "vlan"}

	err := node.Create()
	if err == nil {
		t.Fatal("fixture must reject create after capturing ordered NICs")
	}

	assertQueuePayload(t, capturedPayload(t, fixture), fixtureQueues,
		[]string{fixtureOverlaySubnet, fixtureDirectSubnet, fixtureVLANSubnet})
}
