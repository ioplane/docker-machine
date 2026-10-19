// Package driver implements the Nutanix Rancher node driver.
package driver

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/nutanix/docker-machine/utils"
	"github.com/rancher/machine/libmachine/drivers"
	"github.com/rancher/machine/libmachine/mcnflag"
	"github.com/rancher/machine/libmachine/ssh"
	"github.com/rancher/machine/libmachine/state"
	log "github.com/sirupsen/logrus"

	client "github.com/nutanix-cloud-native/prism-go-client"
	prismv3 "github.com/nutanix-cloud-native/prism-go-client/v3"
)

var (
	errDriverConfiguration   = errors.New("invalid Nutanix driver configuration")
	errTaskFailed            = errors.New("nutanix task failed")
	errInvalidVM             = errors.New("nutanix response contains no valid VM UUID")
	errInvalidTask           = errors.New("nutanix response contains no valid task UUID")
	errCloudInitSyntax       = errors.New("cloud-init syntax error")
	errEmptyUsername         = errors.New("nutanix-username cannot be empty")
	errEmptyPassword         = errors.New("nutanix-password cannot be empty")
	errEmptyEndpoint         = errors.New("nutanix-endpoint cannot be empty")
	errEmptyCluster          = errors.New("nutanix-cluster cannot be empty")
	errEmptyNetwork          = errors.New("nutanix-vm-network cannot be empty")
	errEmptyImage            = errors.New("nutanix-vm-image cannot be empty")
	errInvalidQueues         = errors.New("nutanix-vm-nic-queues must be a positive decimal integer")
	errQueuesOverflow        = errors.New("nutanix-vm-nic-queues must be a positive decimal integer within int64 range")
	errQueueCPUConfiguration = errors.New("CPU sockets and cores must be positive with product within int64 range")
	errQueuesExceedCPU       = errors.New("nutanix-vm-nic-queues cannot exceed nutanix-vm-cpus * nutanix-vm-cores")
)

const (
	driverOperationFormat = "nutanix driver operation: %w"
	int64Bits             = 64
	nameFilterPrefix      = "name=="
	connectionLogFormat   = "Connecting on: %s"
	endpointFormat        = "%s:%s"
	taskSucceeded         = "SUCCEEDED"
	powerOn               = "ON"
	powerOff              = "OFF"
	taskPollSeconds       = 5
	categoryPairLength    = 2
	mibPerGiB             = 1024
	bytesPerGiB           = 1 << 30
	defaultTimeout        = 300
	maxDescriptionLength  = 1000
	decimalBase           = 10
	defaultVMMem          = 2048
	defaultVCPUs          = 2
	defaultCores          = 1
	defaultBootType       = "legacy"
)

// NutanixDriver driver structure.
type NutanixDriver struct {
	*drivers.BaseDriver

	Endpoint         string
	Username         string
	Password         string
	Port             string
	Insecure         bool
	Cluster          string
	VMVCPUs          int
	VMCores          int
	VMNicQueues      string `json:",omitempty"`
	VMCPUPassthrough bool
	VMMem            int
	SSHPass          string
	Subnet           []string
	Image            string
	ImageSize        int
	VMId             string
	SessionAuth      bool
	ProxyURL         string
	Categories       []string
	StorageContainer string
	DiskSize         int
	CloudInit        string
	SerialPort       bool
	Project          string
	BootType         string
	Timeout          int
	GPUs             []string
	Description      string
	prepared         *preparedCreate

	ConsumerCreateBinding string `json:"consumerCreateBinding,omitempty"`
	VMManagementNetwork   string `json:",omitempty"` //nolint:tagliatelle // Established persisted JSON spelling.
}

// NewDriver create new instance.
func NewDriver(hostname, storePath string) *NutanixDriver {
	return &NutanixDriver{
		BaseDriver: &drivers.BaseDriver{
			MachineName: hostname,
			StorePath:   storePath,
		},
	}
}

// Create a host using the driver's config.
func (d *NutanixDriver) Create() (result error) {
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
		return fmt.Errorf("%w: existing VM identity requires reconciliation before Create", errDriverConfiguration)
	}

	if store.journal != nil && store.journal.VMUUID != "" && !d.preparedFor(store.journal) {
		return d.recoverCreate(ctx, store)
	}

	if !d.preparedFor(store.journal) {
		err = d.prepareCreate(ctx, store)
		if err != nil {
			return err
		}
	}

	return d.submitCreate(ctx, store)
}

// DriverName returns the name of the driver.
func (*NutanixDriver) DriverName() string {
	return "nutanix"
}

