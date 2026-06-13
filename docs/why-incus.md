# Why Incus?

Incus excels at running system containers and VMs, but most tooling today assumes
OCI engines. `incus-compose` bridges that gap efficiently.

## The Problem

Running OCI engines inside Incus containers is a common pattern, but it's wasteful:

```
┌─────────────────────────────┐
│    Incus Container          │
│  ┌──────────────────────┐   │
│  │   OCI Engine Daemon  │   │
│  │  ┌────────────────┐  │   │
│  │  │  Your App      │  │   │
│  │  └────────────────┘  │   │
│  └──────────────────────┘   │
└─────────────────────────────┘
```

**Problems with this approach:**

- Double overhead (two container runtimes)
- Nested namespaces add complexity
- Security concerns with privileged nested containers
- Storage inefficiency from layered filesystems

## The Solution

Run OCI images directly on Incus with `incus-compose`:

```
┌─────────────────────────────┐
│          Incus              │
│  ┌────────────────┐         │
│  │   Your App     │         │
│  └────────────────┘         │
└─────────────────────────────┘
```

**Benefits:**

- Single layer of containerization
- Native Incus efficiency and security
- Same compose files you already use
- No wasted resources

## Key Advantages

| Feature        | OCI Engines               | Incus                            |
| -------------- | ------------------------- | -------------------------------- |
| Container type | Application (PID 1 = app) | System (full init)               |
| Isolation      | Namespaces only           | LXC namespaces + cgroups         |
| Security       | Varying models            | VM-like isolation                |
| Networking     | Port mapping via iptables | Real IPs and port proxies        |
| Storage        | Overlay filesystem        | ZFS/Btrfs with instant snapshots |
| Image caching  | Per-engine cache          | Global or per-project            |

## Scale Beyond a Single Host

**Incus clustering:**

- Scale from 1 to 100+ bare metal hosts
- Single API endpoint for the entire cluster
- Automatic instance placement and load balancing
- Live migration between hosts
- No complex orchestration layer needed

**IncusOS:**

- Immutable OS purpose-built for Incus
- Safe, predictable updates
- Minimal attack surface
- Production-ready out of the box

## When to Use Incus

**Choose Incus when:**

- You need to shell into containers for debugging
- You want true RW volumes (not Kubernetes volume limitations)
- You need real network addresses (no port conflicts)
- You want VM-like isolation without VM overhead
- You need ZFS/Btrfs snapshots and clones
- You're running apps that expect a full OS environment
- Security and multi-tenancy are priorities
- You're already using Incus for infrastructure
- You need to scale from dev laptop to production cluster seamlessly

**Stick with OCI engines when:**

- You're targeting Kubernetes deployment
- You need the absolute broadest ecosystem compatibility

## See Also

- [Getting Started](getting-started.md) - install and run your first project
- [Compose Compatibility](compose-compatibility.md) - what works and what does not
