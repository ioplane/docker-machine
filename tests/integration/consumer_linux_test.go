package integration_test

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
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

	"github.com/nutanix/docker-machine/machine/driver"
	machinehost "github.com/rancher/machine/libmachine/host"
	"github.com/rancher/machine/libmachine/persist"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

const (
	consumerCompletePUT      = 3
	rotatedFixtureCredential = "rotated"
	consumerMode             = "ssh-custom-script/v1"
	checkpointNamespace      = "test"
	checkpointMachineName    = "test"
	checkpointSecretName     = "machine"
)

var errConsumerCheckpoint = errors.New("invalid consumer checkpoint")

type consumerDriverState struct {
	VMID              string `json:"vmId"`
	IP                string `json:"ipAddress"`
	User              string `json:"username"`
	Password          string `json:"password"`
	Binding           string `json:"consumerCreateBinding"`
	ManagementNetwork string `json:"vmManagementNetwork"`
	Queues            string `json:"vmNicQueues"`
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
	secret     corev1.Secret
	puts       int
	first      consumerCheckpoint
	checkpoint chan error
	complete   chan error
	wireTypes  []string
}

func (s *checkpointAPI) validate(snapshot *consumerCheckpoint) error {
	phase := "reserved"
	if s.puts == consumerCompletePUT {
		phase = "complete"
	}

	if snapshot.phase != phase || snapshot.uuid != expectedRecoveryUUID || snapshot.driver.Binding == "" {
		return fmt.Errorf("%w: phase/UUID/proof", errConsumerCheckpoint)
	}

	if snapshot.driver.ManagementNetwork != fixtureOverlayUUID || snapshot.driver.Queues != "2" {
		return fmt.Errorf("%w: persisted management selector or queues", errConsumerCheckpoint)
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
		encodeCheckpointSecret(writer, &s.secret)

		return
	}

	if request.Method != http.MethodPut {
		http.Error(writer, "unexpected method", http.StatusMethodNotAllowed)

		return
	}

	s.wireTypes = append(s.wireTypes, request.Header.Get("Content-Type"))
	next, err := decodeCheckpointSecret(writer, request)

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
		err = fmt.Errorf("%w: extra Secret PUT", errConsumerCheckpoint)
	}

	if err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)

		return
	}

	encodeCheckpointSecret(writer, &next)
}

func encodeCheckpointSecret(w http.ResponseWriter, secret *corev1.Secret) {
	err := json.NewEncoder(w).Encode(secret)
	if err != nil {
		http.Error(w, "fixture secret encoding failed", http.StatusInternalServerError)

		return
	}
}

func decodeCheckpointSecret(writer http.ResponseWriter, request *http.Request) (corev1.Secret, error) {
	var secret corev1.Secret

	data, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 4<<20))
	if err == nil {
		scheme := runtime.NewScheme()
		scheme.AddKnownTypes(corev1.SchemeGroupVersion, &corev1.Secret{})
		_, _, err = serializer.NewCodecFactory(scheme).UniversalDeserializer().Decode(data, nil, &secret)
	}

	if err != nil {
		return secret, fmt.Errorf("decode Secret PUT (Content-Type %q): %w", request.Header.Get("Content-Type"), err)
	}

	return secret, nil
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
		secret: corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: checkpointSecretName, Namespace: checkpointNamespace, ResourceVersion: "1"},
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
		"--secret-name", checkpointSecretName, "--secret-namespace", checkpointNamespace, "--kubeconfig", kubeconfig,
		"create", "--driver", "nutanix", "--custom-install-script", script, "--hostname-override", "test-node",
		"--nutanix-endpoint", host, "--nutanix-port", port,
		"--nutanix-username", credentials, "--nutanix-password", credentials,
		"--nutanix-insecure", "--nutanix-cluster", "test-only", "--nutanix-vm-image", "test-only",
		"--nutanix-vm-network", "overlay", "--nutanix-vm-cpus", "2",
		"--nutanix-vm-management-network", fixtureOverlayUUID, "--nutanix-vm-nic-queues", "2",
		"--nutanix-vm-cores", "2", "--nutanix-vm-mem", "2048", checkpointMachineName)

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

	ctx, cancel := context.WithTimeout(context.Background(), fixtureHTTPTimeout)
	defer cancel()

	dialer := net.Dialer{Timeout: time.Second}

	for _, address := range addresses {
		probeErr := waitForPluginListener(ctx, address[1], dialer.DialContext)
		if probeErr != nil {
			t.Errorf("plugin listener did not close after process-group kill: %s: %v", address[1], probeErr)
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

func TestSecretStoreCheckpointWire(t *testing.T) {
	t.Parallel()

	api := &checkpointAPI{
		secret: corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: checkpointSecretName, Namespace: checkpointNamespace, ResourceVersion: "1"},
		},
		checkpoint: make(chan error, 1), complete: make(chan error, 2),
	}
	server := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(server.Close)
	paths := consumerInputs(t, server.URL)
	machine := filepath.Join(paths.store, "machines", checkpointMachineName)

	err := os.MkdirAll(machine, fixtureDirectoryMode)
	if err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"nutanix-create.json": `{"phase":"reserved","vmUuid":"` + expectedRecoveryUUID +
			`","consumerMode":"` + consumerMode + `"}`,
		"id_rsa": "fixture-private", "id_rsa.pub": "fixture-public",
	}
	for name, content := range files {
		err = os.WriteFile(filepath.Join(machine, name), []byte(content), fixtureKeyMode)
		if err != nil {
			t.Fatal(err)
		}
	}

	store, err := persist.NewSecretStore(persist.NewFilestore(paths.store, "", ""),
		checkpointSecretName, checkpointNamespace, paths.kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	node := driver.NewDriver(checkpointMachineName, paths.store)
	node.ConsumerCreateBinding = "fixture-proof"
	node.VMManagementNetwork, node.VMNicQueues = fixtureOverlayUUID, "2"
	err = store.Save(&machinehost.Host{Name: checkpointMachineName, DriverName: "nutanix", Driver: node})

	api.mu.Lock()
	wireTypes := append([]string(nil), api.wireTypes...)
	api.mu.Unlock()
	t.Logf("Secret PUT Content-Type: %q", wireTypes)

	if len(wireTypes) != 1 || wireTypes[0] != runtime.ContentTypeProtobuf {
		t.Fatalf("actual SecretStore did not exercise protobuf PUT: %q", wireTypes)
	}

	var checkpointErr error
	select {
	case checkpointErr = <-api.checkpoint:
	default:
		t.Fatal("Secret PUT did not notify checkpoint handler")
	}

	if err != nil {
		t.Fatalf("actual SecretStore Save: %v; checkpoint decoder: %v", err, checkpointErr)
	}

	if checkpointErr != nil {
		t.Fatal(checkpointErr)
	}
}

