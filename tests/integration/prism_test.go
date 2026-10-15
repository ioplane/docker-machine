package integration_test

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nutanix/docker-machine/machine/driver"
	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	fixtureHTTPTimeout    = 5 * time.Second
	fixtureMemoryMiB      = 2048
	fixtureTimeoutSeconds = 5
	fixtureKeyMode        = 0o600
	fixtureDirectoryMode  = 0o700
	fixtureNICCount       = 3
	missingSubnetScenario = "missing-subnet"
)

type capturedReference struct {
	UUID string `json:"uuid"`
}

type capturedNIC struct {
	SubnetReference capturedReference `json:"subnet_reference"` //nolint:tagliatelle // Prism v3 schema spelling.
	Queues          int               `json:"num_queues"`       //nolint:tagliatelle // Prism v3 schema spelling.
}

type capturedResources struct {
	NICs []capturedNIC `json:"nic_list"` //nolint:tagliatelle // Prism v3 schema spelling.
}

type capturedSpec struct {
	Resources capturedResources `json:"resources"`
}

type capturedPayload struct {
	Spec capturedSpec `json:"spec"`
}

type capturedCreate struct {
	Creates     int             `json:"creates"`
	Allocations int             `json:"allocations"`
	Payload     capturedPayload `json:"payload"`
}

//nolint:paralleltest // Scenarios share one mutable container fixture and must run in sequence.
func TestPrismBoundary(t *testing.T) {
	endpoint, client := startFixture(t)

	for _, scenario := range []string{
		"queues", "429", "500", "malformed", "missing-metadata", missingSubnetScenario,
		"duplicate-vlan", "duplicate-overlay", "management-ip", "nil-management-ip",
	} {
		t.Run(scenario, func(t *testing.T) {
			runScenario(t, client, endpoint, scenario)
		})
	}

	t.Run("crash-restart", func(t *testing.T) { runCrashRecovery(t, client, endpoint) })
}

func startFixture(t *testing.T) (string, *http.Client) {
	t.Helper()
	//nolint:gosec // Readiness probes only the disposable fixture's ephemeral self-signed TLS endpoint.
	readinessTLS := &tls.Config{InsecureSkipVerify: true}
	readiness := wait.ForHTTP("/health").WithPort("8443/tcp").WithTLS(true, readinessTLS).WithStartupTimeout(time.Minute)

	container, err := tc.Run(t.Context(), "localhost/ioplane-prism-fixture:premerge",
		tc.WithProvider(tc.ProviderPodman), tc.WithExposedPorts("8443/tcp"),
		tc.WithWaitStrategy(readiness))

	tc.CleanupContainer(t, container)

	if err != nil {
		t.Fatal(err)
	}

	endpoint, err := container.PortEndpoint(t.Context(), "8443/tcp", "https")
	if err != nil {
		t.Fatal(err)
	}

	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("unsupported default transport %T", http.DefaultTransport)
	}

	transport := defaultTransport.Clone()
	//nolint:gosec // Only the disposable fixture uses an ephemeral self-signed certificate.
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	t.Cleanup(transport.CloseIdleConnections)

	return endpoint, &http.Client{Transport: transport, Timeout: fixtureHTTPTimeout}
}

func runScenario(t *testing.T, client *http.Client, endpoint, scenario string) {
	t.Helper()
	requestFixture(t, client, endpoint+"/scenario", `{"scenario":"`+scenario+`"}`)

	nodeDriver := newDriver(t, strings.TrimPrefix(endpoint, "https://"))

	switch scenario {
	case "management-ip":
		nodeDriver.Timeout = 2 * fixtureTimeoutSeconds
	case "nil-management-ip":
		nodeDriver.Timeout = 1
	default:
		// Other scenarios use the fixture's default deadline.
	}

	if scenario == missingSubnetScenario {
		nodeDriver.Subnet = append(nodeDriver.Subnet, "absent")
	}

	err := nodeDriver.Create()
	if scenario == "management-ip" {
		if err != nil || nodeDriver.IPAddress != "10.0.0.10" {
			t.Fatalf("management subnet selection: IP=%q err=%v", nodeDriver.IPAddress, err)
		}
	} else {
		assertScenarioError(t, scenario, err)
	}

	var captured capturedCreate

	err = json.Unmarshal(requestFixture(t, client, endpoint+"/capture", ""), &captured)
	if err != nil {
		t.Fatal(err)
	}

	assertCapturedCreate(t, captured, scenario)
}

