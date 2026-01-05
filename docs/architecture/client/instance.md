# Instance Details

Instance is the most complex resource due to device handling and UID/GID shifting for volumes.

## InstanceConfig

```go
type InstanceConfig struct {
    Type         incusApi.InstanceType   // container or vm
    Full         bool                    // fetch full instance details
    Image        string                  // image name (required)
    Resources    []Resource              // dependencies that must be ensured
    Devices      []InstanceDevice        // pre-creation devices
    PostDevices  []InstanceDevice        // post-creation devices (need UID/GID)
    Config       map[string]string       // instance config
    ExtraDevices map[string]map[string]string  // raw Incus devices
}
```

## Device Types

Devices are configuration structs attached to instances:

```go
const (
    InstanceDeviceTypeProxy = "proxy"
    InstanceDeviceTypeDisk  = "disk"
    InstanceDeviceTypeNic   = "nic"
)

type InstanceDevice struct {
    Name   string
    Config InstanceDeviceConfig
}

type InstanceDeviceConfig struct {
    DeviceType  string
    Network     Resource                  // for nic
    Proxy       InstanceDeviceProxyConfig // for proxy
    Disk        InstanceDeviceDiskConfig  // for disk
    ExtraConfig map[string]string         // for custom devices
}
```

### Proxy Devices

Port forwarding:

```go
InstanceDeviceProxyConfig{
    ListenType:  "tcp",
    ListenAddr:  "0.0.0.0",
    ListenPort:  8080,
    ConnectType: "tcp",
    ConnectAddr: "127.0.0.1",
    ConnectPort: 80,
    Nat:         true,
}
```

### Disk Devices

Storage volumes or bind mounts:

```go
// Named volume
InstanceDeviceDiskConfig{
    StorageVolumeConfig: &StorageVolumeConfig{...},
    Source:              "myvolume",
    Path:                "/data",
    Shift:               true,
}

// Bind mount (StorageVolumeConfig is nil)
InstanceDeviceDiskConfig{
    Source:   "/host/path",
    Path:     "/container/path",
    ReadOnly: true,
    Shift:    true,
}
```

### NIC Devices

Network attachment:

```go
InstanceDeviceConfig{
    DeviceType: InstanceDeviceTypeNic,
    Network:    network,  // reference to Network resource
}
```

## Pre-Devices vs Post-Devices

### Pre-Devices (Devices)

Attached at instance creation:

- Networks (nic)
- Proxies (port forwarding)

### Post-Devices (PostDevices)

Attached after instance creation:

- Storage volumes (need UID/GID for shifting)
- Bind mounts

Post-devices require UID/GID from the created instance to configure proper ownership.

## Instance.Ensure() Flow

```
1. Check if instance exists
   |-- Found: store reference, extract UID/GID, return
   |-- Not found + Create=false: return ErrNotFound

2. Validate bind mounts (reject if remote connection)

3. Check all Resources are ensured
   |-- Any not ensured: return ErrDependencyNotEnsured

4. Build pre-devices map
   |-- Convert Devices + ExtraDevices to Incus format
   |-- Add root disk if not in profile

5. Get image from resource store

6. Copy image from cache to project
   |-- image.CopyTo(projectClient)
   |-- Fast local copy, no registry access

7. Create instance from image
   |-- CreateInstanceFromImage() with pre-devices

8. Extract UID/GID from created instance
   |-- Read oci.uid, oci.gid from config

9. Process post-devices (volumes)
   |-- For each PostDevice where DeviceType=disk:
       |-- If StorageVolumeConfig != nil:
           |-- Set Shifted=true, UID, GID on volume
           |-- StorageVolume.Ensure()
       |-- Convert to Incus device format

10. Update instance with post-devices
    |-- UpdateInstance() with new devices map
```

## UID/GID Shifting

OCI images contain user metadata:

```
oci.uid = 1000
oci.gid = 1000
```

When creating storage volumes for the instance:

```go
volConfig.Shifted = true
volConfig.UID = inst.UID
volConfig.GID = inst.GID
```

This ensures files in the volume are owned by the correct user inside the container.

## Bind Mount Restriction

Bind mounts only work with local (Unix socket) connections:

```go
if dev.Config.Disk.StorageVolumeConfig == nil && client.IsRemote() {
    return ErrBindMountRemote
}
```

The source path must be accessible to the Incus server.

## Instance Lifecycle

### Ensure

```go
err := instance.Ensure(client.OptionCreate())
```

Fetches existing or creates new. Cascades to dependencies via Resources field.

### Start

```go
err := instance.Start()
```

Calls `UpdateInstanceState` with action "start". No-op if already running.

### Stop

```go
err := instance.Stop(client.OptionForce())
```

Calls `UpdateInstanceState` with action "stop". Force bypasses graceful shutdown.

### Delete

```go
err := instance.Delete(client.OptionForce())
```

Deletes the instance. Clears internal state.

## Full Instance Details

When `Config.Full = true`, Ensure fetches additional data:

```go
if r.Config.Full {
    // Fetch image alias
    r.IncusImageAlias = image.IncusAlias

    // Fetch full instance with state and snapshots
    r.IncusInstanceFull, _, _ = client.GetInstanceFull(name)
}
```

Used by the `list` command to display detailed information.

## Dependency Handling

Dependencies are passed via `InstanceConfig.Resources`:

```go
instanceConfig := &InstanceConfig{
    Image:     imageName,
    Resources: []Resource{image, network1, network2},
    Devices:   devices,
}
```

Instance.Ensure() checks all Resources are ensured before creating. It does not cascade Ensure calls - `Stack.Run()` ensures dependencies are ensured first via priority-based ordering. Resources with lower priority values (images, networks) are ensured before higher priority values (instances).
