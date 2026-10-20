package utils_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	machinetests "github.com/nutanix/docker-machine/machine/tests"
	"github.com/nutanix/docker-machine/utils"
)

func TestMachineConfigValueMethodSet(t *testing.T) {
	t.Parallel()

	configType := reflect.TypeFor[machinetests.MachineConfig]()
	for _, methodName := range []string{
		"CreateMachine", "DeleteMachine", "StartMachine", "StopMachine",
	} {
		_, available := configType.MethodByName(methodName)
		if !available {
			t.Errorf("upstream value method %s is missing", methodName)
		}
	}
}

func TestToJSONStringMarshalFailure(t *testing.T) {
	t.Parallel()

	actual := utils.ToJSONString(func() {})
	if actual != "null" || !json.Valid([]byte(actual)) {
		t.Fatal("marshal failure must return JSON null")
	}
}

//nolint:paralleltest // Captures the process-global logger and stdout.
func TestDiagnosticsDoNotExposePayload(t *testing.T) {
	const secret = "private-diagnostic-sentinel"

	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://example.invalid/?token="+secret, strings.NewReader(secret))
	if err != nil {
		t.Fatal(err)
	}

	request.SetBasicAuth("dummy", secret)
	request.Header.Set("X-Ntnx-Api-Key", secret)
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Set-Cookie": []string{secret}},
		Body:       io.NopCloser(strings.NewReader(secret)),
		Request:    request,
	}

	output := captureDiagnostics(t, func() {
		utils.PrintToJSON(map[string]string{"password": secret}, secret)
		utils.DebugRequest(request)
		utils.DebugResponse(response)
	})
	if strings.Contains(output, secret) {
		t.Fatal("diagnostic output contains a credential or payload")
	}
}

func TestMachineHelperDoesNotPrintCredentials(t *testing.T) {
	const (
		secret                = "private-helper-sentinel"
		executableFixtureMode = 0o700
	)

	directory := t.TempDir()

	binary := filepath.Join(directory, "docker-machine")

	writeErr := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), executableFixtureMode)
	if writeErr != nil {
		t.Fatal(writeErr)
	}

	t.Setenv("PATH", directory)

	config := machinetests.MachineConfig{
		MachineName: "dummy-machine",
		NutanixUser: "dummy-user",
		NutanixPass: secret,
		SSHPass:     secret,
		Endpoint:    "example.invalid",
	}

	output := captureDiagnostics(t, func() {
		_, err := config.CreateMachine()
		if err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(output, secret) {
		t.Fatal("machine helper output contains credentials")
	}
}

func captureDiagnostics(t *testing.T, action func()) string {
	t.Helper()

	var messages bytes.Buffer

	oldWriter := log.Writer()

	log.SetOutput(&messages)
	t.Cleanup(func() { log.SetOutput(oldWriter) })

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	os.Stdout = writer

	t.Cleanup(func() { os.Stdout = oldStdout })

	action()

	writerCloseErr := writer.Close()
	if writerCloseErr != nil {
		t.Fatal(writerCloseErr)
	}

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}

	readerCloseErr := reader.Close()
	if readerCloseErr != nil {
		t.Fatal(readerCloseErr)
	}

	return messages.String() + string(output)
}
