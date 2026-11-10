package main

import (
	"context"

	"github.com/a6n-ai/hive"
	"github.com/a6n-ai/hive/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpCmd serves memory tools over stdio until stdin closes.
func mcpCmd() error {
	h, err := hive.Open(hive.Options{Caller: "mcp", Project: defaultProject()})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	return mcpserver.New(h).Run(context.Background(), &mcp.StdioTransport{})
}