// GetCreateFlags returns the mcnflag.Flag slice representing the flags
// that can be set, their descriptions and defaults.
func (*NutanixDriver) GetCreateFlags() []mcnflag.Flag {
	return []mcnflag.Flag{
		mcnflag.StringFlag{
			EnvVar: "NUTANIX_USERNAME",
			Name:   "nutanix-username",
			Usage:  "Nutanix management username",
		},
		mcnflag.StringFlag{
			EnvVar: "NUTANIX_PASSWORD",
			Name:   "nutanix-password",
			Usage:  "Nutanix management password",
		},
		mcnflag.StringFlag{
			EnvVar: "NUTANIX_ENDPOINT",
			Name:   "nutanix-endpoint",
			Usage:  "Nutanix management endpoint ip address/FQDN",
		},
		mcnflag.StringFlag{
			EnvVar: "NUTANIX_PORT",
			Name:   "nutanix-port",
			Usage:  "Nutanix management endpoint port (default: 9440)",
			Value:  "9440",
		},
		mcnflag.BoolFlag{
			EnvVar: "NUTANIX_INSECURE",
			Name:   "nutanix-insecure",
			Usage:  "Explicitly allow the provider to perform \"insecure\" SSL requests",
		},
		mcnflag.StringFlag{
			EnvVar: "NUTANIX_CLUSTER",
			Name:   "nutanix-cluster",
			Usage:  "Nutanix Cluster to install VM on",
		},
		mcnflag.IntFlag{
			EnvVar: "NUTANIX_VM_MEM",
			Name:   "nutanix-vm-mem",
			Usage:  "Memory in MB of the VM to be created",
			Value:  defaultVMMem,
		},
		mcnflag.IntFlag{
			EnvVar: "NUTANIX_VM_CPUS",
			Name:   "nutanix-vm-cpus",
			Usage:  "Number of VCPUs of the VM to be created",
			Value:  defaultVCPUs,
		},
		mcnflag.IntFlag{
			EnvVar: "NUTANIX_VM_CORES",
			Name:   "nutanix-vm-cores",
			Usage:  "Number of cores per VCPU of the VM to be created",
			Value:  defaultCores,
		},
		mcnflag.BoolFlag{
			EnvVar: "NUTANIX_VM_CPU_PASSTHROUGH",
			Name:   "nutanix-vm-cpu-passthrough",
			Usage:  "Enable passthrough the host's CPU features to the newly created VM",
		},
		mcnflag.StringFlag{
			EnvVar: "NUTANIX_VM_NIC_QUEUES",
			Name:   "nutanix-vm-nic-queues",
			Usage:  "Queues for each NIC (1 to total vCPUs); empty preserves the platform default",
		},
		mcnflag.StringSliceFlag{
			Name:  "nutanix-vm-network",
			Usage: "The name of the network to attach to the newly created VM",
		},
		mcnflag.StringFlag{
			Name:  "nutanix-vm-management-network",
			Usage: "Management subnet UUID among the attached networks; empty selects the first incoming NIC",
		},
		mcnflag.StringFlag{
			EnvVar: "NUTANIX_VM_IMAGE",
			Name:   "nutanix-vm-image",
			Usage:  "The name of the VM disk to clone from, for the newly created VM",
		},
		mcnflag.IntFlag{
			EnvVar: "NUTANIX_VM_IMAGE_SIZE",
			Name:   "nutanix-vm-image-size",
			Usage:  "Increase the size of the template image",
			Value:  0,
		},
		mcnflag.StringSliceFlag{
			Name:  "nutanix-vm-categories",
			Usage: "The name of the categories who will be applied to the newly created VM",
		},
		mcnflag.StringFlag{
			EnvVar: "NUTANIX_STORAGE_CONTAINER",
			Name:   "nutanix-storage-container",
			Usage:  "The UUID of the storage container",
			Value:  "",
		},
		mcnflag.IntFlag{
			EnvVar: "NUTANIX_DISK_SIZE",
			Name:   "nutanix-disk-size",
			Usage:  "The size of the attached disk",
			Value:  0,
		},
		mcnflag.StringFlag{
			EnvVar: "NUTANIX_CLOUD_INIT",
			Name:   "nutanix-cloud-init",
			Usage:  "Cloud-init configuration",
		},
		mcnflag.BoolFlag{
			EnvVar: "NUTANIX_VM_SERIAL_PORT",
			Name:   "nutanix-vm-serial-port",
			Usage:  "Attach a serial port to the newly created VM (type Null)",
		},
		mcnflag.StringFlag{
			EnvVar: "NUTANIX_PROJECT",
			Name:   "nutanix-project",
			Usage:  "The name of the project to assign the VM",
			Value:  "",
		},
		mcnflag.StringFlag{
			EnvVar: "NUTANIX_BOOT_TYPE",
			Name:   "nutanix-boot-type",
			Usage:  "The boot type of the VM (legacy or uefi)",
			Value:  defaultBootType,
		},
		mcnflag.IntFlag{
			EnvVar: "NUTANIX_TIMEOUT",
			Name:   "nutanix-timeout",
			Usage:  "Timeout for Nutanix operations (in seconds)",
			Value:  defaultTimeout,
		},
		mcnflag.StringSliceFlag{
			Name:  "nutanix-vm-gpu",
			Usage: "The list of GPU devices to attach to the newly created VM",
		},
		mcnflag.StringFlag{
			Name:  "nutanix-vm-description",
			Usage: "The description of the newly created VM",
		},
	}
}

