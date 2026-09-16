package iclient

import (
	"context"
	"net/http"
	"net/url"

	"github.com/lxc/incus/v7/shared/api"
)

// incusNetworkACLsPath is the collection every network ACL call hangs off.
const incusNetworkACLsPath = "/network-acls"

// GetNetworkACLNames returns the names of every network ACL.
func (c *Connection) GetNetworkACLNames(ctx context.Context, project string) ([]string, error) {
	uris := []string{}

	_, err := c.getStruct(ctx, project, incusNetworkACLsPath, nil, &uris)
	if err != nil {
		return nil, err
	}

	return resourceNames(incusNetworkACLsPath, uris)
}

// GetNetworkACLs returns every network ACL, each one whole.
func (c *Connection) GetNetworkACLs(ctx context.Context, project string) ([]api.NetworkACL, error) {
	acls := []api.NetworkACL{}

	query := url.Values{}
	query.Set("recursion", "1")

	_, err := c.getStruct(ctx, project, incusNetworkACLsPath, query, &acls)
	if err != nil {
		return nil, err
	}

	return acls, nil
}

// GetNetworkACL returns one network ACL and its ETag.
func (c *Connection) GetNetworkACL(ctx context.Context, project string, name string) (*api.NetworkACL, string, error) {
	acl := api.NetworkACL{}

	etag, err := c.getStruct(ctx, project, incusNetworkACLsPath+"/"+url.PathEscape(name), nil, &acl)
	if err != nil {
		return nil, "", err
	}

	return &acl, etag, nil
}

// CreateNetworkACL adds a network ACL.
func (c *Connection) CreateNetworkACL(ctx context.Context, project string, acl api.NetworkACLsPost) error {
	_, _, err := c.do(ctx, project, http.MethodPost, incusNetworkACLsPath, nil, acl, "")

	return err
}

// UpdateNetworkACL replaces a network ACL's rules.
func (c *Connection) UpdateNetworkACL(ctx context.Context, project string, name string, acl api.NetworkACLPut, etag string) error {
	_, _, err := c.do(ctx, project, http.MethodPut, incusNetworkACLsPath+"/"+url.PathEscape(name), nil, acl, etag)

	return err
}

// DeleteNetworkACL removes a network ACL.
func (c *Connection) DeleteNetworkACL(ctx context.Context, project string, name string) error {
	_, _, err := c.do(ctx, project, http.MethodDelete, incusNetworkACLsPath+"/"+url.PathEscape(name), nil, nil, "")

	return err
}
