package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

const (
	journalLockPoll              = 10 * time.Millisecond
	journalOtherWritePermissions = 0o022
)

// A persistent inode plus a kernel lock survives stale lock files and releases on process exit.
func lockJournal(ctx context.Context, root *os.Root) (*os.File, error) {
	file, err := root.OpenFile("nutanix-operation.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, journalMode)
	if err != nil {
		return nil, fmt.Errorf("open machine operation lock: %w", err)
	}

	info, err := file.Stat()
	if err == nil {
		err = validateJournalOwner(info)
	}

	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&journalPublicPermissions != 0 {
		return nil, errors.Join(errCreateRecovery, err, file.Close())
	}

	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}

		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return nil, errors.Join(err, file.Close())
		}

		timer := time.NewTimer(journalLockPoll)
		select {
		case <-ctx.Done():
			timer.Stop()

			return nil, errors.Join(ctx.Err(), file.Close())
		case <-timer.C:
		}
	}
}

func validateJournalOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Geteuid()) || info.Mode().Perm()&journalOtherWritePermissions != 0 {
		return fmt.Errorf("%w: journal directory or file has unsafe ownership or permissions", errCreateRecovery)
	}

	if info.Mode().IsRegular() && stat.Nlink != 1 {
		return fmt.Errorf("%w: journal file has multiple hard links", errCreateRecovery)
	}

	return nil
}
