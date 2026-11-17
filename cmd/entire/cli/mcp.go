package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/entireio/cli/cmd/entire/cli/strategy"

	"github.com/spf13/cobra"

	"github.com/entireio/cli/cmd/entire/cli/versioninfo"
)

// `entire mcp` runs a Model Context Protocol (MCP) server over stdio so that
// "MCP-host" agents — agents with no entire hook or context-injection channel —
// can reach entire's machine-readable surface as MCP tools. It is the active
// counterpart to the passive `entire status` / `entire help` discovery path: the
// host launches `entire mcp` as a stdio server and calls the agent_help and
// entire_status tools. The server is read-only and reuses the same live
// agent-help / status rendering the CLI uses, so it always matches the installed
// binary. Transport is newline-delimited JSON-RPC 2.0 (the MCP stdio framing).

// mcpProtocolVersion is the MCP revision we advertise when a client doesn't
// request one. We echo the client's requested version when present.
const mcpProtocolVersion = "2025-06-18"

// mcpModernProtocolVersion is the modern MCP revision (the stateless model of
// SEP-2575 / the 2026-07-28 specification) whose per-request surface this
// server additionally speaks alongside the legacy initialize handshake above.
const mcpModernProtocolVersion = "2026-07-28"

// Per-request _meta keys the modern era requires on every stateless request
// (validated before serving one), and the key under which modern results
// report server identity. The io.modelcontextprotocol/ namespace is reserved
// by the specification, so a legacy client's _meta (e.g. progressToken) never
// collides with it.
const (
	mcpMetaKeyPrefix             = "io.modelcontextprotocol/"
	mcpMetaKeyProtocolVersion    = mcpMetaKeyPrefix + "protocolVersion"
	mcpMetaKeyClientInfo         = mcpMetaKeyPrefix + "clientInfo"
	mcpMetaKeyClientCapabilities = mcpMetaKeyPrefix + "clientCapabilities"
	mcpMetaKeyServerInfo         = mcpMetaKeyPrefix + "serverInfo"
)

// mcpCacheScopePublic marks discovery and tools/list results as shareable
// across clients: their content depends only on the binary, not per-client
// state.
const mcpCacheScopePublic = "public"

// mcpCacheTTLMS bounds how long a client may cache a discovery or tools/list
// result. The tool registry is static for the life of the binary (mcpToolDefs
// varies only with the installed version), so cacheScope "public" and a
// five-minute TTL are safe: nothing served here goes stale short of a binary
// upgrade, which restarts the server anyway.
const mcpCacheTTLMS = 5 * 60 * 1000

// maxMCPMessageBytes bounds a single newline-delimited JSON-RPC message so a
// malformed or abusive line can't exhaust memory. 1 MiB is far above any real
// agent_help / status request.
const maxMCPMessageBytes = 1 << 20

// mcpServerName is the MCP serverInfo name advertised at initialize.
const mcpServerName = "entire"

func newMCPCmd(rootCmd *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run a Model Context Protocol server for MCP-host agents",
		Long: `Runs a Model Context Protocol (MCP) server over stdio. Configure an MCP host to
launch "entire mcp" as a stdio server; it exposes entire's agent-help and status
as MCP tools so agents without a hook or context-injection channel can discover
and use entire. Read-only; speaks newline-delimited JSON-RPC 2.0.`,
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runMCPServer(c.Context(), rootCmd, c.InOrStdin(), c.OutOrStdout())
		},
	}
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonRPCVersion is the only JSON-RPC version this server speaks.
const jsonRPCVersion = "2.0"

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	// Data carries structured error detail (e.g. UnsupportedProtocolVersionError's
	// requested/supported); omitted for the plain legacy errors.
	Data any `json:"data,omitempty"`
}

