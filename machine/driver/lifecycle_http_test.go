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

func TestPowerDeadlineAndPostcondition(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name        string
		observed    string
		hangBody    bool
		wantErr     bool
		submissions int64
	}{
		{name: "observed power", observed: "ON", submissions: 1},
		{name: "task success without power transition", observed: "OFF", wantErr: true, submissions: 1},
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
			Spec:     &prismv3.VM{Resources: &prismv3.VMResources{PowerState: new("OFF")}},
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
