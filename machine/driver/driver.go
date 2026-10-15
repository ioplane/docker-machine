// Package driver implements the Nutanix Rancher node driver.
package driver

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nutanix/docker-machine/utils"
	"github.com/rancher/machine/libmachine/drivers"
	"github.com/rancher/machine/libmachine/mcnflag"
	"github.com/rancher/machine/libmachine/ssh"
	"github.com/rancher/machine/libmachine/state"
	log "github.com/sirupsen/logrus"
	"go.yaml.in/yaml/v3"

	client "github.com/nutanix-cloud-native/prism-go-client"
	prismv3 "github.com/nutanix-cloud-native/prism-go-client/v3"
)

var (
	errDriverConfiguration   = errors.New("invalid Nutanix driver configuration")
	errTaskFailed            = errors.New("nutanix task failed")
	errInvalidTask           = errors.New("nutanix response contains no valid task UUID")
	errCloudInitSyntax       = errors.New("cloud-init syntax error")
	errCreateTimeout         = errors.New("timeout waiting for vm to create")
	errIPTimeout             = errors.New("timeout waiting for vm to obtain an IP address")
	errDeleteTimeout         = errors.New("timeout waiting to delete vm")
	errEmptyUsername         = errors.New("nutanix-username cannot be empty")
	errEmptyPassword         = errors.New("nutanix-password cannot be empty")
	errEmptyEndpoint         = errors.New("nutanix-endpoint cannot be empty")
	errEmptyCluster          = errors.New("nutanix-cluster cannot be empty")
	errEmptyNetwork          = errors.New("nutanix-vm-network cannot be empty")
	errEmptyImage            = errors.New("nutanix-vm-image cannot be empty")
	errInvalidQueues         = errors.New("nutanix-vm-nic-queues must be a positive decimal integer")
	errQueuesOverflow        = errors.New("nutanix-vm-nic-queues must be a positive decimal integer within int64 range")
	errQueueCPUConfiguration = errors.New("nutanix-vm-nic-queues requires a positive CPU topology within int64 range")
	errQueuesExceedCPU       = errors.New("nutanix-vm-nic-queues cannot exceed nutanix-vm-cpus * nutanix-vm-cores")
)

