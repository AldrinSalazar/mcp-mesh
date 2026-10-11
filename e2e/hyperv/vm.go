package hyperv

import (
	"context"
	"fmt"
	"strings"
)

// LabNote marks VMs this package created. Destructive operations refuse a
// VM without it, so the tests can never remove someone's own VM.
const LabNote = "mcp-mesh e2e lab"

// Spec describes a lab VM. Paths are Windows paths.
type Spec struct {
	Name     string
	Dir      string // where the VM's files and differencing disk live
	Parent   string // base VHDX the VM's disk differs from
	SeedISO  string // cloud-init seed attached as a DVD
	MemoryMB int
	CPUs     int
	Switch   string
}

// Info is a VM's current state.
type Info struct {
	Name  string   `json:"Name"`
	State string   `json:"State"`
	Notes string   `json:"Notes"`
	MAC   string   `json:"MAC"`
	IPs   []string `json:"IPs"`
}

// Get returns the VM's state, or ok=false if it doesn't exist.
func Get(ctx context.Context, name string) (info Info, ok bool, err error) {
	var out []Info
	err = RunJSON(ctx, fmt.Sprintf(`
$vm = Get-VM -Name %s -ErrorAction SilentlyContinue
$r = @()
if ($vm) {
  $na = Get-VMNetworkAdapter -VM $vm | Select-Object -First 1
  $r += [pscustomobject]@{ Name = $vm.Name; State = "$($vm.State)"; Notes = $vm.Notes;
    MAC = $na.MacAddress; IPs = @($na.IPAddresses) }
}
ConvertTo-Json -Compress -InputObject @($r)`, Quote(name)), &out)
	if err != nil || len(out) == 0 {
		return Info{}, false, err
	}
	return out[0], true, nil
}

// Create makes a Generation 2 VM with a differencing disk on Spec.Parent.
// Secure Boot is off (the Debian cloud image's boot chain doesn't need it
// for testing) and checkpoints are standard, so a checkpoint of a stopped
// VM is a plain disk snapshot.
func Create(ctx context.Context, s Spec) error {
	disk := s.Dir + `\` + s.Name + ".vhdx"
	_, err := Run(ctx, fmt.Sprintf(`
New-Item -ItemType Directory -Force -Path %[2]s | Out-Null
if (Test-Path %[3]s) { Remove-Item -Force %[3]s }
New-VHD -Path %[3]s -ParentPath %[4]s -Differencing | Out-Null
$vm = New-VM -Name %[1]s -Generation 2 -MemoryStartupBytes %[5]dMB -VHDPath %[3]s -SwitchName %[6]s -Path %[2]s
Set-VM -VM $vm -ProcessorCount %[7]d -StaticMemory -CheckpointType Standard -AutomaticStopAction ShutDown -AutomaticStartAction Nothing -Notes %[8]s
Set-VMFirmware -VM $vm -EnableSecureBoot Off
Add-VMDvdDrive -VM $vm -Path %[9]s
Set-VMComPort -VM $vm -Number 1 -Path ('\\.\pipe\' + %[1]s + '-com1')
`, Quote(s.Name), Quote(s.Dir), Quote(disk), Quote(s.Parent), s.MemoryMB, Quote(s.Switch), s.CPUs, Quote(LabNote), Quote(s.SeedISO)))
	if err != nil {
		return fmt.Errorf("create VM %s: %w", s.Name, err)
	}
	return nil
}

// Start boots the VM if it isn't running.
func Start(ctx context.Context, name string) error {
	if _, err := Run(ctx, fmt.Sprintf(`$vm = Get-VM -Name %s; if ($vm.State -ne 'Running') { Start-VM -VM $vm }`, Quote(name))); err != nil {
		return fmt.Errorf("start VM %s: %w", name, err)
	}
	return nil
}

// Stop shuts the VM down through the guest, or cuts its power when
// turnOff is set (what a crash or a pulled plug looks like to the mesh).
func Stop(ctx context.Context, name string, turnOff bool) error {
	how := "-Force"
	if turnOff {
		how = "-TurnOff"
	}
	if _, err := Run(ctx, fmt.Sprintf(`$vm = Get-VM -Name %s; if ($vm.State -ne 'Off') { Stop-VM -VM $vm %s }`, Quote(name), how)); err != nil {
		return fmt.Errorf("stop VM %s: %w", name, err)
	}
	return nil
}

// Checkpoint snapshots the VM under name, replacing an older one.
func Checkpoint(ctx context.Context, vm, name string) error {
	_, err := Run(ctx, fmt.Sprintf(`$vm = Get-VM -Name %[1]s
Get-VMSnapshot -VM $vm -Name %[2]s -ErrorAction SilentlyContinue | Remove-VMSnapshot
Checkpoint-VM -VM $vm -SnapshotName %[2]s`, Quote(vm), Quote(name)))
	if err != nil {
		return fmt.Errorf("checkpoint %s of %s: %w", name, vm, err)
	}
	return nil
}

// HasCheckpoint reports whether the VM has a checkpoint called name.
func HasCheckpoint(ctx context.Context, vm, name string) (bool, error) {
	out, err := Run(ctx, fmt.Sprintf(`[bool](Get-VMSnapshot -VMName %s -Name %s -ErrorAction SilentlyContinue)`, Quote(vm), Quote(name)))
	if err != nil {
		return false, fmt.Errorf("checkpoints of %s: %w", vm, err)
	}
	return strings.TrimSpace(out) == "True", nil
}

// Restore returns the VM to the checkpoint. The VM ends up in the state the
// checkpoint was taken in (off, for the lab's clean checkpoints).
func Restore(ctx context.Context, vm, name string) error {
	_, err := Run(ctx, fmt.Sprintf(`$vm = Get-VM -Name %[1]s
if ($vm.State -ne 'Off') { Stop-VM -VM $vm -TurnOff }
Restore-VMSnapshot -VMName %[1]s -Name %[2]s -Confirm:$false`, Quote(vm), Quote(name)))
	if err != nil {
		return fmt.Errorf("restore %s of %s: %w", name, vm, err)
	}
	return nil
}

// Remove deletes a lab VM and its files. It refuses VMs without LabNote.
func Remove(ctx context.Context, name string) error {
	_, err := Run(ctx, fmt.Sprintf(`$vm = Get-VM -Name %[1]s -ErrorAction SilentlyContinue
if ($vm) {
  if ($vm.Notes -ne %[2]s) { throw "VM %[1]s is not a lab VM; not removing it" }
  if ($vm.State -ne 'Off') { Stop-VM -VM $vm -TurnOff }
  $disks = @(Get-VMHardDiskDrive -VM $vm | ForEach-Object Path)
  $dir = $vm.Path
  Remove-VM -VM $vm -Force
  $disks | ForEach-Object { Remove-Item -Force $_ -ErrorAction SilentlyContinue }
  Remove-Item -Recurse -Force (Join-Path $dir 'Virtual Machines') -ErrorAction SilentlyContinue
  Remove-Item -Recurse -Force (Join-Path $dir 'Snapshots') -ErrorAction SilentlyContinue
}`, Quote(name), Quote(LabNote)))
	if err != nil {
		return fmt.Errorf("remove VM %s: %w", name, err)
	}
	return nil
}
