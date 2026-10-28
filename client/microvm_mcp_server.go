package client

import (
	"context"
	"fmt"
	"net/url"
)

func (c *Client) ListMicrovmMcpProviders(ctx context.Context) ([]map[string]any, error) {
	return c.microvmStrictList(ctx, "/microvm/mcp-providers", "data")
}
func (c *Client) ListMicrovmMcpServers(ctx context.Context) ([]map[string]any, error) {
	return c.microvmStrictList(ctx, "/microvm/mcp-servers", "data")
}
func (c *Client) CreateMicrovmMcpServer(ctx context.Context, body map[string]any) (map[string]any, error) {
	row, err := c.doItemOnce(ctx, "POST", "/microvm/mcp-servers", body, "data")
	return microvmMcpResult(row, err, body)
}
func microvmMcpPath(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("microVM MCP server: empty id")
	}
	return "/microvm/mcp-server/" + url.PathEscape(id), nil
}
func (c *Client) GetMicrovmMcpServer(ctx context.Context, id string) (map[string]any, error) {
	path, err := microvmMcpPath(id)
	if err != nil {
		return nil, err
	}
	row, err := c.doItem(ctx, "GET", path, nil, "data")
	return microvmSafeResult(row, err, "")
}
func (c *Client) UpdateMicrovmMcpServer(ctx context.Context, id string, body map[string]any) (map[string]any, error) {
	path, err := microvmMcpPath(id)
	if err != nil {
		return nil, err
	}
	row, err := c.doItemOnce(ctx, "PUT", path, body, "data")
	return microvmMcpResult(row, err, body)
}
func (c *Client) DeleteMicrovmMcpServer(ctx context.Context, id string) error {
	path, err := microvmMcpPath(id)
	if err != nil {
		return err
	}
	return c.doVoidOnce(ctx, "DELETE", path, nil)
}

// Connect starts browser authorization; it does not establish a connection.
func (c *Client) ConnectMicrovmMcpServer(ctx context.Context, id string, preset *string) (map[string]any, error) {
	path, err := microvmMcpPath(id)
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	if preset != nil {
		body["scope_preset"] = *preset
	}
	return c.doItemOnce(ctx, "POST", path+"/connect", body, "")
}
func (c *Client) DisconnectMicrovmMcpServer(ctx context.Context, id string) (map[string]any, error) {
	path, err := microvmMcpPath(id)
	if err != nil {
		return nil, err
	}
	row, err := c.doItemOnce(ctx, "POST", path+"/disconnect", nil, "data")
	return microvmSafeResult(row, err, "")
}

// Rotate accepts a supplied token. The Master returns metadata only, never a token.
func (c *Client) RotateMicrovmMcpServerToken(ctx context.Context, id, token string) (map[string]any, error) {
	path, err := microvmMcpPath(id)
	if err != nil {
		return nil, err
	}
	row, err := c.doItemOnce(ctx, "POST", path+"/rotate-token", map[string]any{"static_token": token}, "data")
	return microvmSafeResult(row, err, token)
}

// Invalid credential headers can be reflected by upstream validation. Drop error
// prose for every header-carrying write, but preserve legitimate nonsecret headers
// in successful metadata responses.
func microvmMcpResult(row map[string]any, err error, body map[string]any) (map[string]any, error) {
	token := microvmBodySecret(body, "static_token")
	if err != nil && token == "" {
		switch headers := body["headers"].(type) {
		case map[string]string:
			for _, value := range headers {
				if value != "" {
					token = value
					break
				}
			}
		case map[string]any:
			for _, value := range headers {
				if s, ok := value.(string); ok && s != "" {
					token = s
					break
				}
			}
		}
	}
	return microvmSafeResult(row, err, token)
}
