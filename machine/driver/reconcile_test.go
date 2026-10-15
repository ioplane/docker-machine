package driver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	prismv3 "github.com/nutanix-cloud-native/prism-go-client/v3"
)

func TestCreateKnownVMIdentityBeforeAPI(t *testing.T) {
	t.Parallel()

	fixture := newPayloadFixture(t, "")
	node := payloadDriver(t, fixture.server, "")
	decodePayloadJSON(t, []byte(`{"VMId":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}`), node)

	err := node.Create()
	if err == nil || fixture.calls.Load() != 0 {
		t.Fatalf("Create resubmitted known VM identity: requests=%d err=%v", fixture.calls.Load(), err)
	}
}

type removalCase struct {
	name         string
	observedUUID string
	readCode     int
	deleteCode   int
	taskStatus   string
	presentAfter bool
	wantDeletes  int64
	wantError    bool
}

func TestRemoveReconciliation(t *testing.T) {
	t.Parallel()

	for _, testCase := range []removalCase{
		{name: "already absent", readCode: http.StatusNotFound, deleteCode: http.StatusNotFound},
		{name: "forbidden observation", readCode: http.StatusForbidden, deleteCode: http.StatusForbidden, wantError: true},
		{name: "deleted", readCode: http.StatusOK, deleteCode: http.StatusOK, taskStatus: "SUCCEEDED", wantDeletes: 1},
		{name: "failed but absent", readCode: http.StatusOK, deleteCode: http.StatusOK, taskStatus: "FAILED", wantDeletes: 1},
		{
			name: "failed and present", readCode: http.StatusOK, deleteCode: http.StatusOK,
			taskStatus: "FAILED", presentAfter: true, wantDeletes: 1, wantError: true,
		},
		{
			name: "task success but present", readCode: http.StatusOK, deleteCode: http.StatusOK,
			taskStatus: "SUCCEEDED", presentAfter: true, wantDeletes: 1, wantError: true,
		},
		{
			name: "forbidden mutation", readCode: http.StatusOK, deleteCode: http.StatusForbidden,
			wantDeletes: 1, wantError: true,
		},
		{
			name: "mismatched VM identity", readCode: http.StatusOK, observedUUID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
			wantError: true,
		},
		{name: "delete404 and absent", readCode: http.StatusOK, deleteCode: http.StatusNotFound, wantDeletes: 1},
		{
			name: "delete404 but present", readCode: http.StatusOK, deleteCode: http.StatusNotFound,
			presentAfter: true, wantDeletes: 1, wantError: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var deletes atomic.Int64

			server := removalFixture(t, &testCase, &deletes)
			node := payloadDriver(t, server, "")
			node.VMId = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
			node.Timeout = 1

			err := node.Remove()
			if (err != nil) != testCase.wantError || deletes.Load() != testCase.wantDeletes {
				t.Fatalf("Remove: deletes=%d want=%d err=%v", deletes.Load(), testCase.wantDeletes, err)
			}

			if !testCase.wantError {
				err = node.Remove()
				if err != nil || deletes.Load() != testCase.wantDeletes {
					t.Fatalf("repeated Remove submitted mutation: deletes=%d err=%v", deletes.Load(), err)
				}
			}
		})
	}
}

func removalFixture(t *testing.T, testCase *removalCase, deletes *atomic.Int64) *httptest.Server {
	t.Helper()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var response any

		switch {
		case request.Method == http.MethodDelete:
			deletes.Add(1)
			writer.WriteHeader(testCase.deleteCode)

			response = &prismv3.DeleteResponse{Status: &prismv3.DeleteStatus{
				ExecutionContext: &prismv3.ExecutionContext{TaskUUID: "task-id"},
			}}
		case request.URL.Path == "/api/nutanix/v3/tasks/task-id":
			response = &prismv3.TasksResponse{Status: new(testCase.taskStatus)}
		default:
			code := testCase.readCode
			if deletes.Load() > 0 && !testCase.presentAfter {
				code = http.StatusNotFound
			}

			writer.WriteHeader(code)

			identity := testCase.observedUUID
			if identity == "" {
				identity = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
			}

			response = &prismv3.VMIntentResponse{Metadata: &prismv3.Metadata{
				UUID: &identity,
			}}
		}

		writeRemovalResponse(t, writer, response)
	}))
	t.Cleanup(server.Close)

	return server
}

func writeRemovalResponse(t *testing.T, writer http.ResponseWriter, response any) {
	t.Helper()

	err := json.NewEncoder(writer).Encode(response)
	if err != nil {
		t.Error(err)
	}
}
