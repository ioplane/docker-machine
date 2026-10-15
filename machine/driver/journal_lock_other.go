//go:build !linux

package driver

import (
	"context"
	"fmt"
	"os"
)

func lockJournal(context.Context, *os.Root) (*os.File, error) {
	return nil, fmt.Errorf("%w: durable operations require Linux", errDriverConfiguration)
}

func validateJournalOwner(os.FileInfo) error {
	return fmt.Errorf("%w: durable operations require Linux", errDriverConfiguration)
}