func assertCapturedCreate(t *testing.T, captured capturedCreate, scenario string) {
	t.Helper()

	if scenario == missingSubnetScenario || strings.HasPrefix(scenario, "duplicate-") {
		if captured.Creates != 0 {
			t.Fatalf("partial NIC profile reached create: %d", captured.Creates)
		}

		return
	}

	if captured.Creates != 1 {
		t.Fatalf("Create POST count=%d, want 1; mutation must not be retried", captured.Creates)
	}

	if len(captured.Payload.Spec.Resources.NICs) != fixtureNICCount {
		t.Fatalf("NIC count=%d, want 3", len(captured.Payload.Spec.Resources.NICs))
	}

	subnets := []string{"overlay-id", "11111111-1111-1111-1111-111111111111", "vlan-id"}

	for index, nic := range captured.Payload.Spec.Resources.NICs {
		if nic.SubnetReference.UUID != subnets[index] {
			t.Fatalf("NIC %d subnet=%s, want %s", index, nic.SubnetReference.UUID, subnets[index])
		}

		if nic.Queues != 2 {
			t.Fatalf("num_queues=%d, want 2", nic.Queues)
		}
	}
}

func requestFixture(t *testing.T, client *http.Client, endpoint, body string) []byte {
	t.Helper()

	method := http.MethodGet
	if body != "" {
		method = http.MethodPost
	}

	req, err := http.NewRequestWithContext(t.Context(), method, endpoint, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		closeErr := resp.Body.Close()
		if closeErr != nil {
			t.Error(closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fixture HTTP status=%d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func newDriver(t *testing.T, endpoint string) *driver.NutanixDriver {
	t.Helper()

	nodeDriver := driver.NewDriver("test", t.TempDir())

	var err error

	nodeDriver.Endpoint, nodeDriver.Port, err = net.SplitHostPort(endpoint)
	if err != nil {
		t.Fatal(err)
	}

	nodeDriver.Username, nodeDriver.Password = "fixture", "fixture"
	nodeDriver.Cluster, nodeDriver.Image = "test-only", "test-only"
	nodeDriver.Insecure, nodeDriver.VMVCPUs, nodeDriver.VMCores = true, 2, 2
	nodeDriver.VMMem, nodeDriver.Timeout = fixtureMemoryMiB, fixtureTimeoutSeconds
	nodeDriver.VMNicQueues, nodeDriver.BootType = "2", "legacy"
	nodeDriver.Subnet = []string{"overlay", "11111111-1111-1111-1111-111111111111", "vlan"}
	nodeDriver.SSHKeyPath = filepath.Join(nodeDriver.StorePath, "machines", nodeDriver.MachineName, "id_rsa")

	err = os.MkdirAll(filepath.Dir(nodeDriver.SSHKeyPath), fixtureDirectoryMode)
	if err != nil {
		t.Fatal(err)
	}

	for _, suffix := range []string{"", ".pub"} {
		writeErr := os.WriteFile(nodeDriver.SSHKeyPath+suffix, []byte("fixture-placeholder"), fixtureKeyMode)
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}

	return nodeDriver
}

func assertScenarioError(t *testing.T, scenario string, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("fixture must reject Create")
	}

	if scenario == "malformed" {
		var syntaxError *json.SyntaxError

		if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.As(err, &syntaxError) {
			t.Fatalf("malformed response lost decoder error: %v", err)
		}

		return
	}

	expected := map[string]string{
		"queues": "task UUID", "429": "429", "500": "500",
		"missing-metadata": "VM UUID", missingSubnetScenario: "network",
		"duplicate-vlan": "network", "duplicate-overlay": "network", "nil-management-ip": "deadline exceeded",
	}
	if !strings.Contains(err.Error(), expected[scenario]) {
		t.Fatalf("scenario %s returned unexpected error: %v", scenario, err)
	}
}