// GetSSHHostname returns hostname for use with ssh.
func (d *NutanixDriver) GetSSHHostname() (string, error) {
	ip, err := d.GetIP()
	if err != nil {
		return "", fmt.Errorf("get SSH IP: %w", err)
	}

	return ip, nil
}

// GetURL returns a Docker compatible host URL for connecting to this host.
func (d *NutanixDriver) GetURL() (string, error) {
	ip, err := d.GetIP()
	if err != nil {
		return "", fmt.Errorf("get VM IP: %w", err)
	}

	return "tcp://" + net.JoinHostPort(ip, "2376"), nil
}

// GetState returns the state that the host is in (running, stopped, etc).
func (d *NutanixDriver) GetState() (_ state.State, resultErr error) {
	ctx, cancel, err := d.operationContext()
	if err != nil {
		return state.Error, err
	}
	defer cancel()

	store, err := d.openCreateJournal(ctx)
	if err != nil {
		return state.Error, err
	}

	defer func() { resultErr = errors.Join(resultErr, store.close()) }()

	if store.journal != nil {
		if store.journal.VMUUID == "" || (d.VMId != "" && !strings.EqualFold(d.VMId, store.journal.VMUUID)) {
			return state.Error, errCreateRecovery
		}

		d.VMId = store.journal.VMUUID
	}

	conn, err := d.connect(ctx)
	if err != nil {
		return state.Error, fmt.Errorf("get VM state: %w", err)
	}

	resp, err := conn.V3.GetVM(ctx, d.VMId)
	if err != nil {
		return state.Error, fmt.Errorf("get VM state: %w", err)
	}

	if resp == nil || resp.Status == nil || resp.Status.Resources == nil || resp.Status.Resources.PowerState == nil {
		return state.Error, errInvalidVM
	}

	switch *resp.Status.Resources.PowerState {
	case powerOn:
		return state.Running, nil
	case powerOff:
		return state.Stopped, nil
	}

	return state.None, nil
}

// Kill stops a host forcefully.
func (d *NutanixDriver) Kill() error {
	return d.Stop()
}

// Remove a host.
func (d *NutanixDriver) Remove() (result error) {
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

	err = d.restoreRemovalIdentity(ctx, store)
	if err != nil {
		return err
	}

	if d.VMId == "" {
		log.Infof("VMId is empty, nothing to remove")

		return nil
	}

	err = d.removeVM(ctx)
	if err != nil {
		return err
	}

	if store.journal != nil {
		store.journal.Phase = phaseRemoved

		return store.save()
	}

	return nil
}

// Restart a host. This may just call Stop(); Start() if the provider does not
// have any special restart behaviour.
func (d *NutanixDriver) Restart() (result error) {
	ctx, cancel, err := d.operationContext()
	if err != nil {
		return err
	}
	defer cancel()

	store, err := d.lockMutation(ctx)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, store.close()) }()

	err = d.setPowerStateContext(ctx, powerOff)
	if err != nil {
		return err
	}

	return d.setPowerStateContext(ctx, powerOn)
}

