package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"time"

	incus "github.com/lxc/incus/v6/client"
)

// Config holds the healthd configuration.
type Config struct {
	DataDir    string                   `json:"data_dir"`
	SecretsDir string                   `json:"secrets_dir"`
	Debug      bool                     `json:"debug"`
	IncusURL   string                   `json:"incus_url"`
	Projects   []string                 `json:"projects"`
	Services   map[string]ServiceConfig `json:"services"`
}

// ServiceConfig holds healthcheck configuration for a single service.
// The service is identified by the key used in Config.Services.
type ServiceConfig struct {
	Test     []string `json:"test"`
	Interval Duration `json:"interval"`
	Timeout  Duration `json:"timeout"`
	Retries  int      `json:"retries"`
	Restart  bool     `json:"restart"`
}

// Duration wraps time.Duration for JSON unmarshaling.
type Duration time.Duration

// UnmarshalJSON parses a duration string like "30s" or "1m".
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}

	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}

	*d = Duration(parsed)
	return nil
}

// MarshalJSON formats the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Duration returns the underlying time.Duration.
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

// Default healthcheck settings when keys are missing on the instance.
const (
	defaultInterval = 5 * time.Second
	defaultTimeout  = 5 * time.Second
	defaultRetries  = 3
)

// Discover replaces c.Services with the set of healthchecks declared on
// instances in the project the client is scoped to. Instances carrying
// user.healthcheck.daemon=true are skipped (the healthd itself).
// Instances without user.healthcheck.test are skipped (no healthcheck).
// Per-instance parse errors are collected and returned as a joined error;
// valid services are still registered so one broken service cannot stop
// the daemon.
func (c *Config) Discover(client incus.InstanceServer) error {
	instances, err := client.GetInstancesFull("")
	if err != nil {
		return fmt.Errorf("listing instances: %w", err)
	}

	services := make(map[string]ServiceConfig, len(instances))
	var errs error

	for _, inst := range instances {
		if inst.Config["user.healthcheck.daemon"] == "true" {
			continue
		}

		log.Printf("%s, %v, %v", inst.Name, inst.Config["user.restart"], inst.Config["user.healthcheck.test"])

		restart := false
		if slices.Contains([]string{"always", "on-failure", "on-failure:3", "unless-stopped"}, inst.Config["user.restart"]) {
			restart = true
			if inst.Config["user.healthcheck.test"] == "" {
				inst.Config["user.healthcheck.test"] = "[\"NONE\"]"
			}
		}

		if inst.Config["user.healthcheck.test"] == "" && !restart {
			continue
		}

		svc, err := parseServiceConfig(inst.Config)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("%s: %w", inst.Name, err))
			continue
		}

		services[inst.Name] = svc
	}

	c.Services = services
	return errs
}

// parseServiceConfig decodes user.healthcheck.* keys into a ServiceConfig.
// Missing optional keys fall back to sensible defaults.
func parseServiceConfig(cfg map[string]string) (ServiceConfig, error) {
	svc := ServiceConfig{
		Interval: Duration(defaultInterval),
		Timeout:  Duration(defaultTimeout),
		Retries:  defaultRetries,
	}

	if err := json.Unmarshal([]byte(cfg["user.healthcheck.test"]), &svc.Test); err != nil {
		return svc, fmt.Errorf("parsing test: %w", err)
	}

	if v := cfg["user.healthcheck.interval"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return svc, fmt.Errorf("parsing interval: %w", err)
		}
		svc.Interval = Duration(d)
	}

	if v := cfg["user.healthcheck.timeout"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return svc, fmt.Errorf("parsing timeout: %w", err)
		}
		svc.Timeout = Duration(d)
	}

	if v := cfg["user.healthcheck.retries"]; v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return svc, fmt.Errorf("parsing retries: %w", err)
		}
		svc.Retries = int(n)
	}

	if slices.Contains([]string{"always", "on-failure", "on-failure:3", "unless-stopped"}, cfg["user.restart"]) {
		svc.Restart = true
	}

	return svc, nil
}