func waitForPluginListener(ctx context.Context, address string,
	dial func(context.Context, string, string) (net.Conn, error),
) error {
	ticker := time.NewTicker(workerPoll)
	defer ticker.Stop()

	for {
		err := ctx.Err()
		if err != nil {
			return fmt.Errorf("wait for plugin listener closure: %w", err)
		}

		connection, dialErr := dial(ctx, "tcp", address)
		if dialErr == nil {
			closeErr := connection.Close()
			if closeErr != nil {
				return fmt.Errorf("close plugin probe: %w", closeErr)
			}
		}

		err = ctx.Err()
		if err != nil {
			return fmt.Errorf("wait for plugin listener closure: %w", err)
		}

		if errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil
		}

		if dialErr != nil {
			return fmt.Errorf("probe plugin listener: %w", dialErr)
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("wait for plugin listener closure: %w", ctx.Err())
		}
	}
}

func TestPluginListenerWait(t *testing.T) {
	t.Parallel()

	for _, expected := range []error{nil, context.Canceled} {
		name := "asynchronous exit"
		if expected != nil {
			name = "live listener cancellation"
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runPluginListenerWait(t, expected)
		})
	}

	t.Run("unrelated probe error", func(t *testing.T) {
		t.Parallel()

		err := waitForPluginListener(t.Context(), "127.0.0.1:0",
			func(context.Context, string, string) (net.Conn, error) { return nil, errConsumerCheckpoint })
		if !errors.Is(err, errConsumerCheckpoint) {
			t.Fatalf("unrelated dial error was hidden: %v", err)
		}
	})
}

func runPluginListenerWait(t *testing.T, expected error) {
	t.Helper()
	command, address := startPluginListener(t)

	ctx, cancel := context.WithTimeout(t.Context(), fixtureHTTPTimeout)
	defer cancel()

	observed, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)

	var first sync.Once

	dialer := net.Dialer{Timeout: time.Second}

	probe := func(probeContext context.Context, network, endpoint string) (net.Conn, error) {
		connection, err := dialer.DialContext(probeContext, network, endpoint)
		if err != nil {
			return nil, fmt.Errorf("fixture plugin probe: %w", err)
		}

		first.Do(func() {
			close(observed)

			select {
			case <-release:
			case <-ctx.Done():
			}
		})

		return connection, nil
	}
	go func() { result <- waitForPluginListener(ctx, address.String(), probe) }()

	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("actual plugin listener was not probed")
	}

	if expected != nil {
		cancel()
	} else {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if err != nil {
			t.Fatal(err)
		}

		err = command.Wait()
		if err == nil {
			t.Fatal("SIGKILL plugin exited successfully")
		}
	}

	close(release)

	err := <-result
	if !errors.Is(err, expected) {
		t.Fatalf("listener closure result: %v, want %v", err, expected)
	}
}

func startPluginListener(t *testing.T) (*exec.Cmd, *net.TCPAddr) {
	t.Helper()

	binary, err := filepath.Abs("../../build/docker-machine-driver-nutanix")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), fixtureHTTPTimeout)
	t.Cleanup(cancel)
	//nolint:gosec // Execute only the actual plugin built by the declared integration build target.
	command := exec.CommandContext(ctx, binary)

	command.Env = append(os.Environ(), "MACHINE_PLUGIN_TOKEN=42")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = fixtureHTTPTimeout

	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	err = command.Start()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if command.ProcessState == nil {
			killErr := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			if killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
				t.Error(killErr)
			}

			waitErr := command.Wait()
			if waitErr == nil {
				t.Error("killed fixture plugin exited successfully")
			}
		}
	})

	group, err := syscall.Getpgid(command.Process.Pid)
	if err != nil || group != command.Process.Pid {
		t.Fatalf("fixture plugin PID=%d PGID=%d: %v", command.Process.Pid, group, err)
	}

	scanner := bufio.NewScanner(output)
	if !scanner.Scan() {
		t.Fatalf("actual plugin did not publish its listener: %v", scanner.Err())
	}

	t.Logf("test-owned plugin PID=%d PGID=%d", command.Process.Pid, group)

	address, err := net.ResolveTCPAddr("tcp", scanner.Text())
	if err != nil || !address.IP.IsLoopback() {
		t.Fatalf("plugin did not publish a loopback listener: %v", err)
	}

	return command, address
}