// SetConfigFromFlags configures the driver with the object that was returned
// by RegisterCreateFlags.
func (d *NutanixDriver) SetConfigFromFlags(opts drivers.DriverOptions) error {
	err := d.setCredentials(opts)
	if err != nil {
		return err
	}

	d.Categories = opts.StringSlice("nutanix-vm-categories")

	d.Cluster = opts.String("nutanix-cluster")
	if d.Cluster == "" {
		return errEmptyCluster
	}

	err = d.setHardwareFlags(opts)
	if err != nil {
		return err
	}

	d.Subnet = opts.StringSlice("nutanix-vm-network")

	d.VMManagementNetwork = opts.String("nutanix-vm-management-network")
	if len(d.Subnet) == 0 {
		return errEmptyNetwork
	}

	d.Image = opts.String("nutanix-vm-image")
	if d.Image == "" {
		return errEmptyImage
	}

	d.ImageSize = opts.Int("nutanix-vm-image-size")
	d.CloudInit = opts.String("nutanix-cloud-init")
	d.SerialPort = opts.Bool("nutanix-vm-serial-port")
	d.Project = opts.String("nutanix-project")

	err = d.setBootType(opts.String("nutanix-boot-type"))
	if err != nil {
		return err
	}

	d.Timeout = opts.Int("nutanix-timeout")
	if d.Timeout < defaultTimeout {
		log.Warnf("nutanix-timeout is too low, setting to 300 seconds")

		d.Timeout = defaultTimeout
	}

	d.GPUs = opts.StringSlice("nutanix-vm-gpu")

	d.setDescription(opts.String("nutanix-vm-description"))

	return d.validateHardware()
}

// Start a host.
func (d *NutanixDriver) Start() error { return d.setPowerState(powerOn) }

// Stop a host.
func (d *NutanixDriver) Stop() error { return d.setPowerState(powerOff) }

func (d *NutanixDriver) prepareCreate(ctx context.Context, store *journalStore) error {
	err := d.validateHardware()
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	customization, err := d.guestCustomization()
	if err != nil {
		return err
	}

	store.binding, err = d.createBinding()
	if err != nil {
		return err
	}

	conn, err := d.connect(ctx)
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	// Prepare VM creation request
	res := d.initialResources()
	metadata := &prismv3.Metadata{Kind: new("vm")}
	spec := &prismv3.VM{Name: new(d.GetMachineName()), Description: new(d.Description), Resources: res}

	err = d.assignProject(ctx, conn, metadata)
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	cluster, err := d.findCluster(ctx, conn)
	if err != nil {
		return err
	}

	spec.ClusterReference = utils.BuildReference(*cluster.Metadata.UUID, "cluster")

	err = d.addNICs(ctx, conn, *cluster.Metadata.UUID, res)
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	err = d.assignCategories(metadata)
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	err = d.addImage(ctx, conn, res)
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	d.addDisk(res)

	err = d.addGPUs(ctx, conn, *cluster.Metadata.UUID, res)
	if err != nil {
		return err
	}

	res.GuestCustomization = customization

	request := &prismv3.VMIntentInput{Metadata: metadata, Spec: spec}

	return d.prepareIdentity(ctx, conn, store, request)
}

func (d *NutanixDriver) removeVM(ctx context.Context) error {
	if !isUUID(d.VMId) {
		return errInvalidVM
	}

	conn, err := d.connect(ctx)
	if err != nil {
		return err
	}

	observe := func() (bool, error) { return vmAbsent(ctx, conn, d.VMId) }

	absent, err := observe()
	if err != nil || absent {
		return err
	}

	resp, err := conn.V3.DeleteVM(ctx, d.VMId)
	if err != nil {
		if responseStatus(err) == http.StatusNotFound {
			return poll(ctx, observe)
		}

		return fmt.Errorf("delete VM: %w", err)
	}

	taskUUID, err := vmTaskUUID(resp)
	if err != nil {
		return err
	}

	err = waitForTask(ctx, conn, taskUUID)
	if err != nil {
		if !errors.Is(err, errTaskFailed) {
			return err
		}

		absent, observationErr := observe()
		if observationErr != nil || !absent {
			return errors.Join(err, observationErr)
		}

		return nil
	}

	return poll(ctx, observe)
}

// nicQueues also validates persisted driver state before any API request.
func (d *NutanixDriver) nicQueues() (*int64, error) {
	if d.VMNicQueues == "" {
		return nil, nil //nolint:nilnil // Unset queues must remain a nil SDK pointer.
	}

	for _, digit := range d.VMNicQueues {
		if digit < '0' || digit > '9' {
			return nil, errInvalidQueues
		}
	}

	queues, err := strconv.ParseInt(d.VMNicQueues, decimalBase, int64Bits)
	if err != nil || queues < 1 {
		return nil, errQueuesOverflow
	}

	totalCPUs, err := d.queueCPUCount()
	if err != nil {
		return nil, err
	}

	if queues > totalCPUs {
		return nil, errQueuesExceedCPU
	}

	return &queues, nil
}

