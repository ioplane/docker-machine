package driver //nolint:testpackage // Identity selection is private; no test-only exported API is introduced.

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"os"
	"testing"

	prismv3 "github.com/nutanix-cloud-native/prism-go-client/v3"
	"golang.org/x/crypto/ssh"
)

const (
	managementTestSubnet = "management"
)

type networkProfile struct {
	inventory *prismv3.SubnetListIntentResponse
	queries   []string
}

func TestAmbiguousNetworkProfiles(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{
		"duplicate VLAN", "duplicate VPC overlay", "name UUID alias", "malformed inventory", "empty profile", "UUID casing",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			profile := networkProfileFixture(t, scenario)

			data, err := json.Marshal(profile.inventory)
			if err != nil {
				t.Fatal(err)
			}

			conn := lifecycleClient(t, func(request *http.Request) (*http.Response, error) {
				return lifecycleResponse(request, http.StatusOK, string(data)), nil
			})
			node := NewDriver("fixture", "")
			node.Subnet = profile.queries
			resources := &prismv3.VMResources{}

			err = node.addNICs(t.Context(), conn, "cluster-id", resources)
			if err == nil || len(resources.NicList) != 0 {
				t.Fatalf("ambiguous/partial NIC profile accepted: nics=%d err=%v", len(resources.NicList), err)
			}
		})
	}
}

func networkFixture(t *testing.T) *prismv3.SubnetListIntentResponse {
	t.Helper()

	data, err := os.ReadFile("testdata/queue-payload/subnets.json")
	if err != nil {
		t.Fatal(err)
	}

	var inventory prismv3.SubnetListIntentResponse

	err = json.Unmarshal(data, &inventory)
	if err != nil {
		t.Fatal(err)
	}

	return &inventory
}

func TestManagementNICIdentityAndAddresses(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		endpoints []*prismv3.IPAddress
		want      string
	}{
		{name: "nil endpoints"},
		{name: "nil empty entries", endpoints: []*prismv3.IPAddress{nil, {}, {IP: new("")}}},
		{name: "unusable endpoints", endpoints: []*prismv3.IPAddress{
			{IP: new("127.0.0.1")}, {IP: new("169.254.1.1")}, {IP: new("::")}, {IP: new("ff02::1")},
		}},
		{name: "IPv4 preferred and ordered", endpoints: []*prismv3.IPAddress{
			{IP: new("fd00::2")}, {IP: new("10.0.0.20")}, {IP: new("::ffff:10.0.0.10")},
		}, want: "10.0.0.10"},
		{name: "IPv6 parsing", endpoints: []*prismv3.IPAddress{{IP: new("fd00::2")}}, want: "fd00::2"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			management := &prismv3.VMNicOutputStatus{
				SubnetReference: &prismv3.Reference{UUID: new(managementTestSubnet)}, IPEndpointList: testCase.endpoints,
			}
			data := &prismv3.VMNicOutputStatus{
				SubnetReference: &prismv3.Reference{UUID: new("data")},
				IPEndpointList:  []*prismv3.IPAddress{{IP: new("10.99.0.1")}},
			}

			for _, nics := range [][]*prismv3.VMNicOutputStatus{{nil, data, management}, {management, data}} {
				address, err := managementIP(nics, managementTestSubnet)
				if err != nil || address != testCase.want {
					t.Fatalf("management identity: address=%s want=%s err=%v", address, testCase.want, err)
				}
			}
		})
	}
}

func TestManagementIdentityAmbiguity(t *testing.T) {
	t.Parallel()

	nic := &prismv3.VMNicOutputStatus{SubnetReference: &prismv3.Reference{UUID: new(managementTestSubnet)}}

	_, err := managementIP([]*prismv3.VMNicOutputStatus{nic, nic}, managementTestSubnet)
	if err == nil {
		t.Fatal("duplicate management NIC identity accepted")
	}

	_, err = managementIP(nil, "")
	if err == nil {
		t.Fatal("empty management selector accepted")
	}
}

