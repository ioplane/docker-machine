package integration_test

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
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

func TestPrismBoundary(t *testing.T) {
	container, err := tc.Run(t.Context(), "localhost/ioplane-prism-fixture:premerge",
		tc.WithProvider(tc.ProviderPodman), tc.WithExposedPorts("8443/tcp"),
		tc.WithWaitStrategy(wait.ForHTTP("/health").WithPort("8443/tcp").WithTLS(true).WithStartupTimeout(time.Minute)))
	tc.CleanupContainer(t, container)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := container.PortEndpoint(t.Context(), "8443/tcp", "https")
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // Test-only ephemeral certificate.
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for _, scenario := range []string{"queues", "429", "500", "malformed", "missing-metadata", "missing-subnet"} {
		t.Run(scenario, func(t *testing.T) {
			requestFixture(t, client, endpoint+"/scenario", `{"scenario":"`+scenario+`"}`)
			d := newDriver(t, strings.TrimPrefix(endpoint, "https://"))
			if scenario == "missing-subnet" {
				d.Subnet = append(d.Subnet, "absent")
			}
			if err := d.Create(); err == nil {
				t.Fatal("fixture must reject Create")
			}
			var captured struct {
				Creates int `json:"creates"`
				Payload struct {
					Spec struct {
						Resources struct {
							NICs []struct {
								Queues int `json:"num_queues"`
							} `json:"nic_list"`
						} `json:"resources"`
					} `json:"spec"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(requestFixture(t, client, endpoint+"/capture", ""), &captured); err != nil {
				t.Fatal(err)
			}
			if scenario == "missing-subnet" {
				if captured.Creates != 0 {
					t.Fatalf("partial NIC profile reached create: %d", captured.Creates)
				}
				return
			}
			if captured.Creates != 1 {
				t.Fatalf("Create POST count=%d, want 1; mutation must not be retried", captured.Creates)
			}
			if len(captured.Payload.Spec.Resources.NICs) != 3 {
				t.Fatalf("NIC count=%d, want 3", len(captured.Payload.Spec.Resources.NICs))
			}
			for _, nic := range captured.Payload.Spec.Resources.NICs {
				if nic.Queues != 2 {
					t.Fatalf("num_queues=%d, want 2", nic.Queues)
				}
			}
		})
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
		if err := resp.Body.Close(); err != nil {
			t.Error(err)
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
	d := driver.NewDriver("test", t.TempDir())
	var err error
	d.Endpoint, d.Port, err = net.SplitHostPort(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	d.Username, d.Password, d.Cluster, d.Image = "fixture", "fixture", "test-only", "test-only"
	d.Insecure, d.VMVCPUs, d.VMCores, d.VMMem, d.Timeout = true, 2, 2, 2048, 5
	d.VMNicQueues, d.BootType = "2", "legacy"
	d.Subnet = []string{"11111111-1111-1111-1111-111111111111", "vlan", "overlay"}
	d.SSHKeyPath = filepath.Join(t.TempDir(), "id_rsa")
	for _, suffix := range []string{"", ".pub"} {
		if err := os.WriteFile(d.SSHKeyPath+suffix, []byte("fixture-placeholder"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return d
}
