package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func (s *Server) registerResources(srv *mcpserver.MCPServer) {
	srv.AddResource(
		mcp.Resource{
			URI:         "nexus://sessions",
			Name:        "Active Sessions",
			Description: "All active terminal sessions in the Nexus",
			MIMEType:    "application/json",
		},
		s.resourceSessions,
	)
}

func (s *Server) resourceSessions(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	ids := s.registry.List()
	data, _ := json.MarshalIndent(map[string]any{
		"count":    len(ids),
		"sessions": ids,
	}, "", "  ")
	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      "nexus://sessions",
			MIMEType: "application/json",
			Text:     fmt.Sprintf("%s", data),
		},
	}, nil
}
