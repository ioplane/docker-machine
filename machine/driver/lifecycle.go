package driver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	client "github.com/nutanix-cloud-native/prism-go-client"
	prismv3 "github.com/nutanix-cloud-native/prism-go-client/v3"
)

func (d *NutanixDriver) operationContext() (context.Context, context.CancelFunc, error) {
	if d.Timeout <= 0 || int64(d.Timeout) > math.MaxInt64/int64(time.Second) {
		return nil, nil, fmt.Errorf("%w: nutanix-timeout must fit a positive duration in seconds", errDriverConfiguration)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(d.Timeout)*time.Second)

	return ctx, cancel, nil
}

func waitForTask(ctx context.Context, conn *prismv3.Client, taskUUID string) error {
	return poll(ctx, func() (bool, error) {
		task, err := conn.V3.GetTask(ctx, taskUUID)
		if err != nil {
			return false, fmt.Errorf("get task %s: %w", taskUUID, err)
		}

		if task == nil || task.Status == nil {
			return false, errInvalidTask
		}

		// The v3 task schema has ABORTED; v4 CANCELED is a different contract.
		switch *task.Status {
		case taskSucceeded:
			return true, nil
		case "FAILED", "ABORTED":
			return false, fmt.Errorf("%w: task %s (%s)", errTaskFailed, taskUUID, *task.Status)
		case "QUEUED", "RUNNING", "SUSPENDED":
			return false, nil
		default:
			return false, errInvalidTask
		}
	})
}

// poll retries only observations; mutation submission never passes through this loop.
func poll(ctx context.Context, observe func() (bool, error)) error {
	for {
		err := ctx.Err()
		if err != nil {
			return fmt.Errorf(driverOperationFormat, err)
		}

		done, err := observe()

		ctxErr := ctx.Err()
		if ctxErr != nil {
			return fmt.Errorf(driverOperationFormat, ctxErr)
		}

		delay := taskPollSeconds * time.Second

		if err != nil {
			if !transientObservation(err) {
				return err
			}

			delay += rand.N(delay) //nolint:gosec // Retry jitter does not require cryptographic randomness.
		} else if done {
			return nil
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()

			return fmt.Errorf(driverOperationFormat, ctx.Err())
		case <-timer.C:
		}
	}
}

func responseStatus(err error) int {
	if response, ok := errors.AsType[*client.ResponseError](err); ok {
		return response.StatusCode
	}

	return 0
}

func transientObservation(err error) bool {
	switch responseStatus(err) {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func vmAbsent(ctx context.Context, conn *prismv3.Client, vmUUID string) (bool, error) {
	response, err := conn.V3.GetVM(ctx, vmUUID)

	ctxErr := ctx.Err()
	if ctxErr != nil {
		return false, fmt.Errorf("observe VM deletion: %w", ctxErr)
	}

	if err != nil {
		if responseStatus(err) == http.StatusNotFound {
			return true, nil
		}

		return false, fmt.Errorf("observe VM deletion: %w", err)
	}

	if response == nil || response.Metadata == nil || response.Metadata.UUID == nil ||
		!strings.EqualFold(*response.Metadata.UUID, vmUUID) {
		return false, errInvalidVM
	}

	return false, nil
}
