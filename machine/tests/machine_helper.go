// Package tests provides helpers for explicitly requested machine integration tests.
package tests

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
)

const (
	defaultMemoryMiB = 1024
	defaultCPUCount  = 1
)

// MachineConfig contains the machine integration-test configuration.
type MachineConfig struct {
	MachineName string
	SSHUser     string
	SSHPass     string
	NutanixUser string
	NutanixPass string
	Endpoint    string
	VNICs       []string
	VDisks      []string
	VMMem       int
	VMCPUs      int
	VMCores     int
}

// CreateMachine creates a machine without logging credential-bearing arguments.
//
//nolint:gocritic // Preserve the upstream public value-receiver method set.
func (config MachineConfig) CreateMachine() ([]byte, error) {
	vmMem := config.VMMem
	if vmMem == 0 {
		vmMem = defaultMemoryMiB
	}

	vmCores := config.VMCores
	if vmCores == 0 {
		vmCores = defaultCPUCount
	}

	vmCPUs := config.VMCPUs
	if vmCPUs == 0 {
		vmCPUs = defaultCPUCount
	}

	const baseArgumentCount = 19

	args := make([]string, 0,
		baseArgumentCount+2*len(config.VNICs)+2*len(config.VDisks)+1)
	args = append(args,
		"create",
		"-d",
		"nutanix",
		"--nutanix-username",
		config.NutanixUser,
		"--nutanix-password",
		config.NutanixPass,
		"--nutanix-endpoint",
		config.Endpoint,
		"--nutanix-vm-mem",
		strconv.Itoa(vmMem),
		"--nutanix-vm-cpus",
		strconv.Itoa(vmCPUs),
		"--nutanix-vm-cores",
		strconv.Itoa(vmCores),
		"--nutanix-vm-ssh-username",
		config.SSHUser,
		"--nutanix-vm-ssh-password",
		config.SSHPass,
	)

	for _, nic := range config.VNICs {
		args = append(args, "--nutanix-vm-network-uuid", nic)
	}

	for _, disk := range config.VDisks {
		args = append(args, "--nutanix-vm-disk-uuid", disk)
	}

	args = append(args, config.MachineName)

	return runMachine(args)
}

// DeleteMachine removes the configured integration-test machine.
//
//nolint:gocritic // Preserve the upstream public value-receiver method set.
func (config MachineConfig) DeleteMachine() ([]byte, error) {
	args := []string{
		"rm",
		config.MachineName,
	}

	return runMachine(args)
}

// StartMachine starts the configured integration-test machine.
//
//nolint:gocritic // Preserve the upstream public value-receiver method set.
func (config MachineConfig) StartMachine() ([]byte, error) {
	args := []string{
		"start",
		config.MachineName,
	}

	return runMachine(args)
}

// StopMachine stops the configured integration-test machine.
//
//nolint:gocritic // Preserve the upstream public value-receiver method set.
func (config MachineConfig) StopMachine() ([]byte, error) {
	args := []string{
		"stop",
		config.MachineName,
	}

	return runMachine(args)
}

func runMachine(arguments []string) ([]byte, error) {
	//nolint:gosec // The fixed executable receives argv directly; no shell interprets them.
	command := exec.CommandContext(context.Background(), "docker-machine", arguments...)

	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("run integration-test machine command: %w", err)
	}

	return output, nil
}
