# Client Package API Reference

Compact reference for the public API of the `client` package.

## Core Types

### Resource Identification

```go
type Kind string
const (
    KindProject       Kind = "project"
    KindProfile       Kind = "profile"
    KindImage         Kind = "image"
    KindStorageVolume Kind = "storage-volume"
    KindNetwork       Kind = "network"
    KindInstance      Kind = "instance"
)

type Action string
const (
    ActionEnsure Action = "ensure"
    ActionDelete Action = "delete"
    ActionStart  Action = "start"
    ActionStop   Action = "stop"
)
```

### Resource Interface

```go
type Resource interface {
    Kind() Kind
    Name() string
    IncusName() string
    Priority() int
    IsEnsured() bool
}

type EnsureAble interface { Ensure(opts ...Option) error }
type StartAble  interface { Start(opts ...Option) error }
type StopAble   interface { Stop(opts ...Option) error }
type DeleteAble interface { Delete(opts ...Option) error }
```

### Priority Constants

Lower values create first, delete last:

```go
const (
    PriorityProject  = 256   // 1 << 8
    PriorityProfile  = 512   // 1 << 9
    PriorityImage    = 1024  // 1 << 10
    PriorityNetwork  = 2048  // 1 << 11
    PriorityVolume   = 4096  // 1 << 12
    PriorityInstance = 8192  // 1 << 13
)
```

## Client Creation

### GlobalClient

```go
func New(ctx context.Context, opts ...ClientOption) *GlobalClient

// Options
func ClientURL(u string) ClientOption
func ClientLogger(l *slog.Logger) ClientOption
func ClientInsecureSkipVerify() ClientOption
func ClientTLSClientCert(f string) ClientOption
func ClientTLSClientKey(f string) ClientOption
func ClientDefaultStoragePool(n string) ClientOption
func ClientNetworkPrefix(n string) ClientOption
func ClientDescriptionFormat(n string) ClientOption
func ClientProvideConnection(instances, cache incusClient.InstanceServer) ClientOption

// Methods
func (*GlobalClient) Connect() error
func (*GlobalClient) IsConnected() bool
func (*GlobalClient) IsRemote() bool
func (*GlobalClient) EnsureProject(name string, create bool) (*Client, error)
func (*GlobalClient) DeleteProject(name string, force bool) error
func (*GlobalClient) AddHookBefore(hook func(Action, Resource, Options, error) error)
func (*GlobalClient) AddHookAfter(hook func(Action, Resource, Options, error) error)
```

### Project-Scoped Client

```go
// Methods
func (*Client) Project() string
func (*Client) IncusProject() string
func (*Client) IsRemote() bool
func (*Client) Config() ClientConfig
func (*Client) Connection() *incusClient.ProtocolIncus
func (*Client) GlobalConnection() *incusClient.ProtocolIncus
func (*Client) Resource(kind Kind, name string, config Config) (Resource, error)
func (*Client) AddHookBefore(hook func(Action, Resource, Options, error) error)
func (*Client) AddHookAfter(hook func(Action, Resource, Options, error) error)
```

## Resource Management

### Options

```go
type Options struct {
    Create  bool // Create if not exists (ActionEnsure)
    Force   bool // Force deletion/stop
    Timeout int  // Timeout in seconds
}

func OptionCreate() Option
func OptionForce() Option
func OptionTimeout(t int) Option
func NewOptions(opts ...Option) Options
```

### Helper Functions

```go
func ByKind[T Resource](resources []Resource, kind Kind) ([]T, error)
func FilterDuplicates(resources []Resource) []Resource
func SupportsAction(r Resource, action Action) bool
func RunAction(r Resource, action Action, opts ...Option) error
```

### BaseResource

```go
func NewBaseResource(kind Kind, name string, priority int) *BaseResource

func (*BaseResource) Kind() Kind
func (*BaseResource) Name() string
func (*BaseResource) Priority() int
```

## Stack Execution

```go
func NewStack(p *Client, opts ...StackOption) *Stack

// Options
func StackWorkers(w int) StackOption
func StackSortDescending() StackOption

// Methods
func (*Stack) Add(resources ...Resource) *Stack
func (*Stack) All() []Resource
func (*Stack) Run(action Action, opts ...Option) error
func (*Stack) ForAction(action Action) *Stack
```

## Worker Pool

```go
func NewWorkerPool(workers int) *WorkerPool

func (*WorkerPool) Submit(fn func() error)
func (*WorkerPool) Run(args PoolRunArgs) error

type PoolRunArgs struct {
    FailFast bool
}
```

## Resource Types

### Image

```go
type ImageConfig struct {
    Source incusClient.ImageServer
    Cache  incusClient.InstanceServer
    Remote string
    Image  string
}

type Image struct {
    Config     ImageConfig
    IncusAlias *incusApi.ImageAliasesEntry
    ETag       string
}

func (*Image) IncusName() string
func (*Image) IsEnsured() bool
func (*Image) Ensure(opts ...Option) error
func (*Image) Delete(opts ...Option) error
```

### Instance

