package driver

import (
	"context"
	"errors"
	"strings"

	prismv3 "github.com/nutanix-cloud-native/prism-go-client/v3"
)

type preparedCreate struct {
	clientID string
	binding  string
	request  *prismv3.VMIntentInput
	conn     *prismv3.Client
}

// PreCreateCheck reserves identity before Machine Save archives the machine directory into its Secret.
func (d *NutanixDriver) PreCreateCheck() (result error) {
	ctx, cancel, err := d.operationContext()
	if err != nil {
		return err
	}
	defer cancel()

	store, err := d.openCreateJournal(ctx)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, store.close()) }()

	if d.VMId != "" && (store.journal == nil || !strings.EqualFold(d.VMId, store.journal.VMUUID)) {
		return errCreateRecovery
	}

	if store.journal != nil && store.journal.VMUUID != "" {
		// A restored checkpoint grants observation only; the running process keeps its own attestation.
		return nil
	}

	return d.prepareCreate(ctx, store)
}

func (d *NutanixDriver) prepareIdentity(ctx context.Context, conn *prismv3.Client, store *journalStore,
	request *prismv3.VMIntentInput,
) error {
	subnetUUID := *request.Spec.Resources.NicList[0].SubnetReference.UUID

	err := store.reserve(ctx, conn, subnetUUID, *request.Spec.ClusterReference.UUID)
	if err != nil {
		return err
	}

	request.Metadata.UUID = new(store.journal.VMUUID)
	d.prepared = &preparedCreate{clientID: store.journal.ClientID, binding: store.binding, request: request, conn: conn}

	return nil
}

func (d *NutanixDriver) preparedFor(journal *createJournal) bool {
	return journal != nil && journal.Phase == phaseReserved && d.prepared != nil &&
		d.prepared.clientID == journal.ClientID && d.prepared.binding == journal.Binding &&
		*d.prepared.request.Metadata.UUID == journal.VMUUID
}
