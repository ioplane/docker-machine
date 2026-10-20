package driver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	prismv3 "github.com/nutanix-cloud-native/prism-go-client/v3"
	"github.com/nutanix-cloud-native/prism-go-client/v3/models"
)

const (
	allocatorMinutes = 1440
	vmCompleteState  = "COMPLETE"
)

func (d *NutanixDriver) submitCreate(ctx context.Context, store *journalStore) error {
	if !d.preparedFor(store.journal) {
		return errCreateRecovery
	}

	prepared := d.prepared
	// The right to submit is consumed in memory and is never restored from a consumer checkpoint.
	d.prepared = nil
	request, conn := prepared.request, prepared.conn

	subnetUUID, err := d.managementSubnet(request.Spec.Resources.NicList)
	if err != nil {
		return err
	}

	d.VMId = store.journal.VMUUID
	store.journal.Phase = phaseSubmitted

	err = store.save()
	if err != nil {
		return err
	}

	if !store.journal.Expires.After(time.Now()) {
		return fmt.Errorf("%w: reserved UUID expired before submission", errCreateRecovery)
	}

	response, err := conn.V3.CreateVM(ctx, request)
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	return d.acceptCreate(ctx, conn, store, response, subnetUUID)
}

func (s *journalStore) reserve(ctx context.Context, conn *prismv3.Client, subnetUUID, clusterUUID, consumerMode string,
) error {
	fresh := s.journal == nil
	if fresh {
		s.journal = &createJournal{
			Version: journalVersion, Binding: s.binding, Phase: phaseAllocating,
			ClientID: uuid.NewString(), SubnetUUID: subnetUUID, ClusterUUID: clusterUUID,
			VMUUID: "", TaskUUID: "", Expires: time.Time{},
			ConsumerMode: consumerMode,
		}

		err := s.save()
		if err != nil {
			return err
		}
	}

	journal := s.journal
	if journal.ClusterUUID != clusterUUID || journal.SubnetUUID != subnetUUID {
		return fmt.Errorf("%w: resolved create intent changed", errCreateRecovery)
	}

	if journal.Phase != phaseAllocating {
		return errCreateRecovery
	}

	var (
		response *models.IdempotenceIdentifiersResponse
		err      error
	)
	if fresh {
		response, err = conn.V3.CreateIdempotenceIdentifiers(ctx, &models.IdempotenceIdentifiersInput{
			ClientIdentifier: journal.ClientID, Count: new(int64(1)), ValidDurationInMinutes: allocatorMinutes,
		})
	} else {
		// An interrupted allocator POST is observed by its durable client ID, never repeated.
		response, err = conn.V3.GetIdempotenceIdentifiers(ctx, journal.ClientID)
	}

	if err != nil {
		return fmt.Errorf("allocate VM identity: %w", err)
	}

	if !validAllocation(response, journal.ClientID) {
		return fmt.Errorf("%w: invalid allocator response", errCreateRecovery)
	}

	journal.VMUUID = response.UUIDList[0]
	journal.Expires, journal.Phase = time.Time(response.ExpirationTime), phaseReserved

	return s.save()
}

func validAllocation(response *models.IdempotenceIdentifiersResponse, clientID string) bool {
	return response != nil && (response.ClientIdentifier == "" || response.ClientIdentifier == clientID) &&
		response.Count != nil && *response.Count == 1 &&
		len(response.UUIDList) == 1 && isUUID(response.UUIDList[0]) &&
		time.Time(response.ExpirationTime).After(time.Now())
}

func (d *NutanixDriver) lockMutation(ctx context.Context) (*journalStore, error) {
	store, err := d.openCreateJournal(ctx)
	if err != nil {
		return nil, err
	}

	if store.journal == nil {
		return store, nil
	}

	journal := store.journal
	if journal.VMUUID == "" || journal.Phase == phaseRemoved ||
		(d.VMId != "" && !strings.EqualFold(d.VMId, journal.VMUUID)) {
		return nil, errors.Join(errCreateRecovery, store.close())
	}

	d.VMId = journal.VMUUID

	conn, err := d.connect(ctx)
	if err != nil {
		return nil, errors.Join(err, store.close())
	}

	response, err := d.observeJournalVM(ctx, conn, journal)
	if err != nil {
		return nil, errors.Join(err, store.close())
	}

	if !vmCreationComplete(response) {
		return nil, errors.Join(errCreateRecovery, store.close())
	}

	return store, nil
}

