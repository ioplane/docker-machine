package driver //nolint:testpackage // Identity selection is private; no test-only exported API is introduced.

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	prismv3 "github.com/nutanix-cloud-native/prism-go-client/v3"
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
				SubnetReference: &prismv3.Reference{UUID: new("management")}, IPEndpointList: testCase.endpoints,
			}
			data := &prismv3.VMNicOutputStatus{
				SubnetReference: &prismv3.Reference{UUID: new("data")},
				IPEndpointList:  []*prismv3.IPAddress{{IP: new("10.99.0.1")}},
			}

			for _, nics := range [][]*prismv3.VMNicOutputStatus{{nil, data, management}, {management, data}} {
				address, err := managementIP(nics, "management")
				if err != nil || address != testCase.want {
					t.Fatalf("management identity: address=%s want=%s err=%v", address, testCase.want, err)
				}
			}
		})
	}
}

func TestManagementIdentityAmbiguity(t *testing.T) {
	t.Parallel()

	nic := &prismv3.VMNicOutputStatus{SubnetReference: &prismv3.Reference{UUID: new("management")}}

	_, err := managementIP([]*prismv3.VMNicOutputStatus{nic, nic}, "management")
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
		SubnetReference: &prismv3.Reference{UUID: new("management")}, IsConnected: new(false),
		IPEndpointList: []*prismv3.IPAddress{{IP: new("10.0.0.1")}},
	}}, "management")
	if err != nil || address != "" {
		t.Fatalf("disconnected NIC address selected: %s, err=%v", address, err)
	}
}