// runMCPServer reads newline-delimited JSON-RPC 2.0 messages from in (one per
// line — the MCP stdio framing), bounding each line to maxMCPMessageBytes, and
// writes responses to out until EOF. Notifications (messages with no id) get no
// response, per JSON-RPC. An unparseable line — including a JSON-RPC batch array,
// which this server does not support — yields a parse error and the server
// recovers to the next line rather than terminating.
func runMCPServer(ctx context.Context, rootCmd *cobra.Command, in io.Reader, out io.Writer) error {
	enc := json.NewEncoder(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), maxMCPMessageBytes)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}

		var req mcpRequest
		if err := json.Unmarshal(line, &req); err != nil {
			if encErr := enc.Encode(mcpResponse{JSONRPC: jsonRPCVersion, ID: json.RawMessage("null"), Error: &mcpError{Code: -32700, Message: "parse error"}}); encErr != nil {
				return fmt.Errorf("write mcp parse-error response: %w", encErr)
			}
			continue
		}

		// Reject a parseable-but-invalid request (missing/incorrect jsonrpc version
		// or empty method) with -32600 before dispatch, per JSON-RPC, rather than
		// treating it as method-not-found.
		if req.JSONRPC != jsonRPCVersion || req.Method == "" {
			id := req.ID
			if len(id) == 0 {
				id = json.RawMessage("null")
			}
			if encErr := enc.Encode(mcpResponse{JSONRPC: jsonRPCVersion, ID: id, Error: &mcpError{Code: -32600, Message: "invalid request"}}); encErr != nil {
				return fmt.Errorf("write mcp invalid-request response: %w", encErr)
			}
			continue
		}

		// Fresh per-request remote-read cache. This server lives for the whole
		// agent session, so the process-scoped cache main() installs would pin one
		// snapshot of "which git remotes exist" for hours and never observe a
		// remote added meanwhile — and repo remote add's invalidation runs in a
		// different process. One request is the invocation-equivalent window here.
		reqCtx := strategy.WithFreshGitRemoteCache(ctx)
		result, rpcErr := dispatchMCP(reqCtx, rootCmd, req.Method, req.Params)

		// A request without an id is a notification: never responded to.
		if len(req.ID) == 0 {
			continue
		}

		resp := mcpResponse{JSONRPC: jsonRPCVersion, ID: req.ID}
		if rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("write mcp response: %w", err)
		}
	}
	if err := sc.Err(); err != nil {
		// A single line exceeded maxMCPMessageBytes (the scanner can't resynchronize
		// past an over-long token) or the read failed; report once and stop.
		if errors.Is(err, bufio.ErrTooLong) {
			if encErr := enc.Encode(mcpResponse{JSONRPC: jsonRPCVersion, ID: json.RawMessage("null"), Error: &mcpError{Code: -32600, Message: "request too large"}}); encErr != nil {
				return fmt.Errorf("write mcp oversize response: %w", encErr)
			}
			return nil
		}
		return fmt.Errorf("read mcp request: %w", err)
	}
	return nil
}

func dispatchMCP(ctx context.Context, rootCmd *cobra.Command, method string, params json.RawMessage) (any, *mcpError) {
	// Era-independent: the legacy handshake and its notification keep their
	// exact current semantics regardless of metadata.
	switch method {
	case "initialize":
		return mcpInitializeResult(params), nil
	case "notifications/initialized":
		return nil, nil // notification; result is ignored by the caller
	}
	if isModernMCPRequest(method, params) {
		return dispatchModernMCP(ctx, rootCmd, method, params)
	}
	// Legacy era: unchanged surface.
	switch method {
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpToolDefs()}, nil
	case "tools/call":
		return handleMCPToolCall(ctx, rootCmd, params)
	default:
		return nil, &mcpError{Code: -32601, Message: "method not found: " + method}
	}
}

func mcpInitializeResult(params json.RawMessage) map[string]any {
	protocol := mcpProtocolVersion
	if len(params) > 0 {
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(params, &p) == nil && p.ProtocolVersion != "" {
			protocol = p.ProtocolVersion
		}
	}
	return map[string]any{
		"protocolVersion": protocol,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      mcpServerIdentity(),
	}
}

func mcpToolDefs() []map[string]any {
	objSchema := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	return []map[string]any{
		{
			"name":        "agent_help",
			"description": "Machine-readable usage for the entire CLI, generated live from the installed binary. Omit command for a top-level map of when to use entire and which subcommand; pass a command path to drill into that command's exact, current flags.",
			"inputSchema": objSchema(map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "Optional subcommand path, space-separated (e.g. \"checkpoint\" or \"doctor trace\"). Empty for the top-level overview.",
				},
			}),
		},
		{
			"name":        "entire_status",
			"description": "Current entire status for this repo as JSON (enabled, agents, active sessions, agent_help pointer).",
			"inputSchema": objSchema(map[string]any{}),
		},
	}
}

