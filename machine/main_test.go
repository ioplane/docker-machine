package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rancher/machine/libmachine/drivers"
	rpcdriver "github.com/rancher/machine/libmachine/drivers/rpc"
	"github.com/rancher/machine/libmachine/mcnflag"
	"github.com/rancher/machine/libmachine/version"
)

const (
	vmNICQueuesField         = "VMNicQueues"
	pluginTestTimeoutSeconds = 15
)

const (
	rpcTestProcessEnv = "NUTANIX_RPC_TEST_PROCESS"
	rpcConfigVersion  = 3
	rpcIOTimeout      = 5 * time.Second
)

// TestMain reuses the compiled test executable when no release binary is supplied.
// The child enters the production main and registers the real driver RPC server.
func TestMain(tests *testing.M) {
	if os.Getenv(rpcTestProcessEnv) == "1" {
		main()

		return
	}

	tests.Run()
}

func TestRPCPluginHandshakeAndFlags(t *testing.T) {
	t.Parallel()

	client := startRPCPlugin(t)

	var apiVersion int

	rpcCall(t, client, "GetVersion", struct{}{}, &apiVersion)

	if apiVersion != 1 || version.ConfigVersion != rpcConfigVersion {
		t.Fatalf("incompatible Rancher machine contract: API=%d config=%d", apiVersion, version.ConfigVersion)
	}

	var name string

	rpcCall(t, client, "DriverName", struct{}{}, &name)

	if name != "nutanix" {
		t.Fatalf("driver name = %q, want nutanix", name)
	}

	assertRPCQueueFlag(t, rpcCreateFlags(t, client))
}

func assertRPCQueueFlag(t *testing.T, flags []mcnflag.Flag) {
	t.Helper()

	for _, flag := range flags {
		if flag.String() != "nutanix-vm-nic-queues" {
			continue
		}

		stringFlag, ok := flag.(*mcnflag.StringFlag)
		if !ok || stringFlag.Value != "" {
			t.Fatalf("queue flag must be an optional StringFlag with empty default: %T", flag)
		}

		return
	}

	t.Fatal("optional nutanix-vm-nic-queues RPC flag missing")
}

func TestRPCPluginConfigFromFlags(t *testing.T) {
	t.Parallel()

	client := startRPCPlugin(t)
	values := rpcFlagValues(t, client)
	values["nutanix-vm-nic-queues"] = "2"

	var options drivers.DriverOptions = &rpcdriver.RPCFlags{Values: values}

	rpcCall(t, client, "SetConfigFromFlags", &options, &struct{}{})

	config := rpcConfig(t, client)
	if string(config[vmNICQueuesField]) != `"2"` {
		t.Fatalf("RPC configuration did not preserve explicit queues: %s", config[vmNICQueuesField])
	}

	for _, invalid := range []string{"0", "-1", "5", "auto", "1.5"} {
		values["nutanix-vm-nic-queues"] = invalid

		err := client.Call("RPCServerDriver.SetConfigFromFlags", &options, &struct{}{})
		if err == nil {
			t.Fatalf("SetConfigFromFlags accepted invalid queue value %q", invalid)
		}
	}
}

func TestRPCPluginLegacyConfigRoundTrip(t *testing.T) {
	t.Parallel()

	client := startRPCPlugin(t)
	legacy := []byte(`{"MachineName":"offline-rpc-fixture","VMId":"legacy-vm-id",
		"VMVCPUs":2,"VMCores":2,"Subnet":["fixture-network"]}`)
	rpcCall(t, client, "SetConfigRaw", legacy, &struct{}{})

	config := rpcConfig(t, client)
	if string(config["VMId"]) != `"legacy-vm-id"` {
		t.Fatal("legacy VMId changed across the RPC configuration round trip")
	}

	if _, present := config[vmNICQueuesField]; present {
		t.Fatal("loading legacy JSON materialized an omitted queue setting")
	}

	config[vmNICQueuesField] = json.RawMessage(`"2"`)

	updated, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("encode queue configuration: %v", err)
	}

	rpcCall(t, client, "SetConfigRaw", updated, &struct{}{})

	roundTrip := rpcConfig(t, client)
	if string(roundTrip["VMId"]) != `"legacy-vm-id"` || string(roundTrip[vmNICQueuesField]) != `"2"` {
		t.Fatal("new queue field or legacy VMId changed across the RPC configuration round trip")
	}
}

func rpcCreateFlags(t *testing.T, client *rpc.Client) []mcnflag.Flag {
	t.Helper()

	var flags []mcnflag.Flag
	rpcCall(t, client, "GetCreateFlags", struct{}{}, &flags)

	return flags
}

