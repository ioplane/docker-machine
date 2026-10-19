package driver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	prismv3 "github.com/nutanix-cloud-native/prism-go-client/v3"
)

const (
	powerOn    = "ON"
	powerOff   = "OFF"
	vmComplete = "COMPLETE"
)

func TestPowerDeadlineAndPostcondition(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name        string
		observed    string
		hangBody    bool
		wantErr     bool
		submissions int64
	}{
		{name: "observed power", observed: powerOn, submissions: 1},
		{name: "task success without power transition", observed: powerOff, wantErr: true, submissions: 1},
		{name: "stalled HTTP body", hangBody: true, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var submissions atomic.Int64

			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if testCase.hangBody {
					stallPowerBody(t, writer, request)

					return
				}

				response := powerFixtureResponse(request, testCase.observed, &submissions)

				err := json.NewEncoder(writer).Encode(response)
				if err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)

			node := payloadDriver(t, server, "")
			node.Timeout = 1
			node.VMId = "vm-id"

			err := node.Start()
			if (err != nil) != testCase.wantErr || testCase.wantErr && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("power postcondition error = %v, want deadline = %v", err, testCase.wantErr)
			}

			if got := submissions.Load(); got != testCase.submissions {
				t.Fatalf("power submission count = %d, want %d", got, testCase.submissions)
			}
		})
	}
}

func TestPowerStateNoop(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name, target                  string
		desired, observed, completion *string
		attempts                      int
		submissions                   int64
	}{
		{"already ON", powerOn, new(powerOn), new(powerOn), new(vmComplete), 2, 0},
		{"already OFF", powerOff, new(powerOff), new(powerOff), new(vmComplete), 2, 0},
		{"conflicting intent", powerOn, new(powerOff), new(powerOn), new(vmComplete), 1, 1},
		{"observed transition", powerOn, new(powerOn), new(powerOff), new(vmComplete), 1, 1},
		{"pending", powerOn, new(powerOn), new(powerOn), new("PENDING"), 1, 1},
		{"unknown state", powerOn, new(powerOn), new(powerOn), new("UNKNOWN"), 1, 1},
		{"missing state", powerOn, new(powerOn), new(powerOn), nil, 1, 1},
		{"missing desired power", powerOn, nil, new(powerOn), new(vmComplete), 1, 1},
		{"missing observed power", powerOn, new(powerOn), nil, new(vmComplete), 1, 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var submissions atomic.Int64

			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				response := powerFixtureResponse(request, testCase.target, &submissions)
				if request.Method == http.MethodGet && request.URL.Path == "/api/nutanix/v3/vms/vm-id" && submissions.Load() == 0 {
					response = &prismv3.VMIntentResponse{
						Metadata: &prismv3.Metadata{UUID: new("VM-ID")},
						Spec:     &prismv3.VM{Resources: &prismv3.VMResources{PowerState: testCase.desired}},
						Status: &prismv3.VMDefStatus{
							State:     testCase.completion,
							Resources: &prismv3.VMResourcesDefStatus{PowerState: testCase.observed},
						},
					}
				}

				err := json.NewEncoder(writer).Encode(response)
				if err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)

			node := payloadDriver(t, server, "")
			node.VMId = "vm-id"

			operation := node.Start
			if testCase.target == powerOff {
				operation = node.Stop
			}

			for range testCase.attempts {
				err := operation()
				if err != nil {
					t.Fatal(err)
				}
			}

			if got := submissions.Load(); got != testCase.submissions {
				t.Fatalf("power submission count = %d, want %d", got, testCase.submissions)
			}
		})
	}
}

