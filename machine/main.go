// Command machine serves the Nutanix node driver over the Rancher machine RPC protocol.
package main

import (
	"github.com/nutanix/docker-machine/machine/driver"
	"github.com/rancher/machine/libmachine/drivers/plugin"
)

func main() {
	plugin.RegisterDriver(driver.NewDriver("", ""))
}
