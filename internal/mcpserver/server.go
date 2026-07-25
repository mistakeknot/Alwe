// Package mcpserver exposes CASS observations as an MCP server.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/mistakeknot/Alwe/pkg/sessionsearch"
	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server exposes session search as an MCP server.
type Server struct {
	mcp    *gomcp.Server
	search *sessionsearch.Service
}

// New creates an MCP sidecar server. It starts as long as at least one search
// backend is reachable, so a missing or wedged cass no longer prevents the
// server from coming up.
func New() (*Server, error) {
	svc, err := sessionsearch.New()
	if err != nil {
		return nil, fmt.Errorf("session search: %w", err)
	}

	s := &Server{
		mcp: gomcp.NewServer(
			&gomcp.Implementation{Name: "alwe", Version: "0.2.0"},
			nil,
		),
		search: svc,
	}
	s.registerTools()
	return s, nil
}

// Close releases the server's backends.
func (s *Server) Close() error { return s.search.Close() }

func (s *Server) registerTools() {
	s.mcp.AddTool(
		&gomcp.Tool{
			Name:        "search_sessions",
			Description: "Search agent sessions by query. Optionally filter by agent connector (claude_code, codex, gemini, amp, etc.). Merges cass and the local catalog; serves local-only results when cass is unavailable, flagged via the response's degraded/notice fields.",
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"query":     {Type: "string", Description: "Search query for session content"},
					"connector": {Type: "string", Description: "Filter by agent connector (claude_code, codex, gemini, amp, aider, etc.)"},
					"limit":     {Type: "integer", Description: "Maximum results (default 5)"},
				},
				Required: []string{"query"},
			},
		},
		func(ctx context.Context, req *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
			var in struct {
				Query     string `json:"query"`
				Connector string `json:"connector"`
				Limit     int    `json:"limit"`
			}
			if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
				return errResult("invalid input: " + err.Error()), nil
			}
			limit := in.Limit
			if limit == 0 {
				limit = 5
			}
			res, err := s.search.Search(ctx, in.Query, in.Connector, limit)
			if err != nil {
				return errResult(err.Error()), nil
			}
			out, _ := json.MarshalIndent(res, "", "  ")
			return textResult(string(out)), nil
		},
	)

	s.mcp.AddTool(
		&gomcp.Tool{
			Name:        "context_for_file",
			Description: "Find agent sessions that touched a specific file path.",
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"file_path": {Type: "string", Description: "File path to find related sessions for"},
					"limit":     {Type: "integer", Description: "Maximum results (default 5)"},
				},
				Required: []string{"file_path"},
			},
		},
		func(ctx context.Context, req *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
			var in struct {
				FilePath string `json:"file_path"`
				Limit    int    `json:"limit"`
			}
			if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
				return errResult("invalid input: " + err.Error()), nil
			}
			limit := in.Limit
			if limit == 0 {
				limit = 5
			}
			res, err := s.search.ContextForFile(ctx, in.FilePath, limit)
			if err != nil {
				return errResult(err.Error()), nil
			}
			out, _ := json.MarshalIndent(res, "", "  ")
			return textResult(string(out)), nil
		},
	)

	s.mcp.AddTool(
		&gomcp.Tool{
			Name:        "export_session",
			Description: "Export an agent session to markdown format.",
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"session_path": {Type: "string", Description: "Path to the session JSONL file"},
				},
				Required: []string{"session_path"},
			},
		},
		func(ctx context.Context, req *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
			var in struct {
				SessionPath string `json:"session_path"`
			}
			if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
				return errResult("invalid input: " + err.Error()), nil
			}
			md, err := s.search.ExportSession(ctx, in.SessionPath)
			if err != nil {
				return errResult(err.Error()), nil
			}
			return textResult(md), nil
		},
	)

	s.mcp.AddTool(
		&gomcp.Tool{
			Name:        "timeline",
			Description: "Show recent agent activity timeline. Defaults to last 1 hour.",
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"since": {Type: "string", Description: "Time range (e.g. 1h, 2d, 1w). Default: 1h"},
				},
			},
		},
		func(ctx context.Context, req *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
			var in struct {
				Since string `json:"since"`
			}
			if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
				return errResult("invalid input: " + err.Error()), nil
			}
			since := in.Since
			if since == "" {
				since = "1h"
			}
			tl, err := s.search.Timeline(ctx, since)
			if err != nil {
				return errResult(err.Error()), nil
			}
			return textResult(tl), nil
		},
	)

	s.mcp.AddTool(
		&gomcp.Tool{
			Name:        "health",
			Description: "Report search backend availability (cass and local catalog), local catalog coverage, and the running binary build id.",
			InputSchema: &jsonschema.Schema{Type: "object"},
		},
		func(ctx context.Context, req *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
			out, _ := json.MarshalIndent(s.search.Health(ctx), "", "  ")
			return textResult(string(out)), nil
		},
	)
}

// Run starts the MCP server on stdio transport. Blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	return s.mcp.Run(ctx, &gomcp.StdioTransport{})
}

func textResult(text string) *gomcp.CallToolResult {
	return &gomcp.CallToolResult{
		Content: []gomcp.Content{&gomcp.TextContent{Text: text}},
	}
}

func errResult(msg string) *gomcp.CallToolResult {
	return &gomcp.CallToolResult{
		Content: []gomcp.Content{&gomcp.TextContent{Text: "error: " + msg}},
		IsError: true,
	}
}