func (d *NutanixDriver) setPowerState(power string) (result error) {
	ctx, cancel, err := d.operationContext()
	if err != nil {
		return err
	}
	defer cancel()

	store, err := d.lockMutation(ctx)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, store.close()) }()

	return d.setPowerStateContext(ctx, power)
}

func (d *NutanixDriver) setPowerStateContext(ctx context.Context, power string) error {
	conn, err := d.connect(ctx)
	if err != nil {
		return err
	}

	vmResp, err := conn.V3.GetVM(ctx, d.VMId)
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	complete, err := vmPowerStateComplete(vmResp, d.VMId, power)
	if err != nil {
		return err
	}

	if complete {
		err = ctx.Err()
		if err != nil {
			return fmt.Errorf(driverOperationFormat, err)
		}

		return nil
	}

	request := &prismv3.VMIntentInput{Spec: vmResp.Spec, Metadata: vmResp.Metadata}
	request.Spec.Resources.PowerState = new(power)

	resp, err := conn.V3.UpdateVM(ctx, d.VMId, request)
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	taskUUID, err := vmTaskUUID(resp)
	if err != nil {
		return err
	}

	err = waitForTask(ctx, conn, taskUUID)
	if err != nil {
		return err
	}

	return poll(ctx, func() (bool, error) {
		vmInfo, readErr := conn.V3.GetVM(ctx, d.VMId)
		if readErr != nil {
			return false, fmt.Errorf("verify VM power state: %w", readErr)
		}

		if vmInfo == nil || vmInfo.Status == nil || vmInfo.Status.Resources == nil ||
			vmInfo.Status.Resources.PowerState == nil {
			return false, errInvalidVM
		}

		return *vmInfo.Status.Resources.PowerState == power, nil
	})
}

func vmPowerStateComplete(response *prismv3.VMIntentResponse, vmUUID, power string) (bool, error) {
	if response == nil || response.Spec == nil || response.Spec.Resources == nil || response.Metadata == nil ||
		response.Metadata.UUID == nil || *response.Metadata.UUID == "" ||
		!strings.EqualFold(*response.Metadata.UUID, vmUUID) {
		return false, errInvalidVM
	}

	return vmCreationComplete(response) && response.Spec.Resources.PowerState != nil &&
		*response.Spec.Resources.PowerState == power && response.Status.Resources != nil &&
		response.Status.Resources.PowerState != nil && *response.Status.Resources.PowerState == power, nil
}

func vmTaskUUID(response any) (string, error) {
	execution := vmExecutionContext(response)
	if execution == nil {
		return "", errInvalidTask
	}

	task, ok := execution.TaskUUID.(string)
	if !ok || task == "" {
		return "", errInvalidTask
	}

	return task, nil
}

func getEmptyClientSideFilter() []*client.AdditionalFilter {
	return make([]*client.AdditionalFilter, 0)
}

func (d *NutanixDriver) assignProject(ctx context.Context, conn *prismv3.Client, metadata *prismv3.Metadata) error {
	// Assign to project
	if d.Project != "" {
		projectFilter := nameFilterPrefix + d.Project

		projects, projectErr := conn.V3.ListAllProject(ctx, projectFilter)
		if projectErr != nil {
			return fmt.Errorf("list projects: %w", projectErr)
		}

		if len(projects.Entities) == 0 {
			log.Infof("Project %s not found", d.Project)

			return fmt.Errorf("%w: project %s not found", errDriverConfiguration, d.Project)
		} else if len(projects.Entities) > 1 {
			log.Infof("Multiple projects found with name %s", d.Project)

			return fmt.Errorf("%w: multiple projects found with name %s", errDriverConfiguration, d.Project)
		}

		log.Infof("Select project %s", projects.Entities[0].Status.Name)

		metadata.ProjectReference = &prismv3.Reference{
			Kind: new("project"),
			UUID: projects.Entities[0].Metadata.UUID,
		}
	}

	return nil
}

