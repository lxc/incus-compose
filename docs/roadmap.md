# Upcoming Work

> This roadmap represents current intentions, not commitments.
> Priorities and scope may change based on feedback and implementation findings.

## Current Capabilities

config/up/down/list works without all the specials other compose solutions provide.

## Planned Improvements

### Remote Handling with Custom Config

- **Status:** Planned
- **Goal:** Add own remote/server configuration management
- **Config Format:**
  - TOML (preferred)
  - YAML (fallback)
- **Features:**
  - Multiple remote servers
  - Connection profiles
  - Cert management
  - Default remote selection

### Progress Reporting to CLI

- **Status:** Planned (depends on "Worker Pool")
- **Goal:** Add real-time progress feedback for long-running operations
- **Features:**
  - Progress bars for image downloads
  - Parallel operation status
  - ETA calculations
  - Detailed operation logs

### Complete Compose Feature Parity

- **Status:** Planned
- **Goal:** Reach 50%+ feature completeness compared to Docker Compose
- **Current Focus Areas:**
  - Service lifecycle (up, down, restart)
  - Networks and volumes
  - Dependencies
- **Missing Features to Consider:**
  - Health checks
  - Resource limits (CPU, memory)
  - Build support (if applicable)
  - Secrets management
  - More volume types
  - Port publishing
  - Environment file handling
  - Service scaling
  - And more...
