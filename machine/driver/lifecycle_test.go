package driver //nolint:testpackage // Private polling is tested under synthetic time without public test hooks.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	client "github.com/nutanix-cloud-native/prism-go-client"
	prismv3 "github.com/nutanix-cloud-native/prism-go-client/v3"
)

const (
	lifecycleTestTaskID = "task-id"
	sharedBudgetSeconds = 7
)

type lifecycleTransport func(*http.Request) (*http.Response, error)

func (transport lifecycleTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func lifecycleClient(t *testing.T, transport lifecycleTransport) *prismv3.Client {
	t.Helper()

	conn, err := prismv3.NewV3Client(client.Credentials{
		URL: "https://prism.invalid:9440", Endpoint: "prism.invalid", Username: "fixture", Password: "fixture", Port: "9440",
	}, prismv3.WithRoundTripper(transport))
	if err != nil {
		t.Fatal(err)
	}

	return conn
}

func lifecycleResponse(request *http.Request, code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request,
	}
}

func TestTaskTerminalContract(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		body string
		code int
		want error
	}{
		{name: "success", body: `{"status":"SUCCEEDED"}`, code: http.StatusOK},
		{name: "failed without detail", body: `{"status":"FAILED"}`, code: http.StatusOK, want: errTaskFailed},
		{
			name: "aborted",
			body: `{"status":"ABORTED","error_detail":"sensitive-marker"}`, code: http.StatusOK, want: errTaskFailed,
		},
		{name: "v4 canceled is unknown", body: `{"status":"CANCELED"}`, code: http.StatusOK, want: errInvalidTask},
		{name: "missing status", body: `{}`, code: http.StatusOK, want: errInvalidTask},
		{name: "null task", body: `null`, code: http.StatusOK, want: errInvalidTask},
		{name: "forbidden", body: `{"message":"sensitive-marker"}`, code: http.StatusForbidden},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				conn := lifecycleClient(t, func(request *http.Request) (*http.Response, error) {
					calls++

					return lifecycleResponse(request, testCase.code, testCase.body), nil
				})

				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()

				err := waitForTask(ctx, conn, lifecycleTestTaskID)
				if calls != 1 || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("terminal response polled again: calls=%d err=%v", calls, err)
				}

				assertTaskTerminalResult(t, err, testCase.want, testCase.code)
			})
		})
	}
}

func TestTaskReadRetryBudget(t *testing.T) {
	t.Parallel()

	for _, code := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				conn := lifecycleClient(t, func(request *http.Request) (*http.Response, error) {
					calls++
					if calls == 1 {
						return lifecycleResponse(request, code, `{}`), nil
					}

					return lifecycleResponse(request, http.StatusOK, `{"status":"SUCCEEDED"}`), nil
				})

				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()

				err := waitForTask(ctx, conn, lifecycleTestTaskID)
				if err != nil || calls != 2 {
					t.Fatalf("observation retry: calls=%d err=%v", calls, err)
				}
			})
		})
	}
}

func TestCreateSharedDeadlinePreservesVM(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		start := time.Now()

		ctx, cancel := context.WithTimeout(t.Context(), sharedBudgetSeconds*time.Second)
		defer cancel()

		tasks, deletes := 0, 0
		conn := lifecycleClient(t, func(request *http.Request) (*http.Response, error) {
			if request.Method == http.MethodDelete {
				deletes++
			}

			body := `{"status":{"resources":{"nic_list":[]}}}`

			if strings.Contains(request.URL.Path, "/tasks/") {
				tasks++

				body = `{"status":"RUNNING"}`
				if tasks > 1 {
					body = `{"status":"SUCCEEDED"}`
				}
			}

			return lifecycleResponse(request, http.StatusOK, body), nil
		})
		node := NewDriver("fixture", "")
		response := &prismv3.VMIntentResponse{
			Metadata: &prismv3.Metadata{UUID: new("vm-id")},
			Status:   &prismv3.VMDefStatus{ExecutionContext: &prismv3.ExecutionContext{TaskUUID: lifecycleTestTaskID}},
		}

		err := node.finishCreate(ctx, conn, response, "management-subnet")
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != sharedBudgetSeconds*time.Second ||
			deletes != 0 || node.VMId != "vm-id" {
			t.Fatalf("shared budget/identity: elapsed=%v deletes=%d vm=%s err=%v", time.Since(start), deletes, node.VMId, err)
		}
	})
}

func TestPollingCancellation(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0

		err := poll(ctx, func() (bool, error) {
			calls++

			cancel()

			return true, nil
		})
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("canceled observation reported success: calls=%d err=%v", calls, err)
		}
	})
}

func TestTaskPendingAndThrottlingDeadline(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`{"status":"QUEUED"}`, `{"status":"RUNNING"}`, `{"status":"SUSPENDED"}`, "429"} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				conn := lifecycleClient(t, func(request *http.Request) (*http.Response, error) {
					calls++

					code := http.StatusOK
					if body == "429" {
						code = http.StatusTooManyRequests
					}

					return lifecycleResponse(request, code, body), nil
				})

				start := time.Now()

				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()

				err := waitForTask(ctx, conn, lifecycleTestTaskID)
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != time.Second || calls != 1 {
					t.Fatalf("poll budget: elapsed=%v calls=%d err=%v", time.Since(start), calls, err)
				}
			})
		})
	}
}

func assertTaskTerminalResult(t *testing.T, err, want error, code int) {
	t.Helper()

	if want != nil && !errors.Is(err, want) {
		t.Fatalf("task error = %v, want %v", err, want)
	}

	success := code == http.StatusOK && want == nil

	if (err == nil) != success || err != nil && strings.Contains(err.Error(), "sensitive-marker") {
		t.Fatalf("unexpected success or server detail disclosure: %v", err)
	}
}
