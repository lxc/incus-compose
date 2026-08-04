package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	incus "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"

	"github.com/lxc/incus-compose/shared"
)

// restartPolicies are the restart values worth tracking even without a test command.
var restartPolicies = []string{"always", "on-failure", "unless-stopped"}

// isIgnored reports whether an instance opts out via user.healthcheck.ignore.
func isIgnored(cfg map[string]string) bool {
	return cfg[healthIgnoreKey] == "true"
}

// hasHealthCheck reports whether an instance declares a test command or a restart policy.
func hasHealthCheck(cfg map[string]string) bool {
	return cfg[shared.HealthKeyPrefix+"test"] != "" || slices.Contains(restartPolicies, cfg[shared.HealthKeyPrefix+"restart"])
}

// discover returns the config of every trackable instance in the connection's project.
// Per-instance parse errors are joined and returned; valid instances still register.
func discover(conn incus.InstanceServer) (map[string]instanceConfig, error) {
	instances := map[string]instanceConfig{}

	incusInstances, err := conn.GetInstances(incusApi.InstanceTypeAny)
	if err != nil {
		return instances, fmt.Errorf("listing instances: %w", err)
	}

	slog.Debug("Found instances", "count", len(incusInstances))

	var errs error
	for _, inst := range incusInstances {
		if isIgnored(inst.Config) {
			continue
		}

		if !hasHealthCheck(inst.Config) {
			slog.Debug("Skipping instance: no test and no restart", "instance", inst.Name)
			continue
		}

		cfg, err := parseInstance(inst.Config, inst.StatusCode == incusApi.Running)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("%s: %w", inst.Name, err))
			continue
		}

		slog.Debug("Found instance", "instance", inst.Name, "config", cfg)
		instances[inst.Name] = cfg
	}

	return instances, errs
}

// parseInstance decodes user.healthcheck.* keys into an instanceConfig, applying defaults.
func parseInstance(cfg map[string]string, running bool) (instanceConfig, error) {
	ic := instanceConfig{
		StartPeriod:   defaultStartPeriod,
		StartInterval: defaultStartInterval,
		Interval:      defaultInterval,
		Timeout:       defaultTimeout,
		Retries:       defaultRetries,
		Restart:       cfg[shared.HealthKeyPrefix+"restart"],
		Running:       running,
	}

	testRaw := cfg[shared.HealthKeyPrefix+"test"]
	if testRaw == "" && slices.Contains(restartPolicies, ic.Restart) {
		// Restart policy without a test: probe with a no-op so run state is watched.
		testRaw = `["NONE"]`
	}

	if testRaw != "" {
		if err := json.Unmarshal([]byte(testRaw), &ic.Test); err != nil {
			return ic, fmt.Errorf("parsing test: %w", err)
		}
	}

	if len(ic.Test) > 0 && ic.Test[0] == "CMD-SHELL" && len(ic.Test) < 2 {
		return ic, errors.New("CMD-SHELL requires a command")
	}

	if v := cfg[shared.HealthKeyPrefix+"start_period"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return ic, fmt.Errorf("parsing start_period: %w", err)
		}
		ic.StartPeriod = d
	}

	if v := cfg[shared.HealthKeyPrefix+"start_interval"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return ic, fmt.Errorf("parsing start_interval: %w", err)
		}
		if d <= 0 {
			return ic, errors.New("start_interval must be greater than 0")
		}
		ic.StartInterval = d
	}

	if v := cfg[shared.HealthKeyPrefix+"interval"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return ic, fmt.Errorf("parsing interval: %w", err)
		}
		if d <= 0 {
			return ic, errors.New("interval must be greater than 0")
		}
		ic.Interval = d
	}

	if v := cfg[shared.HealthKeyPrefix+"timeout"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return ic, fmt.Errorf("parsing timeout: %w", err)
		}
		ic.Timeout = d
	}

	if v := cfg[shared.HealthKeyPrefix+"retries"]; v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return ic, fmt.Errorf("parsing retries: %w", err)
		}
		if n == 0 {
			return ic, errors.New("retries must be greater than 0")
		}
		ic.Retries = int(n)
	}

	return ic, nil
}

// baseRestartDelay is interval*retries, clamped to [defaultRestartDelay, maxRestartDelay].
func baseRestartDelay(cfg instanceConfig) time.Duration {
	if cfg.Interval <= 0 || cfg.Retries <= 0 {
		return defaultRestartDelay
	}
	return max(min(cfg.Interval*time.Duration(cfg.Retries), maxRestartDelay), defaultRestartDelay)
}