func rpcFlagValues(t *testing.T, client *rpc.Client) map[string]any {
	t.Helper()

	values := make(map[string]any)

	for _, flag := range rpcCreateFlags(t, client) {
		value := flag.Default()
		if value == nil {
			value = false
		}

		values[flag.String()] = value
	}

	for _, key := range []string{"nutanix-username", "nutanix-password", "nutanix-cluster", "nutanix-vm-image"} {
		values[key] = "offline-fixture"
	}

	values["nutanix-endpoint"] = "https://prism.invalid"
	values["nutanix-vm-network"] = []string{"fixture-network"}
	values["nutanix-vm-cpus"] = 2
	values["nutanix-vm-cores"] = 2
	values["nutanix-boot-type"] = "legacy"

	return values
}

func rpcConfig(t *testing.T, client *rpc.Client) map[string]json.RawMessage {
	t.Helper()

	var raw []byte
	rpcCall(t, client, "GetConfigRaw", struct{}{}, &raw)

	var config map[string]json.RawMessage

	err := json.Unmarshal(raw, &config)
	if err != nil {
		t.Fatalf("decode RPC configuration: %v", err)
	}

	return config
}

func rpcCall(t *testing.T, client *rpc.Client, method string, args, reply any) {
	t.Helper()

	err := client.Call("RPCServerDriver."+method, args, reply)
	if err != nil {
		t.Fatalf("RPC %s failed: %v", method, err)
	}
}

func rpcExecutable(t *testing.T) string {
	t.Helper()

	executable := os.Getenv("NUTANIX_DRIVER_BINARY")
	if executable == "" {
		var err error

		executable, err = os.Executable()
		if err != nil {
			t.Fatalf("locate compiled test executable: %v", err)
		}
	}

	return executable
}

func startRPCPlugin(t *testing.T) *rpc.Client {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), pluginTestTimeoutSeconds*time.Second)
	t.Cleanup(cancel)
	//nolint:gosec // The test explicitly accepts a prebuilt binary or reexecutes itself.
	command := exec.CommandContext(ctx, rpcExecutable(t))
	command.Env = []string{"MACHINE_PLUGIN_TOKEN=42", "MACHINE_PLUGIN_DRIVER_NAME=nutanix", rpcTestProcessEnv + "=1"}
	command.WaitDelay = time.Second
	command.Stderr = io.Discard

	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("open plugin stdout: %v", err)
	}

	err = command.Start()
	if err != nil {
		t.Fatalf("start plugin process: %v", err)
	}

	t.Cleanup(func() { waitRPCPlugin(t, command, cancel) })

	address := make(chan string, 1)

	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			address <- scanner.Text()

			return
		}

		address <- ""
	}()

	select {
	case endpoint := <-address:
		return connectRPCPlugin(ctx, t, endpoint)
	case <-ctx.Done():
		t.Fatal("plugin address handshake timed out")

		return nil
	}
}

func waitRPCPlugin(t *testing.T, command *exec.Cmd, cancel context.CancelFunc) {
	t.Helper()

	cancel()

	err := command.Wait()
	if err != nil && !strings.Contains(err.Error(), "signal: killed") {
		t.Errorf("wait for plugin process: %v", err)
	}
}

func validateRPCEndpoint(t *testing.T, endpoint string) {
	t.Helper()

	host, _, err := net.SplitHostPort(endpoint)
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("unexpected plugin endpoint %q", endpoint)
	}
}

func connectRPCPlugin(ctx context.Context, t *testing.T, endpoint string) *rpc.Client {
	t.Helper()

	validateRPCEndpoint(t, endpoint)

	var dialer net.Dialer

	dialer.Timeout = rpcIOTimeout

	connection, err := dialer.DialContext(ctx, "tcp", endpoint)
	if err != nil {
		t.Fatalf("connect to plugin RPC: %v", err)
	}

	t.Cleanup(func() {
		closeErr := connection.Close()
		if closeErr != nil {
			t.Errorf("close plugin RPC connection: %v", closeErr)
		}
	})

	err = connection.SetDeadline(time.Now().Add(rpcIOTimeout))
	if err != nil {
		t.Fatalf("set RPC deadline: %v", err)
	}

	connectRPCTransport(t, connection)

	return rpc.NewClient(connection)
}

//nolint:bodyclose // CONNECT hands the live connection to net/rpc, as in rpc.DialHTTPPath.
func connectRPCTransport(t *testing.T, connection net.Conn) {
	t.Helper()

	_, err := io.WriteString(connection, "CONNECT "+rpc.DefaultRPCPath+" HTTP/1.0\n\n")
	if err != nil {
		t.Fatalf("write RPC CONNECT: %v", err)
	}

	var request http.Request

	request.Method = http.MethodConnect

	response, err := http.ReadResponse(bufio.NewReader(connection), &request)
	if err != nil {
		t.Fatalf("read RPC CONNECT: %v", err)
	}

	if response.StatusCode != http.StatusOK {
		t.Fatalf("RPC CONNECT status = %s", response.Status)
	}
}
