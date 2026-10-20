package driver_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nutanix/docker-machine/machine/driver"
)

const (
	journalLostResponseMode    = "lost-create-response"
	journalMachineDir          = "machines"
	publicJournalFileMode      = 0o644
	publicJournalDirectoryMode = 0o777
)

const journalTestDirectoryMode = 0o700

func TestPreCreateCheckpointRecovery(t *testing.T) {
	t.Parallel()

	for _, expectedCreates := range []int{0, 1} {
		name := []string{"before submission", "before lost response"}[expectedCreates]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runCheckpointRecovery(t, expectedCreates)
		})
	}
}

func runCheckpointRecovery(t *testing.T, expectedCreates int) {
	t.Helper()
	fixture := newDurableFixture(t, journalLostResponseMode)
	node := payloadDriver(t, fixture.server, "")

	err := node.PreCreateCheck()
	if err != nil {
		t.Fatal(err)
	}

	checkpoint, err := os.ReadFile(
		filepath.Join(node.StorePath, journalMachineDir, node.MachineName, "nutanix-create.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	// Consumer Save marshals public state without replacing the running driver.
	public, err := json.Marshal(node) //nolint:gosec,musttag // Fixture-only RPC state retains implicit JSON keys.
	if err != nil {
		t.Fatal(err)
	}

	if expectedCreates == 1 {
		err = node.Create()
		if err == nil {
			t.Fatal("fixture must lose VM response")
		}
	}

	restored := restoreDriverCheckpoint(t, public, checkpoint)

	err = restored.Create()
	if (err != nil) == (expectedCreates == 1) {
		t.Fatalf("restored pre-Create checkpoint: %v", err)
	}

	if expectedCreates == 0 {
		err = restored.Remove()
		if err == nil {
			t.Fatal("reserved checkpoint absence was discarded by Remove")
		}
	}

	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	if fixture.allocations != 1 || fixture.creates != expectedCreates || fixture.deletes != 0 {
		t.Fatalf("checkpoint mutation: allocations=%d creates=%d deletes=%d",
			fixture.allocations, fixture.creates, fixture.deletes)
	}
}

func restoreDriverCheckpoint(t *testing.T, public, checkpoint []byte) *driver.NutanixDriver {
	t.Helper()

	restored := new(driver.NutanixDriver)
	decodePayloadJSON(t, public, restored)
	restored.StorePath = t.TempDir()
	directory := filepath.Join(restored.StorePath, journalMachineDir, restored.MachineName)

	err := os.MkdirAll(directory, journalTestDirectoryMode)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // Restore a fixture-owned machine checkpoint into its private temporary store.
	err = os.WriteFile(filepath.Join(directory, "nutanix-create.json"), checkpoint, fixtureKeyMode)
	if err != nil {
		t.Fatal(err)
	}

	return restored
}

func TestCreateSerialization(t *testing.T) {
	t.Parallel()

	for _, sameInstance := range []bool{true, false} {
		t.Run(map[bool]string{true: "same driver", false: "same machine store"}[sameInstance], func(t *testing.T) {
			t.Parallel()
			fixture := newDurableFixture(t, journalLostResponseMode)
			node := payloadDriver(t, fixture.server, "")

			other := node
			if !sameInstance {
				other = payloadDriver(t, fixture.server, "")
				other.StorePath = node.StorePath
			}

			start := make(chan struct{})
			results := make(chan error, 2)

			for _, instance := range []*driver.NutanixDriver{node, other} {
				go func() { <-start; results <- instance.Create() }()
			}

			close(start)

			first, second := <-results, <-results

			if (first == nil) == (second == nil) {
				t.Fatal("concurrent Create results: want one lost response and one recovery")
			}

			fixture.mu.Lock()
			defer fixture.mu.Unlock()

			if fixture.allocations != 1 || fixture.creates != 1 || fixture.deletes != 0 {
				t.Fatalf("concurrent mutation: allocations=%d creates=%d deletes=%d",
					fixture.allocations, fixture.creates, fixture.deletes)
			}
		})
	}
}

func TestAllocatorIdentityBoundary(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{
		"allocator-count", "allocator-uuid", "allocator-expired",
		"allocator-no-expiry", "allocator-client", "allocator-no-client",
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture := newDurableFixture(t, mode)
			node := payloadDriver(t, fixture.server, "")
			err := node.Create()

			wantError := mode != "allocator-no-client"
			if (err != nil) != wantError {
				t.Fatalf("allocator validation: %v", err)
			}

			if wantError {
				err = node.Create()
				if err == nil {
					t.Fatal("invalid allocation recovered")
				}
			}

			fixture.mu.Lock()
			defer fixture.mu.Unlock()

			wantCreates := 0
			if !wantError {
				wantCreates = 1
			}

			if fixture.allocations != 1 || fixture.creates != wantCreates || fixture.deletes != 0 {
				t.Fatalf("allocator mutations: allocations=%d creates=%d deletes=%d",
					fixture.allocations, fixture.creates, fixture.deletes)
			}
		})
	}
}

