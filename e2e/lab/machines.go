package lab

import (
	"errors"
	"os"
)

// Machines is the lab: one hub and two nodes, small enough to run together
// next to WSL.
var Machines = []MachineSpec{
	{Name: "hub", MemoryMB: 1024, CPUs: 2},
	{Name: "node-a", MemoryMB: 1024, CPUs: 2},
	{Name: "node-b", MemoryMB: 1024, CPUs: 2},
}

// Spec returns the machine called name.
func Spec(name string) (MachineSpec, bool) {
	for _, s := range Machines {
		if s.Name == name {
			return s, true
		}
	}
	return MachineSpec{}, false
}

// Dir is where the lab lives: $MCPMESH_LAB_DIR, a WSL path to a folder on a
// Windows drive (Hyper-V can't keep disks in the WSL file system), for
// example /mnt/d/mcp-mesh-vms. The lab needs a few GB there.
func Dir() (string, error) {
	d := os.Getenv("MCPMESH_LAB_DIR")
	if d == "" {
		return "", errors.New("set MCPMESH_LAB_DIR to a folder on a Windows drive, as a WSL path (e.g. /mnt/d/mcp-mesh-vms)")
	}
	return d, nil
}