func networkProfileFixture(t *testing.T, scenario string) networkProfile {
	t.Helper()

	inventory := networkFixture(t)
	queries := []string{"vlan"}

	switch scenario {
	case "duplicate VLAN":
		inventory.Entities = append(inventory.Entities, inventory.Entities[0])
	case "duplicate VPC overlay":
		inventory.Entities = append(inventory.Entities, inventory.Entities[1])
		queries[0] = "overlay"
	case "name UUID alias":
		identity := "11111111-1111-1111-1111-111111111111"
		inventory.Entities[0].Metadata.UUID = &identity
		queries = []string{"vlan", identity}
	case "malformed inventory":
		inventory.Entities = []*prismv3.SubnetIntentResponse{nil, {}, {Metadata: &prismv3.Metadata{UUID: new("id")}}}
	case "empty profile":
		queries = nil
	case "UUID casing":
		queries = []string{"AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}
	default:
		t.Fatal("unknown test scenario")
	}

	inventory.Metadata.Length = new(int64(len(inventory.Entities)))
	inventory.Metadata.TotalMatches = new(int64(len(inventory.Entities)))

	return networkProfile{inventory: inventory, queries: queries}
}

func TestManagementEndpointRefusal(t *testing.T) {
	t.Parallel()

	_, err := endpointIP([]*prismv3.IPAddress{{IP: new("invalid-address")}})
	if err == nil {
		t.Fatal("malformed IP accepted")
	}

	address, err := managementIP([]*prismv3.VMNicOutputStatus{{
		SubnetReference: &prismv3.Reference{UUID: new(managementTestSubnet)}, IsConnected: new(false),
		IPEndpointList: []*prismv3.IPAddress{{IP: new("10.0.0.1")}},
	}}, managementTestSubnet)
	if err != nil || address != "" {
		t.Fatalf("disconnected NIC address selected: %s, err=%v", address, err)
	}
}

func TestManagementOmissionLegacyGolden(t *testing.T) {
	t.Parallel()

	node := legacyBindingDriver(t)

	binding, err := node.createBinding()
	if err != nil {
		t.Fatal(err)
	}

	keyPath := node.SSHKeyPath
	node.SSHKeyPath = "/fixture/id"

	state, err := json.Marshal(node) //nolint:gosec,musttag // Legacy RPC state golden has no real credentials.
	if err != nil {
		t.Fatal(err)
	}

	const wantBinding = "5e1cf538ccd4f3f8a1b7f5e9793cbf84cedc3432f09901c7948d8ca8ad314599"

	const wantState = `{"IPAddress":"","IPv6Address":"","MachineName":"legacy","SSHUser":"root","SSHPort":0,` +
		`"SSHKeyPath":"/fixture/id","StorePath":"/fixture","SwarmMaster":false,"SwarmHost":"","SwarmDiscovery":"",` +
		`"Endpoint":"","Username":"","Password":"","Port":"","Insecure":false,"Cluster":"","VMVCPUs":0,"VMCores":0,` +
		`"VMCPUPassthrough":false,"VMMem":0,"SSHPass":"","Subnet":null,"Image":"","ImageSize":0,"VMId":"",` +
		`"SessionAuth":false,"ProxyURL":"","Categories":null,"StorageContainer":"","DiskSize":0,"CloudInit":"",` +
		`"SerialPort":false,"Project":"","BootType":"","Timeout":0,"GPUs":null,"Description":""}`
	if binding != wantBinding || string(state) != wantState {
		t.Fatalf("legacy binding/state changed: binding=%s state=%s", binding, state)
	}

	node.SSHKeyPath = keyPath
	//nolint:musttag // Saved driver state retains implicit JSON keys.
	err = json.Unmarshal([]byte(`{"VMManagementNetwork":"FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF"}`), node)
	if err != nil {
		t.Fatal(err)
	}

	upper, err := node.createBinding()
	if err != nil {
		t.Fatal(err)
	}
	//nolint:musttag // Saved driver state retains implicit JSON keys.
	err = json.Unmarshal([]byte(`{"VMManagementNetwork":"ffffffff-ffff-ffff-ffff-ffffffffffff"}`), node)
	if err != nil {
		t.Fatal(err)
	}

	lower, err := node.createBinding()
	if err != nil {
		t.Fatal(err)
	}

	if upper != lower || lower == binding {
		t.Fatal("explicit selector not bound canonically")
	}
}

func legacyBindingDriver(t *testing.T) *NutanixDriver {
	t.Helper()

	node := NewDriver("legacy", "/fixture")
	node.SSHKeyPath = t.TempDir() + "/id"
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))

	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	const keyMode = 0o600

	err = os.WriteFile(node.SSHKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), keyMode)
	if err != nil {
		t.Fatal(err)
	}

	public, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(node.SSHKeyPath+".pub", ssh.MarshalAuthorizedKey(public), keyMode)
	if err != nil {
		t.Fatal(err)
	}

	return node
}
