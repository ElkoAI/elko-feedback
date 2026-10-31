//go:build ignore

// Snippet: a `submit_feedback` MCP tool for an Elko MCP server, using
// github.com/mark3labs/mcp-go. Copy into your server and register it.
package main

import (
	"context"
	"fmt"

	"github.com/ElkoAI/elko-feedback/pkg/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const channel = "elko.mcp.news" // one dotted channel per product
var version = "dev"

func registerFeedback(s *server.MCPServer) {
	fb := client.New("https://feedback.elko.ai")
	tool := mcp.NewTool("submit_feedback",
		mcp.WithDescription("Send feedback about this MCP server to the Elko team (bugs, ideas, data quality). Only call when the user explicitly wants to send feedback."),
		mcp.WithString("message", mcp.Required(), mcp.Description("The feedback text, in the user's words")),
		mcp.WithNumber("rating", mcp.Description("Optional 1-5 rating")),
		mcp.WithString("category", mcp.Description("bug | idea | data quality | praise | other")),
		mcp.WithString("email", mcp.Description("Optional reply-to email, only if the user offers it")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f := client.Feedback{Channel: channel, Message: req.GetString("message", ""), Category: req.GetString("category", ""),
			Email: req.GetString("email", ""), AppVersion: version}
		if r := req.GetInt("rating", 0); r > 0 {
			f.Rating = &r
		}
		id, err := fb.Submit(ctx, f)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Thanks! Feedback #%d recorded. Longer notes: %s", id, fb.FormURL(channel))), nil
	})
}
