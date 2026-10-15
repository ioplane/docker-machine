package driver

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	log "github.com/sirupsen/logrus"

	prism "github.com/nutanix-cloud-native/prism-go-client/v3"
	"go.yaml.in/yaml/v3"
)

const yamlStringTag = "!!str"

var (
	errUtilityGPUNameRequired = errors.New("gpu name must be passed in order to retrieve the GPU")
	errUtilityNoGPUsAvailable = errors.New("no available GPUs found in Prism Element cluster")
	errUtilityGPUUnavailable  = errors.New("no available GPU found in Prism Element that matches required GPU name")
)

func isUUID(uuid string) bool {
	uuidPattern := regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

	return uuidPattern.MatchString(uuid)
}

// deleteAllContents will remove all the contents of a node
// Mark sure to pass the correct node in otherwise bad things will happen
// func deleteAllContents(node *yaml.Node) {
// 	node.Content = []*yaml.Node{}
// }

// buildStringNodes builds Nodes for a single key: value instance.
func buildStringNodes(key, value, comment string) []*yaml.Node {
	keyNode := &yaml.Node{
		Kind:        yaml.ScalarNode,
		Tag:         yamlStringTag,
		Value:       key,
		HeadComment: comment,
	}
	valueNode := &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   yamlStringTag,
		Value: value,
	}

	return []*yaml.Node{keyNode, valueNode}
}

func buildScalarNodes(key string) []*yaml.Node {
	keyNode := &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   yamlStringTag,
		Value: key,
	}

	return []*yaml.Node{keyNode}
}

// buildMapNodes builds Nodes for a key: map instance
// func buildMapNodes(key string) (*yaml.Node, *yaml.Node) {
// 	n1, n2 := &yaml.Node{
// 		Kind:  yaml.ScalarNode,
// 		Tag:   "!!str",
// 		Value: key,
// 	}, &yaml.Node{Kind: yaml.MappingNode,
// 		Tag: "!!map",
// 	}
// 	return n1, n2
// }

// buildSeqNodes builds Nodes for a key: map instance
// func buildSeqNodes(key string) (*yaml.Node, *yaml.Node) {
// 	n1, n2 := &yaml.Node{
// 		Kind:  yaml.ScalarNode,
// 		Tag:   "!!str",
// 		Value: key,
// 	}, &yaml.Node{Kind: yaml.SequenceNode,
// 		Tag: "!!seq",
// 	}
// 	return n1, n2
// }

// GetGPUList retrieves a list of GPUs from the Nutanix Prism Element based on the provided GPU names and PE UUID.
// It returns a slice of VMGpu pointers or an error if any issues occur during the retrieval.
func GetGPUList(ctx context.Context, conn *prism.Client, gpus []string, peUUID string) ([]*prism.VMGpu, error) {
	resultGPUs := make([]*prism.VMGpu, 0, len(gpus))

	for _, gpu := range gpus {
		foundGPU, err := GetGPU(ctx, conn, peUUID, gpu)
		if err != nil {
			return nil, err
		}

		resultGPUs = append(resultGPUs, foundGPU)
	}

	return resultGPUs, nil
}

// GetGPU retrieves a specific GPU from the Nutanix Prism Element based on the provided GPU name and PE UUID.
// It returns a VMGpu pointer or an error if the GPU is not found or if any issues occur during the retrieval.
func GetGPU(ctx context.Context, conn *prism.Client, peUUID, gpu string) (*prism.VMGpu, error) {
	if gpu == "" {
		return nil, errUtilityGPUNameRequired
	}

	log.Infof("Searching GPU %s in Prism Element with UUID %s", gpu, peUUID)

	allGPUs, err := GetGPUsForPE(ctx, conn, peUUID)
	if err != nil {
		return nil, err
	}

	if len(allGPUs) == 0 {
		return nil, fmt.Errorf("%w with UUID %s", errUtilityNoGPUsAvailable, peUUID)
	}

	for _, peGPU := range allGPUs {
		if peGPU.Status != "UNUSED" {
			continue
		}

		if peGPU.Name == gpu {
			log.Infof("GPU %s found with ID %d in Prism Element", peGPU.Name, *peGPU.DeviceID)

			return &prism.VMGpu{
				DeviceID: peGPU.DeviceID,
				Mode:     &peGPU.Mode,
				Vendor:   &peGPU.Vendor,
			}, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", errUtilityGPUUnavailable, gpu)
}

// GetGPUsForPE retrieves all GPUs associated with a specific Prism Element (PE) UUID.
// It returns a slice of GPU pointers or an error if any issues occur during the retrieval.
func GetGPUsForPE(ctx context.Context, conn *prism.Client, peUUID string) ([]*prism.GPU, error) {
	gpus := make([]*prism.GPU, 0)

	hosts, err := conn.V3.ListAllHost(ctx)
	if err != nil {
		return gpus, fmt.Errorf("list prism hosts: %w", err)
	}

	for _, host := range hosts.Entities {
		if host == nil ||
			host.Status == nil ||
			host.Status.ClusterReference == nil ||
			host.Status.Resources == nil ||
			host.Status.ClusterReference.UUID != peUUID {
			continue
		}

		for _, peGpu := range host.Status.Resources.GPUList {
			if peGpu == nil {
				continue
			}

			gpus = append(gpus, peGpu)
		}
	}

	return gpus, nil
}
