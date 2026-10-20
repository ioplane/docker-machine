package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nutanix/docker-machine/machine/driver"
)

const (
	recoveryDirectoryMode = 0o700
)

const (
	workerConfigEnvironment     = "NUTANIX_FIXTURE_WORKER_CONFIG"
	workerResultEnvironment     = "NUTANIX_FIXTURE_WORKER_RESULT"
	workerCheckpointEnvironment = "NUTANIX_FIXTURE_WORKER_CHECKPOINT"
	workerTimeout               = 60
	workerPoll                  = 20 * time.Millisecond
	expectedRecoveryUUID        = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
)

func TestCreateCrashWorker(t *testing.T) {
	t.Parallel()

	config := os.Getenv(workerConfigEnvironment)
	if config == "" {
		t.Skip("subprocess helper")
	}

	data, err := os.ReadFile(config) //nolint:gosec // Parent passes its test-owned temporary config path.
	if err != nil {
		t.Fatal(err)
	}

	var node driver.NutanixDriver

	err = json.Unmarshal(data, &node)
	if err != nil {
		t.Fatal(err)
	}

	checkpoint := os.Getenv(workerCheckpointEnvironment)
	if checkpoint != "" {
		err = node.PreCreateCheck()
		if err != nil {
			t.Fatal(err)
		}

		checkpointMachine(t, node.StorePath, checkpoint)
	}

	err = node.Create()
	if err != nil {
		if checkpoint != "" {
			// Keep the failed worker until SIGKILL; Machine does not Save failed Nutanix Create.
			<-t.Context().Done()
		}

		t.Fatal(err)
	}

	result, err := json.Marshal(map[string]string{"uuid": node.VMId, "ip": node.IPAddress})
	if err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // Parent passes its test-owned temporary result path.
	err = os.WriteFile(os.Getenv(workerResultEnvironment), result, fixtureKeyMode)
	if err != nil {
		t.Fatal(err)
	}
}

func runCrashRecovery(t *testing.T, client *http.Client, endpoint string) {
	t.Helper()
	requestFixture(t, client, endpoint+"/scenario", `{"scenario":"crash"}`)
	nodeDriver := newDriver(t, strings.TrimPrefix(endpoint, "https://"))
	nodeDriver.Timeout = workerTimeout

	data, err := json.Marshal(nodeDriver) //nolint:gosec // Disposable fixture credentials only.
	if err != nil {
		t.Fatal(err)
	}

	config := filepath.Join(t.TempDir(), "worker.json")

	result := filepath.Join(t.TempDir(), "result.json")

	err = os.WriteFile(config, data, fixtureKeyMode)
	if err != nil {
		t.Fatal(err)
	}

	checkpoint := t.TempDir()
	worker, output := crashWorker(t, config, result, checkpoint)

	err = worker.Start()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if worker.ProcessState == nil {
			killErr := worker.Process.Kill()
			if killErr != nil {
				t.Error(killErr)
			}

			waitErr := worker.Wait()
			if waitErr == nil {
				t.Error("killed worker exited successfully")
			}
		}
	})
	waitForSubmittedCreate(t, client, endpoint)
	// A competing consumer still has the pre-Create config and must not submit another mutation.
	nodeDriver.Timeout = 1

	err = nodeDriver.Create()
	if err == nil {
		t.Fatal("concurrent Create completed against the interrupted fixture")
	}

	err = worker.Process.Kill()
	if err != nil {
		t.Fatal(err)
	}

	err = worker.Wait()
	if err == nil {
		t.Fatalf("killed worker exited successfully: %s", output.String())
	}

	requestFixture(t, client, endpoint+"/resume", `{}`)
	// Restore the older pre-Create Secret checkpoint, excluding the killed worker's newer local journal.
	restoreWorkerCheckpoint(t, nodeDriver, checkpoint, config)

	restarted, restartedOutput := crashWorker(t, config, result, "")

	err = restarted.Run()
	if err != nil {
		t.Fatalf("restarted Create: %v\n%s", err, restartedOutput.String())
	}

	assertRecoveredWorker(t, client, endpoint, result)
}

func restoreWorkerCheckpoint(t *testing.T, nodeDriver *driver.NutanixDriver, checkpoint, config string) {
	t.Helper()
	nodeDriver.StorePath = t.TempDir()
	nodeDriver.SSHKeyPath = filepath.Join(nodeDriver.StorePath, "machines", nodeDriver.MachineName, "id_rsa")
	nodeDriver.Timeout = workerTimeout
	checkpointMachine(t, checkpoint, nodeDriver.StorePath)
	nodeDriver.VMId, nodeDriver.IPAddress = "", ""

	data, err := json.Marshal(nodeDriver) //nolint:gosec // Disposable fixture credentials only.
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(config, data, fixtureKeyMode)
	if err != nil {
		t.Fatal(err)
	}
}

func crashWorker(t *testing.T, config, result, checkpoint string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	//nolint:gosec // Execute the current test binary with one fixed test name.
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCreateCrashWorker$")

	command.Env = append(os.Environ(), workerConfigEnvironment+"="+config, workerResultEnvironment+"="+result)
	command.Env = append(command.Env, workerCheckpointEnvironment+"="+checkpoint)
	output := new(bytes.Buffer)
	command.Stdout, command.Stderr = output, output

	return command, output
}

func waitForSubmittedCreate(t *testing.T, client *http.Client, endpoint string) {
	t.Helper()

	deadline := time.NewTimer(fixtureHTTPTimeout)
	defer deadline.Stop()

	ticker := time.NewTicker(workerPoll)
	defer ticker.Stop()

	for {
		var capture capturedCreate

		err := json.Unmarshal(requestFixture(t, client, endpoint+"/capture", ""), &capture)
		if err != nil {
			t.Fatal(err)
		}

		if capture.Creates > 0 {
			return
		}

		select {
		case <-deadline.C:
			t.Fatal("worker did not submit its VM POST")
		case <-ticker.C:
		}
	}
}

//nolint:gosec // Both roots and all files belong to this disposable test's temporary machine stores.
func checkpointMachine(t *testing.T, source, target string) {
	t.Helper()

	err := fs.WalkDir(os.DirFS(source), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		name := filepath.Join(target, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(name, recoveryDirectoryMode)
		}

		data, readErr := os.ReadFile(filepath.Join(source, filepath.FromSlash(path)))
		if readErr != nil {
			return fmt.Errorf("read checkpoint file: %w", readErr)
		}

		return os.WriteFile(name, data, fixtureKeyMode)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertRecoveredWorker(t *testing.T, client *http.Client, endpoint, result string) {
	t.Helper()

	data, err := os.ReadFile(result) //nolint:gosec // The result path belongs to this test's temporary directory.
	if err != nil {
		t.Fatal(err)
	}

	var recovered map[string]string

	err = json.Unmarshal(data, &recovered)
	if err != nil {
		t.Fatal(err)
	}

	if recovered["uuid"] != expectedRecoveryUUID || recovered["ip"] != "10.0.0.10" {
		t.Fatalf("recovered identity/address: %v", recovered)
	}

	var captured capturedCreate

	err = json.Unmarshal(requestFixture(t, client, endpoint+"/capture", ""), &captured)
	if err != nil {
		t.Fatal(err)
	}

	if captured.Creates != 1 || captured.Allocations != 1 {
		t.Fatalf("crash replayed mutation: alloc=%d create=%d", captured.Allocations, captured.Creates)
	}
}