func (d *NutanixDriver) restoreRemovalIdentity(ctx context.Context, store *journalStore) error {
	journal := store.journal
	if journal == nil {
		return nil
	}

	if journal.VMUUID == "" || (d.VMId != "" && !strings.EqualFold(d.VMId, journal.VMUUID)) {
		return errCreateRecovery
	}

	d.VMId = journal.VMUUID

	conn, err := d.connect(ctx)
	if err != nil {
		return err
	}

	terminal, err := createTerminal(ctx, conn, journal)
	if err != nil {
		return err
	}

	absent, err := vmAbsent(ctx, conn, journal.VMUUID)
	if err != nil {
		return err
	}

	if absent && !terminal {
		return fmt.Errorf("%w: absent VM may still be created by the submitted operation", errCreateRecovery)
	}

	if absent {
		return nil
	}

	response, err := d.observeJournalVM(ctx, conn, journal)
	if err != nil {
		return err
	}

	if !terminal && !vmCreationComplete(response) {
		return errCreateRecovery
	}

	return nil
}

func createTerminal(ctx context.Context, conn *prismv3.Client, journal *createJournal) (bool, error) {
	if journal.Phase == phaseComplete || journal.Phase == phaseRemoved {
		return true, nil
	}

	if journal.Phase != phaseAccepted || journal.TaskUUID == "" {
		return false, nil
	}

	err := waitForTask(ctx, conn, journal.TaskUUID)
	if err != nil && !errors.Is(err, errTaskFailed) {
		return false, err
	}

	return true, nil
}

func vmCreationComplete(response *prismv3.VMIntentResponse) bool {
	return response != nil && response.Status != nil && response.Status.State != nil &&
		*response.Status.State == vmCompleteState
}

func (d *NutanixDriver) acceptCreate(ctx context.Context, conn *prismv3.Client, store *journalStore,
	response *prismv3.VMIntentResponse, subnetUUID string,
) error {
	journal := store.journal
	if response == nil || response.Metadata == nil || response.Metadata.UUID == nil ||
		!strings.EqualFold(*response.Metadata.UUID, journal.VMUUID) {
		return errInvalidVM
	}

	journal.Phase = phaseAccepted
	taskUUID, taskErr := vmTaskUUID(response)
	journal.TaskUUID = taskUUID

	err := store.save()
	if err != nil {
		return err
	}

	if taskErr != nil {
		return taskErr
	}

	err = d.finishCreate(ctx, conn, response, subnetUUID)
	if err != nil {
		return err
	}

	journal.Phase = phaseComplete

	return store.save()
}

func (d *NutanixDriver) recoverCreate(ctx context.Context, store *journalStore) error {
	journal := store.journal

	d.VMId = journal.VMUUID
	if journal.Phase == phaseRemoved {
		return fmt.Errorf("%w: VM was removed", errCreateRecovery)
	}

	conn, err := d.connect(ctx)
	if err != nil {
		return err
	}
	// Observe the exact allocated VM before any task polling or further mutation.
	_, err = d.observeJournalVM(ctx, conn, journal)
	if err != nil {
		return err
	}

	if journal.TaskUUID != "" {
		err = waitForTask(ctx, conn, journal.TaskUUID)
		if err != nil {
			return err
		}
	}

	err = poll(ctx, func() (bool, error) {
		response, readErr := d.observeJournalVM(ctx, conn, journal)
		if readErr != nil {
			return false, readErr
		}

		if response.Status == nil || response.Status.State == nil || response.Status.Resources == nil {
			return false, errInvalidVM
		}

		if *response.Status.State == "ERROR" {
			return false, errTaskFailed
		}

		if *response.Status.State != vmCompleteState {
			return false, nil
		}

		address, addressErr := d.createdManagementIP(response.Status.Resources.NicList, journal.SubnetUUID)
		if addressErr != nil || address == "" {
			return false, addressErr
		}

		d.IPAddress = address

		return true, nil
	})
	if err != nil {
		return err
	}

	journal.Phase = phaseComplete

	return store.save()
}

func (d *NutanixDriver) observeJournalVM(ctx context.Context, conn *prismv3.Client,
	journal *createJournal,
) (*prismv3.VMIntentResponse, error) {
	response, err := conn.V3.GetVM(ctx, journal.VMUUID)
	if err != nil {
		if responseStatus(err) == http.StatusNotFound {
			return nil, fmt.Errorf("%w: allocated VM is absent; Create will not be replayed", errCreateRecovery)
		}

		return nil, fmt.Errorf("observe allocated VM: %w", err)
	}

	if response == nil || response.Metadata == nil || response.Metadata.UUID == nil ||
		!strings.EqualFold(*response.Metadata.UUID, journal.VMUUID) || response.Spec == nil || response.Spec.Name == nil ||
		*response.Spec.Name != d.MachineName || response.Spec.ClusterReference == nil ||
		response.Spec.ClusterReference.UUID == nil || *response.Spec.ClusterReference.UUID != journal.ClusterUUID {
		return nil, errors.Join(errCreateRecovery, errInvalidVM)
	}

	return response, nil
}
