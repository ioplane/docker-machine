package driver

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"strings"

	prismv3 "github.com/nutanix-cloud-native/prism-go-client/v3"
	"github.com/nutanix/docker-machine/utils"
)

func (d *NutanixDriver) addNICs(
	ctx context.Context, conn *prismv3.Client, clusterUUID string, res *prismv3.VMResources,
) error {
	queues, err := d.nicQueues()
	if err != nil {
		return err
	}

	if len(d.Subnet) == 0 {
		return errEmptyNetwork
	}

	filters := make([]string, 0, len(d.Subnet))
	for _, query := range d.Subnet {
		query = strings.TrimSpace(query)
		if query == "" {
			return errEmptyNetwork
		}

		if !isUUID(query) {
			encoded := &url.URL{Path: query}
			filters = append(filters, nameFilterPrefix+encoded.String())
		}
	}

	var subnets []*prismv3.SubnetIntentResponse

	if len(filters) != 0 {
		response, listErr := conn.V3.ListAllSubnet(ctx, strings.Join(filters, ","), getEmptyClientSideFilter())
		if listErr != nil {
			return fmt.Errorf("list networks: %w", listErr)
		}

		if response == nil {
			return fmt.Errorf("%w: missing network inventory", errDriverConfiguration)
		}

		subnets = response.Entities
	}

	resolved := make([]*prismv3.VMNic, 0, len(d.Subnet))
	seen := make(map[string]struct{}, len(d.Subnet))

	for _, query := range d.Subnet {
		subnetUUID, resolveErr := resolveSubnet(strings.TrimSpace(query), clusterUUID, subnets)
		if resolveErr != nil {
			return resolveErr
		}

		if _, duplicate := seen[subnetUUID]; duplicate {
			return fmt.Errorf("%w: duplicate network identity", errDriverConfiguration)
		}

		seen[subnetUUID] = struct{}{}
		resolved = append(resolved, &prismv3.VMNic{
			SubnetReference: utils.BuildReference(subnetUUID, "subnet"), NumQueues: queues,
		})
	}

	res.NicList = resolved

	return nil
}

func resolveSubnet(query, clusterUUID string, subnets []*prismv3.SubnetIntentResponse) (string, error) {
	if isUUID(query) {
		return strings.ToLower(query), nil
	}

	identity := ""

	for _, subnet := range subnets {
		if !matchesSubnet(subnet, query, clusterUUID) {
			continue
		}

		if identity != "" {
			return "", fmt.Errorf("%w: ambiguous network %s; specify its UUID", errDriverConfiguration, query)
		}

		identity = strings.ToLower(*subnet.Metadata.UUID)
	}

	if identity == "" {
		return "", fmt.Errorf("%w: network %s not found in target cluster", errDriverConfiguration, query)
	}

	return identity, nil
}

func matchesSubnet(subnet *prismv3.SubnetIntentResponse, query, clusterUUID string) bool {
	if subnet == nil || subnet.Metadata == nil || subnet.Metadata.UUID == nil || *subnet.Metadata.UUID == "" ||
		subnet.Spec == nil || subnet.Spec.Name == nil || *subnet.Spec.Name != query ||
		subnet.Spec.Resources == nil || subnet.Spec.Resources.SubnetType == nil {
		return false
	}

	kind := *subnet.Spec.Resources.SubnetType
	if kind == "OVERLAY" {
		return true
	}

	reference := subnet.Spec.ClusterReference

	return kind == "VLAN" && reference != nil && reference.UUID != nil && *reference.UUID == clusterUUID
}