```go
type InstanceConfig struct {
    Type        incusApi.InstanceType
    Full        bool
    Image       string
    Resources   []Resource
    Devices     []InstanceDevice
    PostDevices []InstanceDevice
    Config      map[string]string
    ExtraDevices map[string]map[string]string
}

type Instance struct {
    Config            InstanceConfig
    IncusInstance     *incusApi.Instance
    ETag              string
    UID               uint32
    GID               uint32
    IncusInstanceFull *incusApi.InstanceFull
    IncusImageAlias   *incusApi.ImageAliasesEntry
}

func (*Client) Instance(name string, config InstanceConfig) (*Instance, error)

func (*Instance) IncusName() string
func (*Instance) IsEnsured() bool
func (*Instance) HasFull() bool
func (*Instance) Ensure(opts ...Option) error
func (*Instance) Start(opts ...Option) error
func (*Instance) Stop(opts ...Option) error
func (*Instance) Delete(opts ...Option) error
```

### Instance Devices

```go
const (
    InstanceDeviceTypeProxy = "proxy"
    InstanceDeviceTypeDisk  = "disk"
    InstanceDeviceTypeNic   = "nic"
)

type InstanceDeviceProxyConfig struct {
    ListenType  string
    ListenAddr  string
    ListenPort  uint32
    ConnectType string
    ConnectAddr string
    ConnectPort uint32
    Nat         bool
}

type InstanceDeviceDiskConfig struct {
    StorageVolumeConfig *StorageVolumeConfig
    Source              string
    Path                string
    Shift               bool
    ReadOnly            bool
}

type InstanceDeviceConfig struct {
    DeviceType  string
    Network     Resource
    Proxy       InstanceDeviceProxyConfig
    Disk        InstanceDeviceDiskConfig
    ExtraConfig map[string]string
}

type InstanceDevice struct {
    Name   string
    Config InstanceDeviceConfig
}

func (*InstanceDevice) ToIncusDevice() (string, map[string]string, *Error)
```

### Network

```go
type NetworkConfig struct {
    Type string // Default: "bridge"
}

type Network struct {
    Config       NetworkConfig
    IncusNetwork *incusApi.Network
    ETag         string
}

func (*Network) IncusName() string
func (*Network) IsEnsured() bool
func (*Network) Ensure(opts ...Option) error
func (*Network) Delete(opts ...Option) error
```

### Profile

```go
type ProfileConfig struct {
    SourceServer  *incusClient.ProtocolIncus
    SourceProject string
    SourceProfile string
}

type Profile struct {
    Config       ProfileConfig
    IncusProfile *incusApi.Profile
    ETag         string
}

func (*Profile) IncusName() string
func (*Profile) IsEnsured() bool
func (*Profile) Ensure(opts ...Option) error
func (*Profile) HasDevice(name string) bool
func (*Profile) Delete(opts ...Option) error
```

### Storage Volume

```go
type StorageVolumeConfig struct {
    Pool        string
    Shifted     bool
    UID         uint32
    GID         uint32
    ExtraConfig map[string]string
}

type StorageVolume struct {
    Config      StorageVolumeConfig
    IncusVolume *incusApi.StorageVolume
    ETag        string
}

func (*StorageVolume) IncusName() string
func (*StorageVolume) IsEnsured() bool
func (*StorageVolume) Ensure(opts ...Option) error
func (*StorageVolume) Delete(opts ...Option) error
```

## Error Handling

### Sentinel Errors

```go
var (
    ErrUnsupportedAction    = NewError("resource does not support action")
    ErrUnknown              = NewError("unknown")
    ErrUnknownConfig        = NewError("unknown config for resource")
    ErrNilPointer           = NewError("found a nil pointer")
    ErrBadDeviceConfig      = NewError("bad config for device")
    ErrDependencyNotEnsured = NewError("dependency not ensured")
    ErrDisconnected         = NewError("client is not connected")
    ErrConnectionFailed     = NewError("connection failed")
    ErrAborted              = NewError("operation aborted")
    ErrNotFound             = NewError("resource not found")
    ErrNotEnsured           = NewError("resource not ensured")
    ErrImageRequired        = NewError("instances without an image are not yet supported")
    ErrBindMountRemote      = NewError("bind mounts not supported over network connection")
)
```

### Error Methods

```go
func NewError(text string) *Error

func (*Error) WithKindName(kind Kind, name string) *Error
func (*Error) WithText(text string) *Error
func (*Error) WithAction(action Action) *Error
func (*Error) WithResource(resource Resource) *Error
func (*Error) Error() string
func (*Error) Unwrap() error
func (*Error) Wrap(wrapped error) *Error
func (*Error) Is(target error) bool

// Usage
if errors.Is(err, client.ErrNotFound) { }
```

## Usage Pattern

```go
// 1. Create global client
globalClient := client.New(ctx,
    client.ClientURL("https://..."),
    client.ClientTLSClientCert("cert.pem"),
    client.ClientTLSClientKey("key.pem"),
)
globalClient.Connect()

// 2. Get or create project
proj, _ := globalClient.EnsureProject("myproject", true)

// 3. Create resources
img, _ := proj.Resource(client.KindImage, "docker.io/alpine", &client.ImageConfig{})
net, _ := proj.Resource(client.KindNetwork, "backend", &client.NetworkConfig{})
inst, _ := proj.Resource(client.KindInstance, "web", &client.InstanceConfig{
    Image: "docker.io/alpine",
    Resources: []client.Resource{img, net},
})

// 4. Execute with stack
stack := client.NewStack(proj)
stack.Add(img, net, inst)
stack.Run(client.ActionEnsure, client.OptionCreate())
stack.Run(client.ActionStart)

// 5. Cleanup
stack.ForAction(client.ActionStop).Run(client.ActionStop)
stack.ForAction(client.ActionDelete).Run(client.ActionDelete, client.OptionForce())
globalClient.DeleteProject("myproject", true)
```
