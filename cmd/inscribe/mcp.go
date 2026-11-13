package main

import (
	"context"
	"flag"
	"time"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpCmd serves memory tools over stdio until stdin closes. The tools act on
// one project, the --project directory or else the current one.
func mcpCmd(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	project := fs.String("project", defaultProject(), "project directory the tools act on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := inscribe.Open(inscribe.Options{Caller: "mcp", Project: *project})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	if err := h.MarkMCPServed(context.Background(), time.Now()); err != nil {
		return err
	}
	return mcpserver.New(h, *project).Run(context.Background(), &mcp.StdioTransport{})
}