func (d *NutanixDriver) findCluster(ctx context.Context, conn *prismv3.Client) (*prismv3.ClusterIntentResponse, error) {
	// Search target cluster
	log.Infof("Searching cluster %s", d.Cluster)

	c := &url.URL{Path: d.Cluster}
	encodedCluster := c.String()
	clusterFilter := nameFilterPrefix + encodedCluster

	clusters, err := conn.V3.ListAllCluster(ctx, clusterFilter)
	if err != nil {
		log.Errorf("Error getting clusters: [%v]", err)

		return nil, fmt.Errorf(driverOperationFormat, err)
	}

	// Validate filtered Clusters

	foundClusters := make([]*prismv3.ClusterIntentResponse, 0)

	for _, s := range clusters.Entities {
		peSpec := s.Spec
		if peSpec.Name == d.Cluster {
			foundClusters = append(foundClusters, s)
		}
	}

	if len(foundClusters) == 0 {
		return nil, fmt.Errorf("%w: failed to retrieve cluster %s", errDriverConfiguration, d.Cluster)
	} else if len(foundClusters) > 1 {
		return nil, fmt.Errorf("%w: more than one Cluster found with name %s", errDriverConfiguration, d.Cluster)
	}

	log.Infof("Cluster %s found with UUID: %s", foundClusters[0].Status.Name, *foundClusters[0].Metadata.UUID)

	return foundClusters[0], nil
}

func (d *NutanixDriver) assignCategories(metadata *prismv3.Metadata) error {
	if len(d.Categories) != 0 {
		log.Infof("Categories provided: %s", d.Categories)

		metadata.CategoriesMapping = make(map[string][]string)
		metadata.UseCategoriesMapping = new(true)

		for _, group := range d.Categories {
			category := strings.Split(group, "=")

			if len(category) < categoryPairLength {
				log.Errorf("Malformed group %s", group)

				return fmt.Errorf("%w: malformed group %s", errDriverConfiguration, group)
			}

			// Strip extraneous whitespace to make this more error tolerant
			category[0] = strings.TrimSpace(category[0])
			category[1] = strings.TrimSpace(category[1])

			metadata.CategoriesMapping[category[0]] = append(metadata.CategoriesMapping[category[0]], category[1])
			log.Infof("Added category %s: %s", category[0], category[1])
		}
	}

	return nil
}

func (d *NutanixDriver) addImage(ctx context.Context, conn *prismv3.Client, res *prismv3.VMResources) error {
	// Search image template
	imageURL := &url.URL{Path: d.Image}
	encodedImage := imageURL.String()
	imageFilter := nameFilterPrefix + encodedImage

	images, err := conn.V3.ListAllImage(ctx, imageFilter)
	if err != nil {
		log.Errorf("Error getting images: [%v]", err)

		return fmt.Errorf(driverOperationFormat, err)
	}

	for _, image := range images.Entities {
		if *image.Status.Name == d.Image {
			log.Infof("Image %s found with UUID: %s", *image.Status.Name, *image.Metadata.UUID)

			if *image.Status.Resources.ImageType != "DISK_IMAGE" {
				log.Errorf("Image %s is not a disk template", d.Image)

				return fmt.Errorf("%w: image %s is not a disk template", errDriverConfiguration, d.Image)
			}

			if d.ImageSize > 0 {
				newSize := int64(d.ImageSize) * mibPerGiB
				n := &prismv3.VMDisk{
					DataSourceReference: utils.BuildReference(*image.Metadata.UUID, "image"),
					DiskSizeMib:         &newSize,
				}
				res.DiskList = append(res.DiskList, n)
			} else {
				n := &prismv3.VMDisk{
					DataSourceReference: utils.BuildReference(*image.Metadata.UUID, "image"),
				}
				res.DiskList = append(res.DiskList, n)
			}

			break
		}
	}

	if len(res.DiskList) < 1 {
		log.Errorf("Image %s not found", d.Image)

		return fmt.Errorf("%w: image %s not found", errDriverConfiguration, d.Image)
	}

	return nil
}

func (d *NutanixDriver) credentials() client.Credentials {
	return client.Credentials{
		URL:         fmt.Sprintf(endpointFormat, d.Endpoint, d.Port),
		Endpoint:    d.Endpoint,
		Username:    d.Username,
		Password:    d.Password,
		Port:        d.Port,
		Insecure:    d.Insecure,
		SessionAuth: d.SessionAuth,
		ProxyURL:    d.ProxyURL,
	}
}

func (d *NutanixDriver) waitForIP(ctx context.Context, conn *prismv3.Client, vmUUID, subnetUUID string) error {
	return poll(ctx, func() (bool, error) {
		vmInfo, err := conn.V3.GetVM(ctx, vmUUID)
		if err != nil {
			return false, fmt.Errorf("get VM IP: %w", err)
		}

		if vmInfo == nil || vmInfo.Status == nil || vmInfo.Status.Resources == nil {
			return false, errInvalidVM
		}

		if d.VMNicQueues != "" && !vmCreationComplete(vmInfo) {
			return false, nil
		}

		address, err := d.createdManagementIP(vmInfo.Status.Resources.NicList, subnetUUID)
		if err != nil || address == "" {
			return false, err
		}

		d.IPAddress = address

		return true, nil
	})
}

