package driver_test

import (
	"bytes"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/nutanix/docker-machine/machine/driver"
	"go.yaml.in/yaml/v3"
)

func TestCreateInvalidInputsBeforeAPI(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		state string
		cloud string
	}{
		{name: "zero persisted timeout", state: `{"Timeout":0}`},
		{name: "negative persisted timeout", state: `{"Timeout":-1}`},
		{name: "overflowing persisted timeout", state: `{"Timeout":9223372037}`},
		{name: "zero sockets without queues", state: `{"VMVCPUs":0}`},
		{name: "negative cores without queues", state: `{"VMCores":-1}`},
		{name: "CPU product overflow", state: `{"VMVCPUs":9223372036854775807,"VMCores":2}`},
		{name: "zero memory", state: `{"VMMem":0}`},
		{name: "negative memory", state: `{"VMMem":-1}`},
		{name: "negative image size", state: `{"ImageSize":-1}`},
		{name: "image MiB overflow", state: `{"ImageSize":9007199254740992}`},
		{name: "negative disk size", state: `{"DiskSize":-1}`},
		{name: "disk bytes overflow", state: `{"DiskSize":8589934592}`},
		{name: "scalar document", cloud: "sensitive-input-marker"},
		{name: "sequence document", cloud: "[sensitive-input-marker]"},
		{name: "null document", cloud: "null"},
		{name: "scalar users", cloud: "users: sensitive-input-marker"},
		{name: "mapping users", cloud: "users: {name: root}"},
		{name: "alias users", cloud: "other: &users []\nusers: *users"},
		{name: "alias root keys", cloud: "other: &keys []\nusers: [{name: root, ssh_authorized_keys: *keys}]"},
		{name: "alias user", cloud: "other: &root {name: root}\nusers: [*root]"},
		{name: "multiple documents", cloud: "users: []\n---\nsensitive-input-marker"},
		{name: "duplicate users key", cloud: "users: []\nusers: []"},
		{name: "duplicate root entries", cloud: "users: [{name: root}, {name: root}]"},
		{name: "duplicate root name", cloud: "users: [{name: root, name: root}]"},
		{name: "invalid root keys", cloud: "users: [{name: root, ssh_authorized_keys: sensitive-input-marker}]"},
		{name: "merged users", cloud: "other: &base {users: []}\n<<: *base"},
		{name: "malformed YAML", cloud: "users: [sensitive-input-marker"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPayloadFixture(t, "")

			nodeDriver := payloadDriver(t, fixture.server, "")
			if testCase.state != "" {
				decodePayloadJSON(t, []byte(testCase.state), nodeDriver)
			} else {
				nodeDriver.CloudInit = "#cloud-config\n" + testCase.cloud + "\n"
			}

			err := nodeDriver.Create()
			if err == nil || strings.Contains(err.Error(), "sensitive-input-marker") {
				t.Fatalf("invalid input was accepted or disclosed: %v", err)
			}

			if calls := fixture.calls.Load(); calls != 0 {
				t.Fatalf("invalid input made %d API requests before rejection", calls)
			}
		})
	}
}

func TestCloudInitRootMerge(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"",
		"#cloud-config",
		"# comment\n",
		"nested:\n  users: [{name: nested-user}]\n",
		"users: [default, root]\n",
		"users:\n  - name: root\n    shell: /bin/bash\n    sudo: custom-rule\n" +
			"    ssh_authorized_keys: [existing-key]\n",
	} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			fixture := newPayloadFixture(t, "")
			nodeDriver := payloadDriver(t, fixture.server, fixtureQueues)

			nodeDriver.CloudInit = "#cloud-config\n" + input
			if input == "#cloud-config" {
				nodeDriver.CloudInit = input
			}

			err := nodeDriver.Create()
			if err == nil {
				t.Fatal("fixture must stop create")
			}

			data := decodedCloudInitField(t, payloadResources(t, capturedPayload(t, fixture)), "user_data")
			if !bytes.HasPrefix(data, []byte("#cloud-config\n")) {
				t.Fatal("cloud-init header lost exact recognition marker")
			}

			assertMergedRoot(t, data, input)

			// Reusing the merged input must not duplicate root or its bootstrap key.
			nodeDriver.CloudInit = string(data)

			err = nodeDriver.Create()
			if err == nil {
				t.Fatal("fixture must stop repeated create")
			}

			again := decodedCloudInitField(t, payloadResources(t, capturedPayload(t, fixture)), "user_data")
			if !bytes.Equal(data, again) {
				t.Fatal("cloud-init root merge is not repeatable")
			}
		})
	}
}

func assertMergedRoot(t *testing.T, data []byte, input string) {
	t.Helper()

	var got map[string]any

	err := yaml.Unmarshal(data, &got)
	if err != nil {
		t.Fatal(err)
	}

	users, ok := got["users"].([]any)
	if !ok {
		t.Fatal("top-level users missing")
	}

	roots := 0

	var root map[string]any

	for _, entry := range users {
		user, isMap := entry.(map[string]any)
		if !isMap || user["name"] != "root" {
			continue
		}

		roots++
		root = user
	}

	if roots != 1 {
		t.Fatalf("root entries = %d, want one", roots)
	}

	keys, isKeys := root["ssh_authorized_keys"].([]any)
	if !isKeys || !slices.Contains(keys, any(fixturePublicKey)) {
		t.Fatalf("bootstrap key missing: %#v", root)
	}

	if strings.Contains(input, "existing-key") &&
		(!slices.Equal(keys, []any{"existing-key", fixturePublicKey}) ||
			root["shell"] != "/bin/bash" || root["sudo"] != "custom-rule") {
		t.Fatalf("existing root settings changed: %#v", root)
	}

	if strings.Contains(input, "nested-user") && got["nested"] == nil {
		t.Fatal("nested settings lost")
	}
}

type hardwareOptions struct {
	queueOptions

	key   string
	value int
}

func (o hardwareOptions) Int(key string) int {
	if key == o.key {
		return o.value
	}

	return o.queueOptions.Int(key)
}

func TestHardwareFlagsValidation(t *testing.T) {
	t.Parallel()

	const (
		testMiBPerGiB   = 1024
		testBytesPerGiB = 1 << 30
	)

	for _, key := range []string{
		"nutanix-vm-cpus", "nutanix-vm-cores", "nutanix-vm-mem", "nutanix-vm-image-size", "nutanix-disk-size",
	} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			nodeDriver := driver.NewDriver("test", "")

			err := nodeDriver.SetConfigFromFlags(hardwareOptions{sockets: 2, cores: 2, key: key, value: -1})
			if err == nil {
				t.Fatal("negative hardware flag accepted")
			}
		})
	}
	// Largest representable conversions are valid; these are not vendor limits.
	nodeDriver := driver.NewDriver("test", "")
	for _, pair := range []struct {
		key   string
		value int
	}{
		{key: "nutanix-vm-image-size", value: math.MaxInt64 / testMiBPerGiB},
		{key: "nutanix-disk-size", value: math.MaxInt64 / testBytesPerGiB},
	} {
		err := nodeDriver.SetConfigFromFlags(hardwareOptions{sockets: 2, cores: 2, key: pair.key, value: pair.value})
		if err != nil {
			t.Fatalf("valid numeric bound rejected: %v", err)
		}
	}
}
