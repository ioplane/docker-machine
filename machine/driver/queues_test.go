package driver_test

import (
	"encoding/json"
	"testing"

	"github.com/nutanix/docker-machine/machine/driver"

	"github.com/rancher/machine/libmachine/mcnflag"
)

type queueOptions struct {
	queues  string
	sockets int
	cores   int
}

func (o queueOptions) String(key string) string {
	switch key {
	case "nutanix-vm-nic-queues":
		return o.queues
	case "nutanix-boot-type":
		return "legacy"
	case "nutanix-username", "nutanix-password", "nutanix-endpoint", "nutanix-cluster", "nutanix-vm-image":
		return "test-only"
	default:
		return ""
	}
}

func (queueOptions) StringSlice(key string) []string {
	if key == "nutanix-vm-network" {
		return []string{"test-network"}
	}

	return nil
}

func (o queueOptions) Int(key string) int {
	switch key {
	case "nutanix-vm-mem":
		return fixtureMemoryMiB
	case "nutanix-vm-cpus":
		return o.sockets
	case "nutanix-vm-cores":
		return o.cores
	default:
		return 0
	}
}

func (queueOptions) Bool(string) bool { return false }

func TestQueueFlagIsOptionalString(t *testing.T) {
	t.Parallel()

	for _, flag := range driver.NewDriver("test", "").GetCreateFlags() {
		if flag.String() != "nutanix-vm-nic-queues" {
			continue
		}

		value, ok := flag.(mcnflag.StringFlag)
		if !ok || value.Value != "" {
			t.Fatalf("queue flag must be StringFlag with empty default: %#v", flag)
		}

		return
	}

	t.Fatal("optional queue flag missing")
}

func TestQueueConfiguration(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		value   string
		sockets int
		cores   int
		invalid bool
	}{
		{name: "omitted", sockets: 2, cores: 2},
		{name: "one", value: "1", sockets: 1, cores: 1},
		{name: "multiple sockets", value: "4", sockets: 2, cores: 2},
		{name: "over cpu count", value: "5", sockets: 2, cores: 2, invalid: true},
		{name: "zero", value: "0", sockets: 2, cores: 2, invalid: true},
		{name: "negative", value: "-1", sockets: 2, cores: 2, invalid: true},
		{name: "fraction", value: "1.5", sockets: 2, cores: 2, invalid: true},
		{name: "text", value: "auto", sockets: 2, cores: 2, invalid: true},
		{name: "whitespace", value: " 2", sockets: 2, cores: 2, invalid: true},
		{name: "plus", value: "+2", sockets: 2, cores: 2, invalid: true},
		{name: "overflow", value: "9223372036854775808", sockets: 2, cores: 2, invalid: true},
		{name: "zero sockets", value: "1", cores: 2, invalid: true},
		{name: "negative cores", value: "1", sockets: 2, cores: -1, invalid: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			nodeDriver := driver.NewDriver("test", "")

			err := nodeDriver.SetConfigFromFlags(queueOptions{
				queues: testCase.value, sockets: testCase.sockets, cores: testCase.cores,
			})
			if (err != nil) != testCase.invalid {
				t.Fatalf("SetConfigFromFlags error = %v, invalid = %v", err, testCase.invalid)
			}

			if testCase.invalid {
				return
			}

			// Driver state must include the RPC credential fields; this fixture uses dummy values only.
			data, err := json.Marshal(nodeDriver) //nolint:gosec // G117: required RPC state, dummy credentials only.
			if err != nil {
				t.Fatal(err)
			}

			var state map[string]any

			err = json.Unmarshal(data, &state)
			if err != nil {
				t.Fatal(err)
			}

			value, present := state["VMNicQueues"]
			if testCase.value == "" && present || testCase.value != "" && value != testCase.value {
				t.Fatalf("persisted queues = %#v, present = %v", value, present)
			}
		})
	}
}

type timeoutOptions struct {
	queueOptions

	timeout int
}

func (o timeoutOptions) Int(key string) int {
	if key == "nutanix-timeout" {
		return o.timeout
	}

	return o.queueOptions.Int(key)
}

func TestTimeoutUsesRequestedValue(t *testing.T) {
	t.Parallel()

	const (
		customTimeout  = 600
		minimumTimeout = 300
	)

	for _, testCase := range []struct {
		name      string
		previous  int
		requested int
		want      int
	}{
		{name: "fresh custom timeout", requested: customTimeout, want: customTimeout},
		{name: "configured default", previous: customTimeout, requested: minimumTimeout, want: minimumTimeout},
		{name: "zero after prior configuration", previous: customTimeout, want: minimumTimeout},
		{name: "negative after prior configuration", previous: customTimeout, requested: -1, want: minimumTimeout},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			nodeDriver := driver.NewDriver("test", "")
			nodeDriver.Timeout = testCase.previous

			err := nodeDriver.SetConfigFromFlags(timeoutOptions{
				sockets: 2, cores: 2, timeout: testCase.requested,
			})
			if err != nil {
				t.Fatal(err)
			}

			if nodeDriver.Timeout != testCase.want {
				t.Fatalf("timeout = %d, want %d", nodeDriver.Timeout, testCase.want)
			}
		})
	}
}
