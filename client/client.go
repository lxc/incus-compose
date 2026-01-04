// Package client provides a high-level wrapper around the Incus client library.
//
// This package abstracts the complexities of interacting with Incus servers and provides
// a compose-spec friendly interface for managing instances, networks, volumes, and projects.
package client

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gosimple/slug"
	incusClient "github.com/lxc/incus/v6/client"
)

// Client wraps a project-scoped Incus client with resource management.
type Client struct {
	globalClient *GlobalClient

	project      string
	incusProject string
	created      bool

	incus      *incusClient.ProtocolIncus
	imageCache incusClient.InstanceServer
	logger     *slog.Logger

	// Resource storage
	resources ResourceStore

	// hookBefore is called before any action
	hookBefore func(action Action, r Resource, args Options, err error) error

	// hookAfter is called after any action
	hookAfter func(action Action, r Resource, args Options, err error) error

	hookOperation       func(action Action, r Resource, args Options, op incusClient.Operation, err error) error
	hookRemoteOperation func(action Action, r Resource, args Options, op incusClient.RemoteOperation, err error) error
}

func (c *GlobalClient) newClientProject(name, incusName string, created bool) (*Client, error) {
	incus := c.incus.UseProject(incusName)
	pIncus, ok := incus.(*incusClient.ProtocolIncus)
	if !ok {
		return nil, errors.New("cannot cast project-scoped client to ProtocolIncus")
	}

	p := &Client{
		globalClient: c,
		project:      name,
		incusProject: incusName,
		created:      created,
		incus:        pIncus,
		imageCache:   c.imageCache,
		logger:       c.logger.With("project", name),

		hookBefore: c.hookBefore,
		hookAfter:  c.hookAfter,

		hookOperation:       c.hookOperation,
		hookRemoteOperation: c.hookRemoteOperation,
	}

	if c.IsDebugging() {
		p.logger = p.logger.With("incus_project", incusName)
	}

	c.projects = append(c.projects, p)
	return p, nil
}

// Project returns the user-facing project name.
func (c *Client) Project() string {
	return c.project
}

// IncusProject returns the sanitized Incus project name.
func (c *Client) IncusProject() string {
	return c.incusProject
}

// IsRemote returns true if connected via network (not unix socket).
func (c *Client) IsRemote() bool {
	return c.globalClient.IsRemote()
}

// LogDebug logs an debug message.
// The `any` here is ok.
func (c *Client) LogDebug(msg string, args ...any) {
	c.logger.DebugContext(c.globalClient.Ctx, msg, args...)
}

// LogWarn logs an warning message.
// The `any` here is ok.
func (c *Client) LogWarn(msg string, args ...any) {
	c.logger.WarnContext(c.globalClient.Ctx, msg, args...)
}

// LogError logs an error.
// The `any` here is ok.
func (c *Client) LogError(msg string, args ...any) {
	c.logger.ErrorContext(c.globalClient.Ctx, msg, args...)
}

// Connection returns the project-scoped Connection client.
func (c *Client) Connection() *incusClient.ProtocolIncus {
	return c.incus
}

// GlobalConnection returns the global (non-project-scoped) Incus client.
func (c *Client) GlobalConnection() *incusClient.ProtocolIncus {
	return c.globalClient.incus
}

// Config returns the client config.
func (c *Client) Config() ClientConfig {
	return c.globalClient.Config
}

// Resource returns an existing resource or creates a new one.
func (c *Client) Resource(kind Kind, name string, config Config) (Resource, error) {
	// Check if already in store
	if res := c.resources.Get(kind, name); res != nil {
		return res, nil
	}

	var (
		res Resource
		err error
	)

	switch kind {
	case KindProfile:
		res, err = newProfile(c, name, config)
	case KindNetwork:
		res, err = newNetwork(c, name, config)
	case KindStorageVolume:
		res, err = newStorageVolume(c, name, config)
	case KindImage:
		res, err = newImage(c, name, config)
	case KindInstance:
		res, err = newInstance(c, name, config)

	default:
		return nil, fmt.Errorf("unknown resource %s", kind)
	}

	if err != nil {
		return nil, err
	}

	c.resources.Add(res)
	return res, nil
}

// AddHookBefore adds a hook that will be executed before any action.
// You may use it for abort control.
func (c *Client) AddHookBefore(hook func(action Action, r Resource, args Options, err error) error) {
	prevHook := c.hookBefore
	newHook := func(action Action, r Resource, args Options, err error) error {
		// Run previous hooks FIRST (FIFO)
		if err := prevHook(action, r, args, err); err != nil {
			return err
		}
		// Then run the new hook
		return hook(action, r, args, nil)
	}

	c.hookBefore = newHook
}

// AddHookAfter adds a hook that will be executed after any action (LIFO order).
func (c *Client) AddHookAfter(hook func(action Action, r Resource, args Options, err error) error) {
	prevHook := c.hookAfter
	newHook := func(action Action, r Resource, args Options, err error) error {
		// Run new hook FIRST, then pass result to previous hooks (LIFO)
		err = hook(action, r, args, err)
		return prevHook(action, r, args, err)
	}

	c.hookAfter = newHook
}

// sanitizeProjectName converts a string to a valid Incus project name.
// Replaces underscores with hyphens and removes special characters via slug.
func sanitizeProjectName(name string) string {
	safe := slug.Make(name)
	safe = strings.ReplaceAll(safe, "_", "-")
	return safe
}