func (d *NutanixDriver) managementSubnet(nics []*prismv3.VMNic) (string, error) {
	selector := d.VMManagementNetwork
	if selector == "" {
		// shortcut: omission keeps first-incoming NIC semantics; set an explicit management UUID for multi-NIC profiles.
		return *nics[0].SubnetReference.UUID, nil
	}

	if !isUUID(selector) {
		return "", fmt.Errorf("%w: nutanix-vm-management-network must be a subnet UUID", errDriverConfiguration)
	}

	matches := 0

	for _, nic := range nics {
		if nic != nil && nic.SubnetReference != nil && nic.SubnetReference.UUID != nil &&
			strings.EqualFold(*nic.SubnetReference.UUID, selector) {
			matches++
		}
	}

	if matches != 1 {
		return "", fmt.Errorf("%w: management subnet must identify exactly one attached NIC", errDriverConfiguration)
	}

	return strings.ToLower(selector), nil
}

func (d *NutanixDriver) checkNICQueues(nics []*prismv3.VMNicOutputStatus, management string) error {
	queues, err := d.nicQueues()
	if err != nil || queues == nil {
		return err
	}

	if len(nics) != len(d.Subnet) {
		return fmt.Errorf("%w: NIC queues postcondition: NIC count differs from intent", errInvalidVM)
	}

	seen := make(map[string]bool, len(nics))
	for _, nic := range nics {
		if nic == nil || nic.SubnetReference == nil || nic.SubnetReference.UUID == nil ||
			!isUUID(*nic.SubnetReference.UUID) || nic.NumQueues == nil || *nic.NumQueues != *queues {
			return fmt.Errorf("%w: NIC queues postcondition: missing identity or configured queue value", errInvalidVM)
		}

		identity := strings.ToLower(*nic.SubnetReference.UUID)
		if seen[identity] {
			return fmt.Errorf("%w: NIC queues postcondition: duplicate subnet identity", errInvalidVM)
		}

		seen[identity] = true
	}

	// Names are bound by the journal intent; only UUID inputs can be matched without another inventory request.
	for _, subnet := range append([]string{management}, d.Subnet...) {
		identity := strings.TrimSpace(subnet)
		if isUUID(identity) && !seen[strings.ToLower(identity)] {
			return fmt.Errorf("%w: NIC queues postcondition: missing configured subnet", errInvalidVM)
		}
	}

	return nil
}

func (d *NutanixDriver) createdManagementIP(nics []*prismv3.VMNicOutputStatus, subnetUUID string) (string, error) {
	err := d.checkNICQueues(nics, subnetUUID)
	if err != nil {
		return "", err
	}

	return managementIP(nics, subnetUUID)
}

func managementIP(nics []*prismv3.VMNicOutputStatus, subnetUUID string) (string, error) {
	if subnetUUID == "" {
		return "", fmt.Errorf("%w: missing management network identity", errDriverConfiguration)
	}

	var selected *prismv3.VMNicOutputStatus

	for _, nic := range nics {
		if nic == nil || nic.SubnetReference == nil || nic.SubnetReference.UUID == nil ||
			!strings.EqualFold(*nic.SubnetReference.UUID, subnetUUID) {
			continue
		}

		if selected != nil {
			return "", fmt.Errorf("%w: ambiguous management NIC", errInvalidVM)
		}

		selected = nic
	}

	if selected == nil || selected.IsConnected != nil && !*selected.IsConnected {
		return "", nil
	}

	return endpointIP(selected.IPEndpointList)
}

func endpointIP(endpoints []*prismv3.IPAddress) (string, error) {
	var selected netip.Addr

	for _, endpoint := range endpoints {
		if endpoint == nil || endpoint.IP == nil || *endpoint.IP == "" {
			continue
		}

		address, err := netip.ParseAddr(*endpoint.IP)
		if err != nil {
			return "", fmt.Errorf("%w: malformed management IP", errInvalidVM)
		}

		address = address.Unmap()
		if !address.IsGlobalUnicast() || address.Zone() != "" {
			continue
		}

		if !selected.IsValid() || address.Compare(selected) < 0 {
			selected = address
		}
	}

	if !selected.IsValid() {
		return "", nil
	}

	return selected.String(), nil
}