func (d *NutanixDriver) initialResources() *prismv3.VMResources {
	res := &prismv3.VMResources{PowerState: new(powerOn)}

	res.MemorySizeMib = new(int64(d.VMMem))
	res.NumSockets = new(int64(d.VMVCPUs))
	res.NumVcpusPerSocket = new(int64(d.VMCores))

	// Configure BootType
	log.Infof("Set BootType to %s", d.BootType)

	res.BootConfig = &prismv3.VMBootConfig{}

	res.BootConfig.BootType = new(strings.ToUpper(d.BootType))
	if d.BootType == "legacy" {
		res.BootConfig.BootDeviceOrderList = append(res.BootConfig.BootDeviceOrderList, new("DISK"))
	}

	// Configure CPU Passthrough
	if d.VMCPUPassthrough {
		res.EnableCPUPassthrough = new(d.VMCPUPassthrough)
	}

	// Add Serial Port
	if d.SerialPort {
		serialPort := &prismv3.VMSerialPort{
			Index:       utils.Int64Ptr(0),
			IsConnected: new(true),
		}

		res.SerialPortList = append(res.SerialPortList, serialPort)
	}

	return res
}

func (d *NutanixDriver) guestCustomization() (*prismv3.GuestCustomization, error) {
	// SSH Key generation
	err := ssh.GenerateSSHKey(d.GetSSHKeyPath())
	if err != nil {
		log.Errorf("Error generating ssh key")

		return nil, fmt.Errorf(driverOperationFormat, err)
	}

	pubKey, err := os.ReadFile(d.GetSSHKeyPath() + ".pub")
	if err != nil {
		log.Errorf("Error reading public key")

		return nil, fmt.Errorf(driverOperationFormat, err)
	}

	log.Infof("SSH pub key ready (%s)", pubKey)

	userdata, err := d.cloudInitUserData(pubKey)
	if err != nil {
		return nil, err
	}
	// Generate metadata for the VM
	specUUID := uuid.New()
	cloudMetadata := fmt.Sprintf("{\"hostname\": %q, \"uuid\": %q}", d.GetMachineName(), specUUID)

	// Encode the metadata by base64
	metadataEncoded := base64.StdEncoding.EncodeToString([]byte(cloudMetadata))

	cloudInit := &prismv3.GuestCustomizationCloudInit{
		UserData: new(base64.StdEncoding.EncodeToString(userdata)),
		MetaData: new(metadataEncoded),
	}

	guestCustomization := &prismv3.GuestCustomization{
		CloudInit: cloudInit,
	}

	return guestCustomization, nil
}

func (d *NutanixDriver) validateHardware() error {
	if d.VMManagementNetwork != "" && !isUUID(d.VMManagementNetwork) {
		return fmt.Errorf("%w: nutanix-vm-management-network must be a subnet UUID", errDriverConfiguration)
	}

	_, err := d.queueCPUCount()
	if err != nil {
		return err
	}

	if d.VMMem < 1 {
		return fmt.Errorf("%w: nutanix-vm-mem must be positive", errDriverConfiguration)
	}

	if d.ImageSize < 0 || int64(d.ImageSize) > math.MaxInt64/mibPerGiB {
		return fmt.Errorf("%w: nutanix-vm-image-size exceeds the nonnegative int64 MiB range", errDriverConfiguration)
	}

	if d.DiskSize < 0 || int64(d.DiskSize) > math.MaxInt64/bytesPerGiB {
		return fmt.Errorf("%w: nutanix-disk-size exceeds the nonnegative int64 byte range", errDriverConfiguration)
	}

	_, err = d.nicQueues()

	return err
}

func (d *NutanixDriver) queueCPUCount() (int64, error) {
	if d.VMVCPUs < 1 || d.VMCores < 1 || int64(d.VMVCPUs) > math.MaxInt64/int64(d.VMCores) {
		return 0, errQueueCPUConfiguration
	}

	return int64(d.VMVCPUs) * int64(d.VMCores), nil
}

func vmExecutionContext(response any) *prismv3.ExecutionContext {
	var execution *prismv3.ExecutionContext

	switch result := response.(type) {
	case *prismv3.VMIntentResponse:
		if result != nil && result.Status != nil {
			execution = result.Status.ExecutionContext
		}
	case *prismv3.DeleteResponse:
		if result != nil && result.Status != nil {
			execution = result.Status.ExecutionContext
		}
	default:
		return nil
	}

	return execution
}

