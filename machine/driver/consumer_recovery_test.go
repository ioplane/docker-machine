package driver_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nutanix/docker-machine/machine/driver"
	"github.com/rancher/machine/libmachine/drivers"
	"github.com/rancher/machine/libmachine/state"
)

type consumerOptions struct {
	queueOptions

	node *driver.NutanixDriver
}

func (o consumerOptions) String(key string) string {
	switch key {
	case "nutanix-endpoint":
		return o.node.Endpoint
	case "nutanix-port":
		return o.node.Port
	case "nutanix-vm-description":
		return o.node.Description
	case "nutanix-username", "nutanix-password":
		return "rotated-api-credential"
	default:
		return o.queueOptions.String(key)
	}
}

func (o consumerOptions) StringSlice(key string) []string {
	if key == "nutanix-vm-network" {
		return o.node.Subnet
	}

	return o.queueOptions.StringSlice(key)
}

func (consumerOptions) Bool(key string) bool { return key == "nutanix-insecure" }

func prepareConsumerRecovery(t *testing.T, node *driver.NutanixDriver, opts drivers.DriverOptions,
	request drivers.CreateRecoveryContext,
) (bool, error) {
	t.Helper()

	recovery, ok := any(node).(drivers.CreateRecoveryDriver)
	if !ok {
		t.Fatal("Nutanix driver lacks optional Create recovery capability")
	}

	eligible, err := recovery.PrepareCreateRecovery(opts, request)
	if err != nil {
		return false, fmt.Errorf("prepare fixture Create recovery: %w", err)
	}

	return eligible, nil
}

func consumerCheckpoint(t *testing.T, mode string,
) (*driver.NutanixDriver, *durableFixture, drivers.CreateRecoveryContext) {
	t.Helper()
	fixture := newDurableFixture(t, "lost-create-response")
	node := payloadDriver(t, fixture.server, "")
	script := filepath.Join(t.TempDir(), "install.sh")
	writeFixtureKey(t, script, "#!/bin/sh\necho fixture-script-content\n")
	request := drivers.CreateRecoveryContext{CustomInstallScriptPath: script}

	if mode == "consumer" {
		eligible, err := prepareConsumerRecovery(t, node, nil, request)
		if err != nil || eligible {
			t.Fatalf("fresh recovery preparation: eligible=%v err=%v", eligible, err)
		}
	}

	err := node.SetConfigFromFlags(currentConsumerOptions(node))
	if err != nil {
		t.Fatal(err)
	}

	err = node.PreCreateCheck()
	if err != nil {
		t.Fatal(err)
	}

	request.Existing = true

	return node, fixture, request
}

func recoveryCandidate(node *driver.NutanixDriver) *driver.NutanixDriver {
	candidate := driver.NewDriver(node.MachineName, node.StorePath)
	candidate.SSHKeyPath = node.SSHKeyPath

	return candidate
}

func currentConsumerOptions(node *driver.NutanixDriver) consumerOptions {
	options := queueOptions{sockets: node.VMVCPUs, cores: node.VMCores}

	return consumerOptions{queueOptions: options, node: node}
}

func alterConsumerJournal(t *testing.T, node *driver.NutanixDriver, key string, value any) {
	t.Helper()

	path := filepath.Join(node.StorePath, "machines", node.MachineName, "nutanix-create.json")
	//nolint:gosec // Read only the fixture-owned private machine journal.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var journal map[string]any

	decodePayloadJSON(t, data, &journal)
	journal[key] = value

	data, err = json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}

	writeFixtureKey(t, path, string(data))
}

func TestConsumerRecoveryIneligibleBeforeInputs(t *testing.T) {
	t.Parallel()

	for _, phase := range []string{"legacy", "complete", "removed", "allocating"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()

			mode := "consumer"
			if phase == "legacy" {
				mode = "ordinary"
			}

			node, fixture, request := consumerCheckpoint(t, mode)
			if phase != "legacy" {
				alterConsumerJournal(t, node, "phase", phase)
			}

			if phase == "allocating" {
				alterConsumerJournal(t, node, "vmUuid", "")
			}

			request.CustomInstallScriptPath = filepath.Join(t.TempDir(), "missing-script")

			eligible, err := prepareConsumerRecovery(t, recoveryCandidate(node), nil, request)
			if err != nil || eligible {
				t.Fatalf("ineligible checkpoint examined current inputs: eligible=%v err=%v", eligible, err)
			}

			if fixture.creates != 0 || fixture.allocations != 1 {
				t.Fatalf("eligibility mutated remote state: creates=%d allocations=%d", fixture.creates, fixture.allocations)
			}
		})
	}
}

