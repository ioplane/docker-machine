package driver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	createJournalName        = "nutanix-create.json"
	journalLimit             = 64 << 10
	journalMode              = 0o600
	journalDirectoryMode     = 0o700
	journalPublicPermissions = 0o077
	machinesDirectory        = "machines"
	currentDirectory         = "."
	journalVersion           = 1
	phaseAllocating          = "allocating"
	phaseReserved            = "reserved"
	phaseSubmitted           = "submitted"
	phaseAccepted            = "accepted"
	phaseComplete            = "complete"
	phaseRemoved             = "removed"
)

var errCreateRecovery = errors.New("nutanix Create recovery requires reconciliation")

// The journal is independent of the consumer's config.json, which is not saved on failed Create.
type createJournal struct {
	Version     int       `json:"version"`
	Binding     string    `json:"binding"`
	Phase       string    `json:"phase"`
	ClientID    string    `json:"clientId"`
	VMUUID      string    `json:"vmUuid"`
	SubnetUUID  string    `json:"subnetUuid"`
	ClusterUUID string    `json:"clusterUuid"`
	TaskUUID    string    `json:"taskUuid"`
	Expires     time.Time `json:"expires"`
}

type journalStore struct {
	root    *os.Root
	lock    *os.File
	journal *createJournal
	binding string
}

func (d *NutanixDriver) openCreateJournal(ctx context.Context) (*journalStore, error) {
	if d.BaseDriver == nil || d.StorePath == "" || !filepath.IsLocal(d.MachineName) ||
		filepath.Base(d.MachineName) != d.MachineName || d.MachineName == "." {
		return nil, fmt.Errorf("%w: invalid machine journal path", errDriverConfiguration)
	}

	machine, err := d.openJournalDirectory()
	if err != nil {
		return nil, err
	}

	lock, err := lockJournal(ctx, machine)
	if err != nil {
		return nil, errors.Join(err, machine.Close())
	}

	store := &journalStore{root: machine, lock: lock, binding: "", journal: nil}

	err = store.load()
	if err != nil {
		return nil, errors.Join(err, store.close())
	}

	if store.journal != nil {
		store.binding, err = d.createBinding()
		if err == nil {
			err = store.journal.validate(store.binding)
		}

		if err != nil {
			return nil, errors.Join(err, store.close())
		}
	}

	return store, nil
}

func (d *NutanixDriver) openJournalDirectory() (_ *os.Root, result error) {
	root, err := os.OpenRoot(d.StorePath)
	if err != nil {
		return nil, fmt.Errorf("open machine store: %w", err)
	}
	defer func() { result = errors.Join(result, root.Close()) }()

	for _, path := range []string{currentDirectory, machinesDirectory, filepath.Join(machinesDirectory, d.MachineName)} {
		err = root.Mkdir(path, journalDirectoryMode)
		if err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create machine journal directory: %w", err)
		}

		info, statErr := root.Lstat(path)
		if statErr != nil || !info.IsDir() {
			return nil, fmt.Errorf("%w: machine journal directory is not a directory", errCreateRecovery)
		}

		err = validateJournalOwner(info)
		if err != nil {
			return nil, err
		}
	}

	for _, path := range []string{currentDirectory, machinesDirectory} {
		err = syncJournalDirectory(root, path)
		if err != nil {
			return nil, err
		}
	}

	machine, err := root.OpenRoot(filepath.Join(machinesDirectory, d.MachineName))
	if err != nil {
		return nil, fmt.Errorf("open machine journal directory: %w", err)
	}

	return machine, nil
}

func (s *journalStore) close() error {
	return errors.Join(s.lock.Close(), s.root.Close())
}

