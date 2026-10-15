package integration_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	consumerCompletePUT      = 3
	rotatedFixtureCredential = "rotated"
	consumerMode             = "ssh-custom-script/v1"
)

var errConsumerCheckpoint = errors.New("invalid consumer checkpoint")

type checkpointSecret struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   map[string]string `json:"metadata"`
	Data       map[string][]byte `json:"data,omitempty"`
}

type consumerDriverState struct {
	VMID     string `json:"vmId"`
	IP       string `json:"ipAddress"`
	User     string `json:"username"`
	Password string `json:"password"`
	Binding  string `json:"consumerCreateBinding"`
}

type consumerCheckpoint struct {
	key         [32]byte
	phase, uuid string
	driver      consumerDriverState
}

type consumerCapture struct {
	Creates     int      `json:"creates"`
	Allocations int      `json:"allocations"`
	Deletes     int      `json:"deletes"`
	Reads       []string `json:"recoveryVmReads"`
}

type consumerInputPaths struct {
	store, kubeconfig, script string
}

type checkpointAPI struct {
	mu         sync.Mutex
	secret     checkpointSecret
	puts       int
	first      consumerCheckpoint
	checkpoint chan error
	complete   chan error
}

func (s *checkpointAPI) validate(snapshot *consumerCheckpoint) error {
	phase := "reserved"
	if s.puts == consumerCompletePUT {
		phase = "complete"
	}

	if snapshot.phase != phase || snapshot.uuid != expectedRecoveryUUID || snapshot.driver.Binding == "" {
		return fmt.Errorf("%w: phase/UUID/proof", errConsumerCheckpoint)
	}

	if s.puts > 1 && (snapshot.key != s.first.key || snapshot.driver.Binding != s.first.driver.Binding ||
		snapshot.driver.User != rotatedFixtureCredential) {
		return fmt.Errorf("%w: replacement identity or current credentials", errConsumerCheckpoint)
	}

	if s.puts == consumerCompletePUT &&
		(snapshot.driver.IP != "10.0.0.10" || snapshot.driver.Password != rotatedFixtureCredential) {
		return fmt.Errorf("%w: completed IP or current credentials", errConsumerCheckpoint)
	}

	return nil
}

func (s *checkpointAPI) serve(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/api/v1/namespaces/test/secrets/machine" {
		http.NotFound(writer, request)

		return
	}

	writer.Header().Set("Content-Type", "application/json")
	s.mu.Lock()
	defer s.mu.Unlock()

	if request.Method == http.MethodGet {
		encodeCheckpointSecret(writer, s.secret)

		return
	}

	if request.Method != http.MethodPut {
		http.Error(writer, "unexpected method", http.StatusMethodNotAllowed)

		return
	}

	var next checkpointSecret

	err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 4<<20)).Decode(&next)

	var snapshot consumerCheckpoint
	if err == nil {
		snapshot, err = readConsumerCheckpoint(next.Data["extractedConfig"])
	}

	s.puts++
	if err == nil {
		err = s.validate(&snapshot)
	}

	s.secret = next
	switch s.puts {
	case 1:
		s.first = snapshot
		s.checkpoint <- err
	case 2:
		if err != nil {
			s.complete <- err
		}
	case consumerCompletePUT:
		s.complete <- err
		// Withhold the post-Create PUT response until the whole CLI/plugin process group is killed.
		s.mu.Unlock()
		<-request.Context().Done()
		s.mu.Lock()

		return
	default:
		err = fmt.Errorf("%writer: extra Secret PUT", errConsumerCheckpoint)
	}

	if err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)

		return
	}

	encodeCheckpointSecret(writer, next)
}

func encodeCheckpointSecret(w http.ResponseWriter, secret checkpointSecret) {
	err := json.NewEncoder(w).Encode(secret)
	if err != nil {
		http.Error(w, "fixture secret encoding failed", http.StatusInternalServerError)

		return
	}
}