func (d *NutanixDriver) addDisk(res *prismv3.VMResources) {
	// Add additional disks
	if d.StorageContainer != "" && d.DiskSize > 0 {
		disk := &prismv3.VMDisk{
			DiskSizeBytes: new(int64(d.DiskSize) * bytesPerGiB),
			StorageConfig: &prismv3.VMStorageConfig{
				StorageContainerReference: &prismv3.StorageContainerReference{
					Kind: "storage_container",
					UUID: d.StorageContainer,
				},
			},
		}

		res.DiskList = append(res.DiskList, disk)

		log.Infof("Added disk with %d GiB on storage container with UUID: %s", d.DiskSize, d.StorageContainer)
	}
}

func (d *NutanixDriver) setDescription(description string) {
	d.Description = description
	if d.Description == "" {
		d.Description = "VM created by Nutanix Rancher Node Driver"
	} else if len(d.Description) > maxDescriptionLength {
		d.Description = d.Description[:maxDescriptionLength]
	}
}

func (d *NutanixDriver) connect(ctx context.Context) (*prismv3.Client, error) {
	credentials := d.credentials()
	log.Infof(connectionLogFormat, credentials.URL)

	conn, err := prismv3.NewV3ClientWithContext(ctx, credentials)
	if err != nil {
		return nil, fmt.Errorf("create Prism client: %w", err)
	}

	return conn, nil
}

func (d *NutanixDriver) setBootType(bootType string) error {
	d.BootType = bootType
	if d.BootType != "uefi" && d.BootType != "legacy" {
		return fmt.Errorf("%w: nutanix-boot-type %s is invalid", errDriverConfiguration, d.BootType)
	}

	return nil
}

func (d *NutanixDriver) addGPUs(
	ctx context.Context, conn *prismv3.Client, clusterUUID string, res *prismv3.VMResources,
) error {
	// Add GPU devices
	if len(d.GPUs) > 0 {
		gpuList, gpuErr := GetGPUList(ctx, conn, d.GPUs, clusterUUID)
		if gpuErr != nil {
			return fmt.Errorf("get GPU list: %w", gpuErr)
		}

		res.GpuList = gpuList
	}

	return nil
}

func (d *NutanixDriver) setHardwareFlags(opts drivers.DriverOptions) error {
	d.DiskSize = opts.Int("nutanix-disk-size")
	d.StorageContainer = opts.String("nutanix-storage-container")

	d.VMMem = opts.Int("nutanix-vm-mem")
	d.VMVCPUs = opts.Int("nutanix-vm-cpus")
	d.VMCores = opts.Int("nutanix-vm-cores")

	d.VMNicQueues = opts.String("nutanix-vm-nic-queues")

	_, err := d.nicQueues()
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	d.VMCPUPassthrough = opts.Bool("nutanix-vm-cpu-passthrough")

	return nil
}

func (d *NutanixDriver) setCredentials(opts drivers.DriverOptions) error {
	d.Username = opts.String("nutanix-username")
	if d.Username == "" {
		return errEmptyUsername
	}

	d.Password = opts.String("nutanix-password")
	if d.Password == "" {
		return errEmptyPassword
	}

	d.Endpoint = opts.String("nutanix-endpoint")
	if d.Endpoint == "" {
		return errEmptyEndpoint
	}

	d.Port = opts.String("nutanix-port")

	d.Insecure = opts.Bool("nutanix-insecure")

	return nil
}

func (d *NutanixDriver) finishCreate(
	ctx context.Context, conn *prismv3.Client, resp *prismv3.VMIntentResponse, subnetUUID string,
) error {
	if resp == nil || resp.Metadata == nil || resp.Metadata.UUID == nil || *resp.Metadata.UUID == "" {
		return errInvalidVM
	}

	name := d.GetMachineName()
	vmUUID := *resp.Metadata.UUID
	// Retain accepted identity even when task validation or polling fails.
	d.VMId = vmUUID

	taskUUID, taskErr := vmTaskUUID(resp)
	if taskErr != nil {
		return taskErr
	}

	log.Infof("waiting for vm %s (%s) to create: task %s", name, vmUUID, taskUUID)

	err := waitForTask(ctx, conn, taskUUID)
	if err != nil {
		return err
	}

	log.Infof("VM %s successfully created", name)

	return d.waitForIP(ctx, conn, vmUUID, subnetUUID)
}