func (s *journalStore) load() (result error) {
	info, err := s.root.Lstat(createJournalName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("stat Create journal: %w", err)
	}

	if !info.Mode().IsRegular() || info.Mode().Perm()&journalPublicPermissions != 0 || info.Size() > journalLimit {
		return fmt.Errorf("%w: unsafe Create journal file", errCreateRecovery)
	}

	err = validateJournalOwner(info)
	if err != nil {
		return err
	}

	file, err := s.root.Open(createJournalName)
	if err != nil {
		return fmt.Errorf("open Create journal: %w", err)
	}
	defer func() { result = errors.Join(result, file.Close()) }()

	decoder := json.NewDecoder(io.LimitReader(file, journalLimit))
	decoder.DisallowUnknownFields()

	journal := new(createJournal)

	err = decoder.Decode(journal)
	if err != nil {
		return fmt.Errorf("%w: invalid Create journal", errCreateRecovery)
	}

	var extra any

	err = decoder.Decode(&extra)
	if !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing Create journal content", errCreateRecovery)
	}

	err = journal.validate(journal.Binding)
	if err != nil {
		return err
	}

	s.journal = journal

	return nil
}

func (j *createJournal) validate(binding string) error {
	valid := j.Version == journalVersion && j.Binding == binding && isUUID(j.ClientID) &&
		j.SubnetUUID != "" && j.ClusterUUID != ""
	switch j.Phase {
	case phaseAllocating:
		valid = valid && j.VMUUID == "" && j.TaskUUID == ""
	case phaseReserved, phaseSubmitted, phaseAccepted, phaseComplete, phaseRemoved:
		valid = valid && isUUID(j.VMUUID) && !j.Expires.IsZero()
	default:
		valid = false
	}

	if !valid {
		return fmt.Errorf("%w: corrupt or mismatched Create journal", errCreateRecovery)
	}

	return nil
}

func (s *journalStore) save() error {
	data, err := json.Marshal(s.journal)
	if err != nil {
		return fmt.Errorf("encode Create journal: %w", err)
	}

	name := createJournalName + "." + uuid.NewString() + ".tmp"

	file, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, journalMode)
	if err != nil {
		return fmt.Errorf("create journal temporary file: %w", err)
	}

	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}

	err = errors.Join(err, file.Close())
	if err == nil {
		err = s.root.Rename(name, createJournalName)
	}

	if err != nil {
		return fmt.Errorf("persist Create journal: %w", err)
	}

	return syncJournalDirectory(s.root, currentDirectory)
}

func syncJournalDirectory(root *os.Root, path string) error {
	directory, err := root.Open(path)
	if err != nil {
		return fmt.Errorf("open journal directory: %w", err)
	}

	return errors.Join(directory.Sync(), directory.Close())
}

// Only the digest is persisted; credentials and raw cloud-init never enter the journal.
func (d *NutanixDriver) createBinding() (string, error) {
	publicKey, err := os.ReadFile(d.GetSSHKeyPath() + ".pub")
	if err != nil {
		return "", fmt.Errorf("read bootstrap identity for Create journal: %w", err)
	}

	keyDigest := sha256.Sum256(publicKey)
	passwordDigest := sha256.Sum256([]byte(d.SSHPass))
	intent := map[string]any{
		"endpoint": strings.ToLower(net.JoinHostPort(d.Endpoint, d.Port)), "machine": d.MachineName,
		"cluster": d.Cluster, "cpus": d.VMVCPUs, "cores": d.VMCores, "memory": d.VMMem,
		"queues": d.VMNicQueues, "passthrough": d.VMCPUPassthrough, "subnets": d.Subnet,
		"image": d.Image, "imageSize": d.ImageSize, "categories": d.Categories,
		"storage": d.StorageContainer, "diskSize": d.DiskSize, "cloudInit": d.CloudInit,
		"serial": d.SerialPort, "project": d.Project, "boot": d.BootType,
		"gpus": d.GPUs, "description": d.Description,
		"sshUser": d.SSHUser, "sshPasswordDigest": hex.EncodeToString(passwordDigest[:]),
		"publicKeyDigest": hex.EncodeToString(keyDigest[:]),
	}

	data, err := json.Marshal(intent)
	if err != nil {
		return "", fmt.Errorf("encode Create intent digest: %w", err)
	}

	digest := sha256.Sum256(data)

	return hex.EncodeToString(digest[:]), nil
}