func TestPowerResponseIdentity(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		uuid *string
	}{
		{name: "missing"},
		{name: "empty", uuid: new("")},
		{name: "foreign", uuid: new("foreign-vm")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var submissions atomic.Int64

			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				response := powerFixtureResponse(request, powerOn, &submissions)
				if request.Method == http.MethodGet && request.URL.Path == "/api/nutanix/v3/vms/vm-id" {
					response = &prismv3.VMIntentResponse{
						Metadata: &prismv3.Metadata{UUID: testCase.uuid},
						Spec:     &prismv3.VM{Resources: &prismv3.VMResources{PowerState: new(powerOn)}},
						Status: &prismv3.VMDefStatus{
							State:     new(vmComplete),
							Resources: &prismv3.VMResourcesDefStatus{PowerState: new(powerOn)},
						},
					}
				}

				err := json.NewEncoder(writer).Encode(response)
				if err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)

			node := payloadDriver(t, server, "")
			node.VMId = "vm-id"

			err := node.Start()
			if err == nil || submissions.Load() != 0 {
				t.Fatalf("invalid response identity: err=%v submissions=%d", err, submissions.Load())
			}
		})
	}
}

func TestRestartPowerTransitions(t *testing.T) {
	t.Parallel()

	for _, initial := range []string{powerOn, powerOff} {
		t.Run(initial, func(t *testing.T) {
			t.Parallel()

			var submissions atomic.Int64

			powers := make(chan string, 2)
			server := httptest.NewTLSServer(restartPowerHandler(t, initial, powers, &submissions))
			t.Cleanup(server.Close)

			node := payloadDriver(t, server, "")
			node.VMId = "vm-id"

			err := node.Restart()
			if err != nil {
				t.Fatal(err)
			}

			want := []string{powerOff, powerOn}
			if initial == powerOff {
				want = want[1:]
			}

			if got := submissions.Load(); got != int64(len(want)) {
				t.Fatalf("Restart submissions = %d, want %d", got, len(want))
			}

			for _, expected := range want {
				if got := <-powers; got != expected {
					t.Fatalf("Restart power = %s, want %s", got, expected)
				}
			}
		})
	}
}

func restartPowerHandler(t *testing.T, initial string, powers chan<- string, submissions *atomic.Int64) http.Handler {
	t.Helper()

	var power atomic.Pointer[string]
	power.Store(new(initial))

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPut {
			var input prismv3.VMIntentInput

			err := json.NewDecoder(request.Body).Decode(&input)
			if err != nil {
				t.Error(err)

				return
			}

			power.Store(input.Spec.Resources.PowerState)

			select {
			case powers <- *power.Load():
			default:
				t.Error("unexpected extra power PUT")
			}
		}

		response := powerFixtureResponse(request, *power.Load(), submissions)
		if request.Method == http.MethodGet && request.URL.Path == "/api/nutanix/v3/vms/vm-id" {
			response = &prismv3.VMIntentResponse{
				Metadata: &prismv3.Metadata{UUID: new("vm-id")},
				Spec:     &prismv3.VM{Resources: &prismv3.VMResources{PowerState: power.Load()}},
				Status: &prismv3.VMDefStatus{
					State:     new(vmComplete),
					Resources: &prismv3.VMResourcesDefStatus{PowerState: power.Load()},
				},
			}
		}

		err := json.NewEncoder(writer).Encode(response)
		if err != nil {
			t.Error(err)
		}
	})
}

func powerFixtureResponse(request *http.Request, observed string, submissions *atomic.Int64) any {
	var response any

	switch {
	case request.Method == http.MethodPut:
		submissions.Add(1)

		response = &prismv3.VMIntentResponse{Status: &prismv3.VMDefStatus{
			ExecutionContext: &prismv3.ExecutionContext{TaskUUID: "task-id"},
		}}
	case request.URL.Path == "/api/nutanix/v3/tasks/task-id":
		response = &prismv3.TasksResponse{Status: new("SUCCEEDED")}
	default:
		response = &prismv3.VMIntentResponse{
			Metadata: &prismv3.Metadata{UUID: new("vm-id")},
			Spec:     &prismv3.VM{Resources: &prismv3.VMResources{PowerState: new(powerOff)}},
			Status:   &prismv3.VMDefStatus{Resources: &prismv3.VMResourcesDefStatus{PowerState: new(observed)}},
		}
	}

	return response
}

func stallPowerBody(t *testing.T, writer http.ResponseWriter, request *http.Request) {
	t.Helper()

	_, err := writer.Write([]byte(`{"status":`))
	if err != nil {
		t.Error(err)
	}

	err = http.NewResponseController(writer).Flush()
	if err != nil {
		t.Error(err)
	}

	<-request.Context().Done()
}