const (
	driverOperationFormat = "nutanix driver operation: %w"
	int64Bits             = 64
	nameFilterPrefix      = "name=="
	connectionLogFormat   = "Connecting on: %s"
	endpointFormat        = "%s:%s"
	taskSucceeded         = "SUCCEEDED"
	taskPollSeconds       = 5
	categoryPairLength    = 2
	mibPerGiB             = 1024
	bytesPerGiB           = 1 << 30
	defaultTimeout        = 300
	maxDescriptionLength  = 1000
	powerPollAttempts     = 1200
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
func (d *NutanixDriver) Create() error {
	_, err := d.nicQueues()
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	name := d.GetMachineName()

	ctx := context.Background()

	conn, err := d.connect()
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	// Prepare VM creation request
	res := d.initialResources()
	metadata := &prismv3.Metadata{Kind: new("vm")}
	spec := &prismv3.VM{Name: new(name), Description: new(d.Description), Resources: res}

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

	res.GuestCustomization, err = d.guestCustomization()
	if err != nil {
		return err
	}

	request := &prismv3.VMIntentInput{Metadata: metadata, Spec: spec}

	resp, err := conn.V3.CreateVM(ctx, request)
	if err != nil {
		log.Errorf("Error creating vm: [%v]", err)

		return fmt.Errorf(driverOperationFormat, err)
	}

	return d.finishCreate(ctx, conn, resp)
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
func (d *NutanixDriver) GetState() (state.State, error) {
	ctx := context.Background()

	conn, err := d.connect()
	if err != nil {
		return state.Error, fmt.Errorf("get VM state: %w", err)
	}

	resp, err := conn.V3.GetVM(ctx, d.VMId)
	if err != nil {
		return state.Error, fmt.Errorf("get VM state: %w", err)
	}

	switch *resp.Status.Resources.PowerState {
	case "ON":
		return state.Running, nil
	case "OFF":
		return state.Stopped, nil
	}

	return state.None, nil
}

// Kill stops a host forcefully.
func (d *NutanixDriver) Kill() error {
	return d.Stop()
}

// Remove a host.
func (d *NutanixDriver) Remove() error {
	if d.VMId == "" {
		log.Infof("VMId is empty, nothing to remove")

		return nil
	}

	return d.removeVM()
}

// Restart a host. This may just call Stop(); Start() if the provider does not
// have any special restart behaviour.
func (d *NutanixDriver) Restart() error {
	err := d.Stop()
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	return d.Start()
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

	if d.Timeout < defaultTimeout {
		log.Warnf("nutanix-timeout is too low, setting to 300 seconds")

		d.Timeout = defaultTimeout
	} else {
		d.Timeout = opts.Int("nutanix-timeout")
	}

	d.GPUs = opts.StringSlice("nutanix-vm-gpu")

	d.setDescription(opts.String("nutanix-vm-description"))

	return nil
}

// Start a host.
func (d *NutanixDriver) Start() error { return d.setPowerState("ON") }

// Stop a host.
func (d *NutanixDriver) Stop() error { return d.setPowerState("OFF") }

func (d *NutanixDriver) removeVM() error {
	name := d.GetMachineName()
	ctx := context.Background()

	conn, err := d.connect()
	if err != nil {
		return fmt.Errorf("error connecting to Nutanix: %w", err)
	}

	log.Infof("Deleting VM %s (%s)", name, d.VMId)

	resp, err := conn.V3.DeleteVM(ctx, d.VMId)
	if err != nil {
		return fmt.Errorf("error launching deleting VM %s: %w", name, err)
	}

	taskUUID, taskErr := vmTaskUUID(resp)
	if taskErr != nil {
		return taskErr
	}

	log.Infof("waiting to delete vm %s (%s): task %s", name, d.VMId, taskUUID)

	// Wait end of the task
waitTask:
	for attempt := range d.Timeout / taskPollSeconds {
		resp, err := conn.V3.GetTask(ctx, taskUUID)
		if err != nil {
			log.Errorf("Error getting task: [%v]", err)

			return fmt.Errorf(driverOperationFormat, err)
		}

		switch *resp.Status {
		case taskSucceeded:
			log.Infof("VM %s deletion task succeeded", name)

			break waitTask
		case "FAILED":
			errMsg := strings.ReplaceAll(*resp.ErrorDetail, "\n", " ")
			if strings.Contains(errMsg, "ENTITY_NOT_FOUND") {
				log.Infof("VM %s already deleted", name)

				return nil
			}

			log.Errorf("Error deleting vm: %v", errMsg)

			return fmt.Errorf("%w: %s", errTaskFailed, errMsg)
		}

		if attempt == (d.Timeout/taskPollSeconds)-1 {
			log.Errorf("Timeout waiting to delete vm %s", name)

			return errDeleteTimeout
		}

		log.Infof("VM %s deletion is in %s state", name, *resp.Status)
		<-time.After(taskPollSeconds * time.Second)
	}

	return nil
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

func (d *NutanixDriver) setPowerState(power string) error {
	name := d.GetMachineName()

	ctx := context.Background()

	conn, err := d.connect()
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	vmResp, err := conn.V3.GetVM(ctx, d.VMId)
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	// Prepare VM update request
	request := &prismv3.VMIntentInput{}
	request.Spec = vmResp.Spec
	request.Metadata = vmResp.Metadata
	request.Spec.Resources.PowerState = new(power)

	resp, err := conn.V3.UpdateVM(ctx, d.VMId, request)
	if err != nil {
		return fmt.Errorf(driverOperationFormat, err)
	}

	taskUUID, taskErr := vmTaskUUID(resp)
	if taskErr != nil {
		return taskErr
	}

	// Wait for the VM to be deleted
	for range powerPollAttempts {
		resp, err := conn.V3.GetTask(ctx, taskUUID)
		if err != nil || *resp.Status != taskSucceeded {
			<-time.After(1 * time.Second)

			continue
		}

		return nil
	}

	return fmt.Errorf("%w: unable to set power state of VM %s", errDriverConfiguration, name)
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

func (d *NutanixDriver) addNICs(
	ctx context.Context, conn *prismv3.Client, clusterUUID string, res *prismv3.VMResources,
) error {
	queues, err := d.nicQueues()
	if err != nil {
		return err
	}

	subnetFilter := d.addUUIDNICs(res, queues)

	// Retrieve all subnets
	responseSubnets, err := conn.V3.ListAllSubnet(ctx, subnetFilter, getEmptyClientSideFilter())
	if err != nil {
		log.Errorf("Error getting subnets: [%v]", err)

		return fmt.Errorf(driverOperationFormat, err)
	}

	// Search for non UUID Subnets
	for _, query := range d.Subnet {
		if isUUID(query) {
			continue
		}

		log.Infof("Searching subnet %s", query)

		for _, subnet := range responseSubnets.Entities {
			if !matchesSubnet(subnet, query, clusterUUID) {
				continue
			}

			res.NicList = append(res.NicList, &prismv3.VMNic{
				SubnetReference: utils.BuildReference(*subnet.Metadata.UUID, "subnet"),
				NumQueues:       queues,
			})
			log.Infof("Subnet %s found with UUID: %s", *subnet.Status.Name, *subnet.Metadata.UUID)

			break
		}
	}

	if len(res.NicList) < 1 {
		log.Errorf("Network %s not found in cluster %s", d.Subnet, d.Cluster)

		return fmt.Errorf("%w: network %s not found in cluster %s", errDriverConfiguration, d.Subnet, d.Cluster)
	}

	return nil
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
				newSize := int64(d.ImageSize * mibPerGiB)
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

func (d *NutanixDriver) cloudInitUserData(pubKey []byte) ([]byte, error) {
	defaultData := []byte("#cloud-config\r\nusers:\r\n - name: root\r\n   ssh_authorized_keys:\r\n    - " + string(pubKey))
	if d.CloudInit == "" {
		return defaultData, nil
	}

	document := yaml.Node{Kind: yaml.DocumentNode, HeadComment: "cloud-config"}

	if !strings.HasPrefix(d.CloudInit, "#cloud-config") {
		return nil, errCloudInitSyntax
	}

	r1 := strings.ReplaceAll(d.CloudInit, "\\n", "\n")
	r2 := strings.ReplaceAll(r1, "\\r", "\r")

	err := yaml.Unmarshal([]byte(r2), &document)
	if err != nil {
		return nil, fmt.Errorf(driverOperationFormat, err)
	}

	if document.Content == nil {
		return defaultData, nil
	}

	log.Infof("Cloud-init merge")

	usersNode := iterateNode(&document, "users")

	if usersNode == nil {
		rootNode := document.Content[0]
		rootNode.Content = append(rootNode.Content, buildScalarNodes("users")...)
		usersNode = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		rootNode.Content = append(rootNode.Content, usersNode)
	}

	rancherNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	rancherNode.Content = append(rancherNode.Content, buildStringNodes("name", "root", "")...)
	rancherNode.Content = append(rancherNode.Content, buildStringNodes("sudo", "ALL=(ALL) NOPASSWD:ALL", "")...)
	rancherNode.Content = append(rancherNode.Content, buildScalarNodes("ssh_authorized_keys")...)

	sshSeqNode := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	sshSeqNode.Content = append(sshSeqNode.Content, buildScalarNodes(string(pubKey))...)

	rancherNode.Content = append(rancherNode.Content, sshSeqNode)
	usersNode.Content = append(usersNode.Content, rancherNode)

	userdata, err := yaml.Marshal(&document)
	if err != nil {
		return nil, fmt.Errorf("marshal cloud-init: %w", err)
	}

	return userdata, nil
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

func (d *NutanixDriver) waitForIP(ctx context.Context, conn *prismv3.Client, vmUUID string) error {
	name := d.GetMachineName()
	// Wait for the VM obtain an IP address
	for attempt := range d.Timeout / taskPollSeconds {
		vmInfo, err := conn.V3.GetVM(ctx, vmUUID)
		if err != nil {
			log.Errorf("Error getting vm: [%v]", err)

			return fmt.Errorf(driverOperationFormat, err)
		}

		if len(vmInfo.Status.Resources.NicList[0].IPEndpointList) != 0 {
			d.IPAddress = *vmInfo.Status.Resources.NicList[0].IPEndpointList[0].IP

			break
		}

		if attempt == (d.Timeout/taskPollSeconds)-1 {
			log.Errorf("Timeout waiting for vm %s to obtain an IP address", name)
			log.Infof("Deleting VM %s (%s)", name, vmUUID)

			_, err := conn.V3.DeleteVM(ctx, vmUUID)
			if err != nil {
				log.Errorf("Failed to delete VM %s (%s): %v", name, vmUUID, err)
			}

			return errIPTimeout
		}

		log.Infof("Waiting VM %s ip configuration", name)
		<-time.After(taskPollSeconds * time.Second)
	}

	log.Infof("VM %s configured with ip address %s", name, d.IPAddress)

	return nil
}

func (d *NutanixDriver) initialResources() *prismv3.VMResources {
	res := &prismv3.VMResources{PowerState: new("ON")}

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

func (d *NutanixDriver) waitForCreateTask(ctx context.Context, conn *prismv3.Client, vmUUID, taskUUID string) error {
	name := d.GetMachineName()
	// Wait end of the task
	for attempt := range d.Timeout / taskPollSeconds {
		resp, err := conn.V3.GetTask(ctx, taskUUID)
		if err != nil {
			log.Errorf("Error getting task: [%v]", err)

			return fmt.Errorf(driverOperationFormat, err)
		}

		switch *resp.Status {
		case taskSucceeded:
			log.Infof("VM %s creation task succeeded", name)

			return nil
		case "FAILED":
			errMsg := strings.ReplaceAll(*resp.ErrorDetail, "\n", " ")
			log.Errorf("Error creating vm: [%v]", errMsg)
			log.Infof("Deleting VM %s (%s)", name, vmUUID)

			_, err := conn.V3.DeleteVM(ctx, vmUUID)
			if err != nil {
				log.Errorf("Failed to delete VM %s (%s): %v", name, vmUUID, err)
			}

			return fmt.Errorf("%w: %s", errTaskFailed, errMsg)
		}

		if attempt == (d.Timeout/taskPollSeconds)-1 {
			log.Errorf("Timeout waiting for vm %s to create", name)

			return errCreateTimeout
		}

		log.Infof("VM %s creation is in %s state", name, *resp.Status)
		<-time.After(taskPollSeconds * time.Second)
	}

	return nil
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

func (d *NutanixDriver) connect() (*prismv3.Client, error) {
	credentials := d.credentials()
	log.Infof(connectionLogFormat, credentials.URL)

	conn, err := prismv3.NewV3Client(credentials)
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

func (d *NutanixDriver) addUUIDNICs(res *prismv3.VMResources, queues *int64) string {
	// Search target subnet
	for index, subnet := range d.Subnet {
		// Trim extraneous whitespace
		d.Subnet[index] = strings.TrimSpace(subnet)
	}

	subnetFilter := ""

	// Create subnets filter query and add UUID subnets directly
	for _, subnet := range d.Subnet {
		if isUUID(subnet) {
			n := &prismv3.VMNic{
				SubnetReference: utils.BuildReference(subnet, "subnet"),
				NumQueues:       queues,
			}

			res.NicList = append(res.NicList, n)

			log.Infof("UUID subnet added %s", subnet)
		} else {
			if subnetFilter != "" {
				subnetFilter += ","
			}

			t := &url.URL{Path: subnet}
			encodedSubnet := t.String()
			subnetFilter += nameFilterPrefix + encodedSubnet
		}
	}

	return subnetFilter
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

func matchesSubnet(subnet *prismv3.SubnetIntentResponse, query, clusterUUID string) bool {
	if *subnet.Spec.Name != query {
		return false
	}

	kind := *subnet.Spec.Resources.SubnetType
	if kind == "OVERLAY" {
		return true
	}

	return kind == "VLAN" && *subnet.Spec.ClusterReference.UUID == clusterUUID
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

func (d *NutanixDriver) finishCreate(ctx context.Context, conn *prismv3.Client, resp *prismv3.VMIntentResponse) error {
	name := d.GetMachineName()
	vmUUID := *resp.Metadata.UUID

	taskUUID, taskErr := vmTaskUUID(resp)
	if taskErr != nil {
		return taskErr
	}

	log.Infof("waiting for vm %s (%s) to create: task %s", name, vmUUID, taskUUID)

	err := d.waitForCreateTask(ctx, conn, vmUUID, taskUUID)
	if err != nil {
		return err
	}

	d.VMId = vmUUID

	log.Infof("VM %s successfully created", name)

	return d.waitForIP(ctx, conn, vmUUID)
}
