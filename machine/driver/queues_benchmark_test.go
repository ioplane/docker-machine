package driver_test

import (
	"encoding/json"
	"testing"

	"github.com/nutanix/docker-machine/machine/driver"
)

type benchmarkQueueOptions struct{ queueOptions }

const benchmarkTimeout = 300

func (o benchmarkQueueOptions) Int(key string) int {
	if key == "nutanix-timeout" {
		return benchmarkTimeout
	}

	return o.queueOptions.Int(key)
}

func BenchmarkQueueConfiguration(b *testing.B) {
	for _, queues := range []string{"", "2"} {
		b.Run("queues="+queues, func(b *testing.B) {
			options := benchmarkQueueOptions{queueOptions{queues: queues, sockets: 2, cores: 2}}

			b.ReportAllocs()

			for b.Loop() {
				nodeDriver := driver.NewDriver("fixture", "")
				// shortcut: preset timeout to exclude cold-path logging; validate defaults in the input-contract task.
				nodeDriver.Timeout = benchmarkTimeout

				err := nodeDriver.SetConfigFromFlags(options)
				if err != nil {
					b.Fatal(err)
				}

				_, err = json.Marshal(nodeDriver) //nolint:gosec // G117: persisted RPC state contains fixture-only credentials.
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
