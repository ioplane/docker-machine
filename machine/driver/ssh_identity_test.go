package driver_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nutanix/docker-machine/machine/driver"
)

func TestCreateBootstrapPairBoundary(t *testing.T) {
	t.Parallel()

	for _, phase := range []string{"fresh", "restored"} {
		for _, failure := range []string{"missing", "corrupt", "mismatched"} {
			if phase == "fresh" && failure == "missing" {
				continue
			} // A fresh operation generates its missing key.

			t.Run(phase+"/"+failure, func(t *testing.T) {
				t.Parallel()
				runBootstrapPairFailure(t, phase, failure)
			})
		}
	}
}

func runBootstrapPairFailure(t *testing.T, phase, failure string) {
	t.Helper()

	fixture := newDurableFixture(t, "lost-create-response")

	node := payloadDriver(t, fixture.server, "")
	if phase == "restored" {
		before, err := json.Marshal(node) //nolint:gosec,musttag // Dummy RPC config retains implicit JSON keys.
		if err != nil {
			t.Fatal(err)
		}

		err = node.Create()
		if err == nil {
			t.Fatal("fixture must interrupt Create")
		}

		node = new(driver.NutanixDriver)
		decodePayloadJSON(t, before, node)
	}

	switch failure {
	case "missing":
		err := os.Remove(node.SSHKeyPath)
		if err != nil {
			t.Fatal(err)
		}
	case "corrupt":
		writeFixtureKey(t, node.SSHKeyPath, "private-key-corruption-marker")
	case "mismatched":
		writeFixtureKey(t, node.SSHKeyPath, fixturePrivateKey(t, 1))
	default:
		t.Fatal("unknown key failure")
	}

	fixture.mu.Lock()
	calls := fixture.calls
	fixture.mu.Unlock()

	err := node.Create()

	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	if err == nil || fixture.calls != calls {
		t.Fatalf("unusable bootstrap pair accepted: calls=%d want=%d err=%v", fixture.calls, calls, err)
	}

	if strings.Contains(err.Error(), "private-key-corruption-marker") ||
		strings.Contains(err.Error(), "BEGIN PRIVATE KEY") {
		t.Fatal("bootstrap error disclosed private key content")
	}
}
