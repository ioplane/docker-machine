package driver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rancher/machine/libmachine/drivers"
)

const consumerRecoveryMode = "ssh-custom-script/v1"

// PrepareCreateRecovery validates current intent without granting permission to submit a VM.
func (d *NutanixDriver) PrepareCreateRecovery(opts drivers.DriverOptions, request drivers.CreateRecoveryContext,
) (_ bool, result error) {
	if request.CustomInstallScriptPath == "" {
		return false, nil
	}

	if !request.Existing {
		return false, d.setConsumerCreateBinding(request)
	}

	// Current flags are untrusted until the journal proves a supported pending operation.
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout*time.Second)
	defer cancel()

	store, err := d.loadCreateJournal(ctx)
	if err != nil {
		return false, err
	}
	defer func() { result = errors.Join(result, store.close()) }()

	journal := store.journal
	if journal == nil || journal.ConsumerMode != consumerRecoveryMode || journal.VMUUID == "" ||
		journal.Phase == phaseComplete || journal.Phase == phaseRemoved {
		return false, nil
	}

	if d.VMId != "" && !strings.EqualFold(d.VMId, journal.VMUUID) {
		return false, errCreateRecovery
	}

	err = d.SetConfigFromFlags(opts)
	if err != nil {
		return false, err
	}

	err = d.setConsumerCreateBinding(request)
	if err != nil {
		return false, err
	}

	binding, err := d.createBinding()
	if err != nil {
		return false, err
	}

	err = journal.validate(binding)
	if err != nil {
		return false, err
	}

	return true, nil
}

func (d *NutanixDriver) setConsumerCreateBinding(request drivers.CreateRecoveryContext) error {
	script, err := os.ReadFile(request.CustomInstallScriptPath)
	if err != nil {
		return fmt.Errorf("read custom install script for Create recovery: %w", err)
	}

	hostname := request.HostnameOverride
	if hostname == "" {
		hostname = d.MachineName
	}

	scriptDigest := sha256.Sum256(script)
	intent := consumerRecoveryMode + "\x00" + hostname + "\x00" + hex.EncodeToString(scriptDigest[:])
	digest := sha256.Sum256([]byte(intent))
	d.ConsumerCreateBinding = hex.EncodeToString(digest[:])

	return nil
}