func TestConsumerRecoveryWithoutCheckpoint(t *testing.T) {
	t.Parallel()
	fixture := newDurableFixture(t, "lost-create-response")
	node := payloadDriver(t, fixture.server, "")

	for _, request := range []drivers.CreateRecoveryContext{
		{}, {Existing: true, CustomInstallScriptPath: filepath.Join(t.TempDir(), "missing-script")},
	} {
		eligible, err := prepareConsumerRecovery(t, node, nil, request)
		if err != nil || eligible {
			t.Fatalf("checkpoint-free recovery examined inputs: eligible=%v err=%v", eligible, err)
		}
	}
}

func TestConsumerRecoveryPendingDoesNotPrepareSubmission(t *testing.T) {
	t.Parallel()

	for _, phase := range []string{"reserved", "submitted", "accepted"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			node, fixture, request := consumerCheckpoint(t, "consumer")
			alterConsumerJournal(t, node, "phase", phase)
			copyPath := filepath.Join(t.TempDir(), "identical-script")
			writeFixtureKey(t, copyPath, "#!/bin/sh\necho fixture-script-content\n")
			request.CustomInstallScriptPath, request.HostnameOverride = copyPath, node.MachineName
			candidate := recoveryCandidate(node)
			calls := fixture.calls

			eligible, err := prepareConsumerRecovery(t, candidate, currentConsumerOptions(node), request)
			if err != nil || !eligible || fixture.calls != calls || candidate.Username != "rotated-api-credential" {
				t.Fatalf("pending recovery: eligible=%v calls=%d want=%d err=%v", eligible, fixture.calls, calls, err)
			}

			err = candidate.Create()
			if err == nil || fixture.creates != 0 || fixture.allocations != 1 {
				t.Fatalf("recovery reconstructed POST permission: creates=%d alloc=%d err=%v",
					fixture.creates, fixture.allocations, err)
			}
		})
	}
}

func TestConsumerRecoveryIntentBoundary(t *testing.T) {
	t.Parallel()

	for _, failure := range []string{"script", "hostname", "VM intent", "missing key", "VM identity", "corrupt UUID"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			node, fixture, request := consumerCheckpoint(t, "consumer")
			candidate := recoveryCandidate(node)
			opts := currentConsumerOptions(node)

			switch failure {
			case "script":
				writeFixtureKey(t, request.CustomInstallScriptPath, "changed-secret-script")
			case "hostname":
				request.HostnameOverride = "changed-hostname"
			case "VM intent":
				opts.sockets++
			case "missing key":
				err := os.Remove(node.SSHKeyPath)
				if err != nil {
					t.Fatal(err)
				}
			case "VM identity":
				candidate.VMId = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
			case "corrupt UUID":
				alterConsumerJournal(t, node, "vmUuid", "invalid")
			default:
				t.Fatal("unknown intent failure")
			}

			calls := fixture.calls

			eligible, err := prepareConsumerRecovery(t, candidate, opts, request)
			if err == nil || eligible || fixture.calls != calls {
				t.Fatalf("mismatched recovery accepted: eligible=%v calls=%d want=%d err=%v", eligible, fixture.calls, calls, err)
			}

			if strings.Contains(err.Error(), "changed-secret-script") ||
				strings.Contains(err.Error(), "private-key-corruption-marker") {
				t.Fatal("recovery diagnostic disclosed input contents")
			}
		})
	}
}

func TestCreateSSHDefaultSurvivesConsumerLifecycle(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"ordinary", "consumer"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			node, _, request := consumerCheckpoint(t, mode)

			err := node.Create()
			if err == nil {
				t.Fatal("fixture must interrupt Create")
			}

			err = node.Create()
			if err != nil {
				t.Fatal(err)
			}

			if node.GetSSHUsername() != "root" {
				t.Fatal("unexpected default SSH username")
			}

			if mode == "consumer" {
				err = os.Remove(request.CustomInstallScriptPath)
				if err != nil {
					t.Fatal(err)
				}
			}

			public, err := json.Marshal(node) //nolint:gosec,musttag // Fixture RPC state retains implicit JSON keys.
			if err != nil {
				t.Fatal(err)
			}

			restored := new(driver.NutanixDriver)
			decodePayloadJSON(t, public, restored)

			observed, err := restored.GetState()
			if err != nil || observed != state.Running {
				t.Fatalf("restored GetState without script: state=%v err=%v", observed, err)
			}

			err = restored.Remove()
			if err != nil {
				t.Fatalf("restored Remove without script: %v", err)
			}
		})
	}
}