func TestJournalFilesystemBoundary(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"escape", "machine-link", "lock-link", "public-lock", "public-directory"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture := newPayloadFixture(t, "")
			node := payloadDriver(t, fixture.server, "")

			directory := filepath.Join(node.StorePath, journalMachineDir, node.MachineName)

			err := os.MkdirAll(directory, journalTestDirectoryMode)
			if err != nil {
				t.Fatal(err)
			}

			switch mode {
			case "escape":
				node.MachineName = "../escape"
			case "machine-link":
				node.MachineName = "alias"

				err = os.Symlink(directory, filepath.Join(node.StorePath, journalMachineDir, "alias"))
				if err != nil {
					t.Fatal(err)
				}
			case "lock-link":
				err = os.Symlink(filepath.Join(t.TempDir(), "lock"), filepath.Join(directory, "nutanix-operation.lock"))
				if err != nil {
					t.Fatal(err)
				}
			case "public-lock":
				// G306: an empty public lock is the unsafe fixture being rejected.
				err = os.WriteFile(filepath.Join(directory, "nutanix-operation.lock"), nil, publicJournalFileMode)
				if err != nil {
					t.Fatal(err)
				}
			case "public-directory":
				// G302: a writable temporary directory is the unsafe fixture being rejected.
				err = os.Chmod(directory, publicJournalDirectoryMode)
				if err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatal("unknown filesystem case")
			}

			err = node.Create()
			if err == nil || fixture.calls.Load() != 0 {
				t.Fatalf("unsafe path reached API: %v", err)
			}
		})
	}
}

func TestRemoveDurableIdentity(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{journalLostResponseMode, "unknown-absent", "foreign-vm"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture := newDurableFixture(t, mode)

			node := payloadDriver(t, fixture.server, "")

			err := node.Create()
			if err == nil {
				t.Fatal("fixture must interrupt Create")
			}

			node.VMId = ""
			for range 2 {
				err := node.Remove()
				if (err != nil) != (mode != journalLostResponseMode) {
					t.Fatalf("Remove after failed Create: %v", err)
				}
			}

			fixture.mu.Lock()
			defer fixture.mu.Unlock()

			wantDeletes := 0
			if mode == journalLostResponseMode {
				wantDeletes = 1
			}

			if fixture.creates != 1 || fixture.deletes != wantDeletes {
				t.Fatalf("repeated Remove: creates=%d deletes=%d", fixture.creates, fixture.deletes)
			}
		})
	}
}

func TestRemoveLateCreate(t *testing.T) {
	t.Parallel()
	fixture := newDurableFixture(t, "unknown-absent")
	node := payloadDriver(t, fixture.server, "")

	err := node.Create()
	if err == nil {
		t.Fatal("fixture must interrupt Create")
	}

	node.VMId = ""

	err = node.Remove()
	if err == nil {
		t.Fatal("unknown create absence was treated as terminal")
	}

	data, err := os.ReadFile(filepath.Join(node.StorePath, journalMachineDir, node.MachineName, "nutanix-create.json"))
	if err != nil || !strings.Contains(string(data), `"phase":"submitted"`) {
		t.Fatalf("unknown operation journal was discarded: %v", err)
	}

	fixture.mu.Lock()
	fixture.present, fixture.mode = true, "recovered"
	fixture.mu.Unlock()

	for range 2 {
		err = node.Remove()
		if err != nil {
			t.Fatal(err)
		}
	}

	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	if fixture.creates != 1 || fixture.deletes != 1 {
		t.Fatalf("late create: creates=%d deletes=%d", fixture.creates, fixture.deletes)
	}
}