func readConsumerCheckpoint(data []byte) (_ consumerCheckpoint, resultErr error) {
	var result consumerCheckpoint

	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return result, fmt.Errorf("open checkpoint archive: %w", err)
	}

	defer func() { resultErr = errors.Join(resultErr, reader.Close()) }()

	archive := tar.NewReader(io.LimitReader(reader, 4<<20))
	files := map[string][]byte{}

	for {
		header, readErr := archive.Next()
		if errors.Is(readErr, io.EOF) {
			break
		}

		if readErr != nil {
			return result, fmt.Errorf("read checkpoint header: %w", readErr)
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		content, readErr := io.ReadAll(io.LimitReader(archive, 1<<20))
		if readErr != nil {
			return result, fmt.Errorf("read checkpoint file: %w", readErr)
		}

		files[filepath.Base(header.Name)] = content
	}

	var journal struct {
		Phase string `json:"phase"`
		UUID  string `json:"vmUuid"`
		Mode  string `json:"consumerMode"`
	}

	err = json.Unmarshal(files["nutanix-create.json"], &journal)
	if err != nil {
		return result, fmt.Errorf("decode checkpoint journal: %w", err)
	}

	var config struct {
		Driver consumerDriverState `json:"driver"`
	}

	err = json.Unmarshal(files["config.json"], &config)
	if err != nil {
		return result, fmt.Errorf("decode checkpoint driver: %w", err)
	}

	if len(files["id_rsa"]) == 0 || len(files["id_rsa.pub"]) == 0 || journal.Mode != consumerMode {
		return result, fmt.Errorf("%w: missing keys or consumer mode", errConsumerCheckpoint)
	}

	result = consumerCheckpoint{
		key:   sha256.Sum256(bytes.Join([][]byte{files["id_rsa"], files["id_rsa.pub"]}, nil)),
		phase: journal.Phase, uuid: journal.UUID, driver: config.Driver,
	}
	if result.phase == "complete" && config.Driver.VMID != result.uuid {
		return result, fmt.Errorf("%w: VMId differs from allocated UUID", errConsumerCheckpoint)
	}

	return result, nil
}

func runCLISecretRecovery(t *testing.T, client *http.Client, endpoint string) {
	t.Helper()
	requestFixture(t, client, endpoint+"/scenario", `{"scenario":"crash"}`)

	api := &checkpointAPI{
		secret: checkpointSecret{
			APIVersion: "v1", Kind: "Secret",
			Metadata: map[string]string{"name": "machine", "namespace": "test", "resourceVersion": "1"}, Data: nil,
		},
		checkpoint: make(chan error, 1), complete: make(chan error, 2),
	}
	server := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(server.Close)
	paths := consumerInputs(t, server.URL)
	first, firstOutput := startConsumerCLI(t, endpoint, paths.store, paths.kubeconfig, paths.script, "fixture")
	awaitCheckpoint(t, api.checkpoint)
	waitForSubmittedCreate(t, client, endpoint)
	stopConsumerCLI(t, first, firstOutput)
	// Back up only test-owned local state; SecretStore itself extracts the older checkpoint at the same path.
	err := os.Rename(paths.store, paths.store+".interrupted")
	if err != nil {
		t.Fatal(err)
	}

	requestFixture(t, client, endpoint+"/resume", `{}`)
	second, secondOutput := startConsumerCLI(t, endpoint, paths.store, paths.kubeconfig, paths.script,
		rotatedFixtureCredential)
	awaitCheckpoint(t, api.complete)
	stopConsumerCLI(t, second, secondOutput)

	var capture consumerCapture

	err = json.Unmarshal(requestFixture(t, client, endpoint+"/capture", ""), &capture)
	if err != nil {
		t.Fatal(err)
	}

	if capture.Creates != 1 || capture.Allocations != 1 || capture.Deletes != 0 || len(capture.Reads) == 0 {
		t.Fatalf("unexpected recovery counts: %+v", capture)
	}

	for _, id := range capture.Reads {
		if id != expectedRecoveryUUID {
			t.Fatalf("recovery observed unowned UUID: %s", id)
		}
	}

	api.mu.Lock()
	puts := api.puts
	api.mu.Unlock()

	if puts != consumerCompletePUT {
		t.Fatalf("Secret PUTs=%d, want initial/replacement pre-Create then post-Create", puts)
	}

	t.Logf("checkpoint: allocation=%d VM_POST=%d DELETE=%d recovery_GET=%d Secret_PUT=%d; post-Create response withheld",
		capture.Allocations, capture.Creates, capture.Deletes, len(capture.Reads), puts)
}