func handleMCPToolCall(ctx context.Context, rootCmd *cobra.Command, params json.RawMessage) (map[string]any, *mcpError) {
	var call struct {
		Name      string `json:"name"`
		Arguments struct {
			Command string `json:"command"`
		} `json:"arguments"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &call); err != nil {
			return nil, &mcpError{Code: -32602, Message: "invalid tool call params"}
		}
	}

	if call.Name == "" {
		return nil, &mcpError{Code: -32602, Message: "invalid params: tool name required"}
	}
	switch call.Name {
	case "agent_help":
		args := strings.Fields(call.Arguments.Command)
		// Resolve origin once (mirrors `entire agent-help`): derive both the repo
		// line and the trails-enablement check from a single scope.
		repoLine, trailsEnabled := agentHelpRepoContext(ctx)
		text, err := runAgentHelp(rootCmd, args, repoLine, true, trailsEnabled)
		if err != nil {
			// A bad command path is a tool-level error the agent can recover from,
			// not a protocol error — return it as an isError result.
			return mcpToolErrorResult(err.Error()), nil
		}
		return mcpToolTextResult(text), nil
	case "entire_status":
		var buf bytes.Buffer
		if err := runStatusJSON(ctx, &buf); err != nil {
			return mcpToolErrorResult(err.Error()), nil
		}
		return mcpToolTextResult(buf.String()), nil
	default:
		return nil, &mcpError{Code: -32602, Message: "unknown tool: " + call.Name}
	}
}

func mcpToolTextResult(text string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}
}

func mcpToolErrorResult(text string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": true,
	}
}

// mcpServerIdentity is the server identity shared by the legacy initialize
// result and the modern _meta serverInfo, so the two eras can never drift.
func mcpServerIdentity() map[string]any {
	return map[string]any{"name": mcpServerName, "version": versioninfo.Version}
}

// mcpRequestMeta extracts a request's _meta object, reporting whether one was
// present.
func mcpRequestMeta(params json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(params) == 0 {
		return nil, false
	}
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return nil, false
	}
	return p.Meta, p.Meta != nil
}

// isModernMCPRequest reports whether a request (after the era-independent
// initialize and notifications/initialized) is served with modern stateless
// semantics. server/discover is modern-only by definition — it is the probe a
// dual-era client sends before anything else, so it arrives with no
// per-request metadata at all. Any other request is modern when its _meta
// carries an io.modelcontextprotocol/ namespaced key: modern clients always
// send protocolVersion alongside clientInfo and clientCapabilities, and legacy
// clients never use that reserved namespace — so a namespaced key without a
// protocolVersion is a malformed modern request to reject (validateModernMeta),
// not a legacy request to serve.
func isModernMCPRequest(method string, params json.RawMessage) bool {
	if method == "server/discover" {
		return true
	}
	meta, ok := mcpRequestMeta(params)
	if !ok {
		return false
	}
	for key := range meta {
		if strings.HasPrefix(key, mcpMetaKeyPrefix) {
			return true
		}
	}
	return false
}

// dispatchModernMCP serves a modern 2026-07-28 request statelessly:
// per-request _meta replaces the initialize handshake, every result states its
// resultType, and server identity rides in _meta rather than a top-level
// serverInfo. The tools themselves are the shared registry and execution path
// the legacy era uses — only the envelope differs.
func dispatchModernMCP(ctx context.Context, rootCmd *cobra.Command, method string, params json.RawMessage) (any, *mcpError) {
	meta, _ := mcpRequestMeta(params)
	// The discovery probe predates client metadata: validate the protocol
	// version only when the probe carries one. Demanding full client identity
	// here would break the probe-first flow the method exists to serve.
	if method == "server/discover" {
		if _, hasVersion := meta[mcpMetaKeyProtocolVersion]; hasVersion {
			if err := validateModernProtocolVersion(meta); err != nil {
				return nil, err
			}
		}
		return mcpDiscoverResult(), nil
	}
	if err := validateModernMeta(meta); err != nil {
		return nil, err
	}
	switch method {
	case "tools/list":
		return mcpModernToolsListResult(), nil
	case "tools/call":
		result, rpcErr := handleMCPToolCall(ctx, rootCmd, params)
		if rpcErr != nil {
			return nil, rpcErr
		}
		return mcpModernResultEnvelope(result), nil
	default:
		// ping is legacy-era only; it does not exist in the modern surface.
		return nil, &mcpError{Code: -32601, Message: "method not found: " + method}
	}
}

// validateModernProtocolVersion checks the per-request protocolVersion. A
// well-formed but unsupported version gets UnsupportedProtocolVersionError
// (-32022) with the requested/supported detail the schema requires; a missing
// or non-string version is invalid params (-32602).
func validateModernProtocolVersion(meta map[string]json.RawMessage) *mcpError {
	raw, ok := meta[mcpMetaKeyProtocolVersion]
	if !ok {
		return &mcpError{Code: -32602, Message: "invalid params: missing _meta." + mcpMetaKeyProtocolVersion}
	}
	var version string
	if json.Unmarshal(raw, &version) != nil || version == "" {
		return &mcpError{Code: -32602, Message: "invalid params: _meta." + mcpMetaKeyProtocolVersion + " must be a non-empty string"}
	}
	if version != mcpModernProtocolVersion {
		return &mcpError{
			Code:    -32022,
			Message: "unsupported protocol version: " + version,
			Data:    map[string]any{"requested": version, "supported": []string{mcpModernProtocolVersion}},
		}
	}
	return nil
}

// validateModernMeta enforces the full per-request metadata the modern era
// requires on every stateless request beyond the discovery probe:
// protocolVersion, clientInfo (an object with a string name), and
// clientCapabilities (an object).
func validateModernMeta(meta map[string]json.RawMessage) *mcpError {
	if err := validateModernProtocolVersion(meta); err != nil {
		return err
	}
	raw, ok := meta[mcpMetaKeyClientInfo]
	if !ok {
		return &mcpError{Code: -32602, Message: "invalid params: missing _meta." + mcpMetaKeyClientInfo}
	}
	var clientInfo struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &clientInfo) != nil || clientInfo.Name == "" {
		return &mcpError{Code: -32602, Message: "invalid params: _meta." + mcpMetaKeyClientInfo + " must be an object with a string name"}
	}
	raw, ok = meta[mcpMetaKeyClientCapabilities]
	if !ok {
		return &mcpError{Code: -32602, Message: "invalid params: missing _meta." + mcpMetaKeyClientCapabilities}
	}
	var caps map[string]any
	if json.Unmarshal(raw, &caps) != nil || caps == nil {
		return &mcpError{Code: -32602, Message: "invalid params: _meta." + mcpMetaKeyClientCapabilities + " must be an object"}
	}
	return nil
}

// mcpModernResultEnvelope adds the fields every modern result carries: the
// resultType discriminator (clients may treat an absent resultType as
// "complete" only for an earlier-protocol server, so a modern response must
// state it) and server identity under _meta.
func mcpModernResultEnvelope(result map[string]any) map[string]any {
	result["resultType"] = "complete"
	result["_meta"] = map[string]any{mcpMetaKeyServerInfo: mcpServerIdentity()}
	return result
}

// mcpDiscoverResult answers the era-detection probe. A successful result is
// what tells a dual-era client "modern": supportedVersions lists only modern
// versions the server genuinely implements — advertising a version whose
// stateless semantics the server does not honor would produce clients that
// skip initialize and send requests the server rejects. The legacy surface
// stays reachable through initialize regardless.
func mcpDiscoverResult() map[string]any {
	return mcpModernResultEnvelope(map[string]any{
		"supportedVersions": []string{mcpModernProtocolVersion},
		"capabilities":      map[string]any{"tools": map[string]any{}},
		"cacheScope":        mcpCacheScopePublic,
		"ttlMs":             mcpCacheTTLMS,
	})
}

// mcpModernToolsListResult serves the shared tool registry in the modern
// cacheable envelope. Order is the registry's fixed definition order
// (deterministic, per the changelog's SHOULD). The legacy surface has no
// cursor pagination, so none is advertised here either.
func mcpModernToolsListResult() map[string]any {
	return mcpModernResultEnvelope(map[string]any{
		"tools":      mcpToolDefs(),
		"cacheScope": mcpCacheScopePublic,
		"ttlMs":      mcpCacheTTLMS,
	})
}
