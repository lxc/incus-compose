package main

import "time"

const (
	certFile  = "client.crt"
	keyFile   = "client.key"
	tokenFile = "token"
)

const (
	maxRestartDelay = 60 * time.Second
)

// Default healthcheck settings when keys are missing on the instance.
const (
	defaultRestartDelay  = 5 * time.Second
	defaultInterval      = 30 * time.Second
	defaultTimeout       = 30 * time.Second
	defaultRetries       = 3
	defaultStartPeriod   = 0 * time.Second
	defaultStartInterval = 5 * time.Second
)

// Config holds the healthd configuration.
type Config struct {
	DataDir    string
	SecretsDir string
	IncusURL   string
	Projects   []string
}

// InstanceConfig holds healthcheck configuration for a single instance.
type InstanceConfig struct {
	Test          []string
	StartPeriod   time.Duration
	StartInterval time.Duration
	Interval      time.Duration
	Timeout       time.Duration
	Retries       int
	Restart       bool
	UnlessStopped bool
	RestartDelay  time.Duration
}