func consumerInputs(t *testing.T, endpoint string) consumerInputPaths {
	t.Helper()
	directory := t.TempDir()
	store := filepath.Join(directory, "store")

	err := os.Mkdir(store, fixtureDirectoryMode)
	if err != nil {
		t.Fatal(err)
	}

	kubeconfig := filepath.Join(directory, "kubeconfig")
	contents := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: fixture
  cluster:
    server: %s
contexts:
- name: fixture
  context:
    cluster: fixture
    user: fixture
current-context: fixture
users:
- name: fixture
  user: {}
`, endpoint)

	err = os.WriteFile(kubeconfig, []byte(contents), fixtureKeyMode)
	if err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(directory, "install.sh")

	err = os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), fixtureKeyMode)
	if err != nil {
		t.Fatal(err)
	}

	return consumerInputPaths{store: store, kubeconfig: kubeconfig, script: script}
}

func startConsumerCLI(t *testing.T, endpoint, store, kubeconfig, script, credentials string,
) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()

	build, err := filepath.Abs("../../build")
	if err != nil {
		t.Fatal(err)
	}

	host, port, err := net.SplitHostPort(strings.TrimPrefix(endpoint, "https://"))
	if err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(build, "rancher-machine")
	//nolint:gosec // Executable and arguments are fixed fixture-owned build output and local test paths.
	cmd := exec.CommandContext(t.Context(), binary, "-D", "--storage-path", store,
		"--secret-name", "machine", "--secret-namespace", "test", "--kubeconfig", kubeconfig,
		"create", "--driver", "nutanix", "--custom-install-script", script, "--hostname-override", "test-node",
		"--nutanix-endpoint", host, "--nutanix-port", port,
		"--nutanix-username", credentials, "--nutanix-password", credentials,
		"--nutanix-insecure", "--nutanix-cluster", "test-only", "--nutanix-vm-image", "test-only",
		"--nutanix-vm-network", "overlay", "--nutanix-vm-cpus", "2",
		"--nutanix-vm-cores", "2", "--nutanix-vm-mem", "2048", "test")

	cmd.Env = append(os.Environ(), "PATH="+build+":"+os.Getenv("PATH"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = fixtureHTTPTimeout
	output := new(bytes.Buffer)
	cmd.Stdout, cmd.Stderr = output, output

	err = cmd.Start()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			stopConsumerCLI(t, cmd, output)
		}
	})

	return cmd, output
}

func stopConsumerCLI(t *testing.T, cmd *exec.Cmd, output *bytes.Buffer) {
	t.Helper()

	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Error(err)
	}

	err = cmd.Wait()
	if err == nil {
		t.Error("checkpoint CLI unexpectedly exited successfully")
	}

	pattern := regexp.MustCompile(`Plugin server listening at address ([0-9.:]+)`)

	addresses := pattern.FindAllStringSubmatch(output.String(), -1)
	if len(addresses) == 0 {
		t.Errorf("no plugin RPC address recorded: %s", output.String())
	}

	for _, address := range addresses {
		dialer := net.Dialer{Timeout: time.Second}

		conn, dialErr := dialer.DialContext(t.Context(), "tcp", address[1])
		if dialErr == nil {
			closeErr := conn.Close()
			if closeErr != nil {
				t.Error(closeErr)
			}

			t.Errorf("plugin listener survived process-group kill: %s", address[1])
		}
	}
}

func awaitCheckpoint(t *testing.T, signal <-chan error) {
	t.Helper()

	select {
	case err := <-signal:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Minute):
		t.Fatal("CLI checkpoint deadline exceeded")
	}
}
