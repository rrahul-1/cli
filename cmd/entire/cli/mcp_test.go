package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/versioninfo"
)

type mcpTestResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

// driveMCP runs the server against a sequence of newline-delimited requests and
// returns the decoded responses (notifications produce none).
func driveMCP(t *testing.T, requests ...string) []mcpTestResp {
	t.Helper()
	root := NewRootCmd()
	in := strings.NewReader(strings.Join(requests, "\n") + "\n")
	var out bytes.Buffer
	if err := runMCPServer(context.Background(), root, in, &out); err != nil {
		t.Fatalf("runMCPServer: %v", err)
	}
	var resps []mcpTestResp
	dec := json.NewDecoder(&out)
	for dec.More() {
		var r mcpTestResp
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("decode response: %v (raw: %s)", err, out.String())
		}
		resps = append(resps, r)
	}
	return resps
}

func mcpResultText(t *testing.T, result json.RawMessage) string {
	t.Helper()
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &res); err != nil {
		t.Fatalf("unmarshal tool result content: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("tool result had no content: %s", result)
	}
	return res.Content[0].Text
}

// The MCP handshake: initialize echoes the client's protocolVersion and returns
// serverInfo + the tools capability; the `notifications/initialized` notification
// gets no response.
func TestMCPServer_InitializeHandshake(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response (the notification is silent), got %d", len(resps))
	}
	var res struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("unmarshal initialize result: %v", err)
	}
	if res.ServerInfo.Name != mcpServerName {
		t.Errorf("serverInfo.name = %q, want %q", res.ServerInfo.Name, mcpServerName)
	}
	if res.ProtocolVersion != "2024-11-05" {
		t.Errorf("initialize should echo the client's protocolVersion, got %q", res.ProtocolVersion)
	}
	if _, ok := res.Capabilities["tools"]; !ok {
		t.Errorf("initialize result should advertise the tools capability, got %v", res.Capabilities)
	}
}

// When the client omits protocolVersion, the server advertises its own default.
func TestMCPServer_InitializeDefaultProtocol(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("unmarshal initialize result: %v", err)
	}
	if res.ProtocolVersion != mcpProtocolVersion {
		t.Errorf("default protocolVersion = %q, want %q", res.ProtocolVersion, mcpProtocolVersion)
	}
}

// A non-string protocolVersion is ignored gracefully; the server falls back to
// its default instead of erroring.
func TestMCPServer_InitializeBadProtocolType(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":123}}`)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("unmarshal initialize result: %v", err)
	}
	if res.ProtocolVersion != mcpProtocolVersion {
		t.Errorf("bad protocolVersion type should fall back to default %q, got %q", mcpProtocolVersion, res.ProtocolVersion)
	}
}

func TestMCPServer_Ping(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":8,"method":"ping"}`)
	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("ping should return exactly one non-error response, got %+v", resps)
	}
}

func TestMCPServer_ToolsList(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	var res struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range res.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"agent_help", "entire_status"} {
		if !names[want] {
			t.Errorf("tools/list missing %q; got %v", want, names)
		}
	}
}

// The agent_help tool returns the live agent-help JSON document (asJSON=true),
// reusing the same renderer the CLI uses.
func TestMCPServer_AgentHelpToolCall(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"agent_help","arguments":{"command":""}}}`)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	var doc struct {
		Command     string `json:"command"`
		Subcommands []struct {
			Name string `json:"name"`
		} `json:"subcommands"`
	}
	if err := json.Unmarshal([]byte(mcpResultText(t, resps[0].Result)), &doc); err != nil {
		t.Fatalf("agent_help tool should return the agent-help JSON document: %v", err)
	}
	if doc.Command != NewRootCmd().Name() {
		t.Errorf("top-level agent_help should be the root command document, got command=%q", doc.Command)
	}
	if len(doc.Subcommands) == 0 {
		t.Error("top-level agent_help should list subcommands, got none")
	}
}

// Drilling into a command path returns that command's document.
func TestMCPServer_AgentHelpToolCall_Subcommand(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"agent_help","arguments":{"command":"status"}}}`)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	var doc struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(mcpResultText(t, resps[0].Result)), &doc); err != nil {
		t.Fatalf("agent_help subcommand should return JSON: %v", err)
	}
	if doc.Command != "entire status" {
		t.Errorf("agent_help command=status should drill into `entire status`, got %q", doc.Command)
	}
}

// A bad command path is surfaced as a tool error (isError), not a protocol error,
// so the agent can recover.
func TestMCPServer_AgentHelpUnknownCommandIsToolError(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"agent_help","arguments":{"command":"definitely-not-a-command"}}}`)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	var res struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("unmarshal tool result: %v", err)
	}
	if !res.IsError {
		t.Errorf("unknown command should be a tool error (isError=true); result: %s", resps[0].Result)
	}
}

// The entire_status tool must stay byte-identical to the passive `status --json`
// surface (runStatusJSON) — it is the same data over a different transport.
func TestMCPServer_EntireStatusMatchesPassive(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"entire_status"}}`)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	text := mcpResultText(t, resps[0].Result)
	var direct bytes.Buffer
	if err := runStatusJSON(context.Background(), &direct); err != nil {
		t.Fatalf("runStatusJSON: %v", err)
	}
	if strings.TrimSpace(text) != strings.TrimSpace(direct.String()) {
		t.Errorf("entire_status MCP tool must match runStatusJSON\n tool:   %s\n direct: %s", text, direct.String())
	}
}

// entire_status is a shipped path: it must return parseable status JSON.
func TestMCPServer_EntireStatusToolCall(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"entire_status","arguments":{}}}`)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	text := mcpResultText(t, resps[0].Result)
	var status struct {
		Enabled        *bool    `json:"enabled"`
		Agents         []string `json:"agents"`
		ActiveSessions []any    `json:"active_sessions"`
		AgentHelp      string   `json:"agent_help"`
		Error          string   `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &status); err != nil {
		t.Fatalf("entire_status tool should return status JSON, got %q (err %v)", text, err)
	}
}

func TestMCPServer_EmptyToolName(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":""}}`)
	if len(resps) != 1 || resps[0].Error == nil || resps[0].Error.Code != -32602 {
		t.Fatalf("empty tool name should return invalid-params (-32602), got %+v", resps)
	}
}

func TestMCPServer_UnknownToolIsProtocolError(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"no-such-tool"}}`)
	if len(resps) != 1 || resps[0].Error == nil {
		t.Fatalf("expected one error response for an unknown tool, got %+v", resps)
	}
}

func TestMCPServer_UnknownMethodReturnsError(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":6,"method":"bogus/method"}`)
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	if resps[0].Error == nil || resps[0].Error.Code != -32601 {
		t.Errorf("expected method-not-found (-32601), got: %+v", resps[0].Error)
	}
}

// A parseable-but-invalid request (missing method or wrong jsonrpc version) is
// rejected with -32600 before dispatch, not treated as method-not-found.
func TestMCPServer_InvalidRequest(t *testing.T) {
	t.Parallel()
	for _, req := range []string{
		`{"jsonrpc":"2.0","id":1}`,                 // missing method
		`{"jsonrpc":"1.0","id":2,"method":"ping"}`, // wrong jsonrpc version
	} {
		resps := driveMCP(t, req)
		if len(resps) != 1 || resps[0].Error == nil || resps[0].Error.Code != -32600 {
			t.Errorf("request %s should be rejected with -32600 (invalid request), got %+v", req, resps)
		}
	}
}

// runMCPServer must echo the request id verbatim (numeric or string); a
// dropped/swapped id silently breaks multi-call MCP sessions.
func TestMCPServer_EchoesRequestID(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t,
		`{"jsonrpc":"2.0","id":42,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":"abc","method":"ping"}`,
	)
	if len(resps) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(resps))
	}
	if string(resps[0].ID) != "42" {
		t.Errorf("numeric id should round-trip verbatim, got %s", resps[0].ID)
	}
	if string(resps[1].ID) != `"abc"` {
		t.Errorf("string id should round-trip verbatim, got %s", resps[1].ID)
	}
}

// A single line larger than maxMCPMessageBytes is rejected without consuming
// unbounded memory, and the server stops cleanly.
func TestMCPServer_OversizedMessageRejected(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", maxMCPMessageBytes+1024)
	line := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"agent_help","arguments":{"command":"` + big + `"}}}` + "\n"
	var out bytes.Buffer
	if err := runMCPServer(context.Background(), NewRootCmd(), strings.NewReader(line), &out); err != nil {
		t.Fatalf("runMCPServer: %v", err)
	}
	var resp mcpTestResp
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("expected an oversize error response, got %q (err %v)", out.String(), err)
	}
	if resp.Error == nil || resp.Error.Code != -32600 {
		t.Errorf("expected request-too-large (-32600), got %+v", resp.Error)
	}
}

// A JSON-RPC batch array is unsupported: it yields a parse error, and the server
// recovers to the next line instead of terminating.
func TestMCPServer_BatchArrayRejectedThenRecovers(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t,
		`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
	)
	if len(resps) != 2 {
		t.Fatalf("expected 2 responses (parse error, then the recovered ping), got %d: %+v", len(resps), resps)
	}
	if resps[0].Error == nil || resps[0].Error.Code != -32700 {
		t.Errorf("batch array should yield a parse error (-32700), got %+v", resps[0].Error)
	}
	if resps[1].Error != nil {
		t.Errorf("server should recover and serve the next message, got error %+v", resps[1].Error)
	}
}

// Malformed JSON in the stream is reported as a JSON-RPC parse error (-32700).
func TestMCPServer_ParseError(t *testing.T) {
	t.Parallel()
	root := NewRootCmd()
	var out bytes.Buffer
	if err := runMCPServer(context.Background(), root, strings.NewReader("{not valid json\n"), &out); err != nil {
		t.Fatalf("runMCPServer: %v", err)
	}
	var resp mcpTestResp
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("expected a JSON parse-error response, got %q (err %v)", out.String(), err)
	}
	if resp.Error == nil || resp.Error.Code != -32700 {
		t.Errorf("expected parse error -32700, got %+v", resp.Error)
	}
}

// --- Modern era (2026-07-28): server/discover ---

// mcpModernMeta wraps params in the full modern per-request metadata a
// compliant modern client sends on every stateless request.
func mcpModernMeta(params string) string {
	inner := `"io.modelcontextprotocol/protocolVersion":"` + mcpModernProtocolVersion + `",` +
		`"io.modelcontextprotocol/clientInfo":{"name":"mcp-test-client","version":"1.0.0"},` +
		`"io.modelcontextprotocol/clientCapabilities":{}`
	if params == "{}" {
		return `{"_meta":{` + inner + `}}`
	}
	return `{"_meta":{` + inner + `},` + params[1:]
}

// mcpAssertModernIdentity asserts the shared _meta serverInfo identity block
// every modern result carries.
func mcpAssertModernIdentity(t *testing.T, result json.RawMessage) {
	t.Helper()
	var res struct {
		Meta struct {
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"io.modelcontextprotocol/serverInfo"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(result, &res); err != nil {
		t.Fatalf("unmarshal _meta serverInfo: %v (%s)", err, result)
	}
	if res.Meta.ServerInfo.Name != mcpServerName || res.Meta.ServerInfo.Version != versioninfo.Version {
		t.Errorf("_meta serverInfo = %+v, want name %q version %q", res.Meta.ServerInfo, mcpServerName, versioninfo.Version)
	}
}

// The discovery probe must succeed with no prior initialize and no metadata:
// a successful DiscoverResult is what tells a dual-era client this server is
// modern. Identity lives under _meta — the modern schema has no top-level
// serverInfo — and supportedVersions is exactly the modern versions the server
// genuinely implements.
func TestMCPServer_Discover(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":0,"method":"server/discover","params":{}}`)
	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("server/discover should return exactly one non-error response, got %+v", resps)
	}
	if string(resps[0].ID) != "0" {
		t.Errorf("discovery should round-trip the numeric id verbatim, got %s", resps[0].ID)
	}
	var res struct {
		ResultType        string                     `json:"resultType"`
		SupportedVersions []string                   `json:"supportedVersions"`
		Capabilities      map[string]json.RawMessage `json:"capabilities"`
		CacheScope        string                     `json:"cacheScope"`
		TTLMS             float64                    `json:"ttlMs"`
		ServerInfo        json.RawMessage            `json:"serverInfo"`
	}
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("unmarshal discover result: %v (%s)", err, resps[0].Result)
	}
	if res.ResultType != "complete" {
		t.Errorf("resultType = %q, want %q", res.ResultType, "complete")
	}
	if len(res.SupportedVersions) != 1 || res.SupportedVersions[0] != mcpModernProtocolVersion {
		t.Errorf("supportedVersions = %v, want exactly [%s] (a legacy version here would misidentify the server as modern)", res.SupportedVersions, mcpModernProtocolVersion)
	}
	if _, ok := res.Capabilities["tools"]; !ok || len(res.Capabilities) != 1 {
		t.Errorf("capabilities must advertise exactly the tools capability, got %v", res.Capabilities)
	}
	if res.CacheScope == "" || res.TTLMS <= 0 {
		t.Errorf("a cacheable discovery result needs cacheScope and ttlMs, got %q %v", res.CacheScope, res.TTLMS)
	}
	if len(res.ServerInfo) != 0 {
		t.Errorf("modern DiscoverResult has no top-level serverInfo (identity lives in _meta), got %s", res.ServerInfo)
	}
	mcpAssertModernIdentity(t, resps[0].Result)
}

// The probe works with a string id too, and two probes back to back are
// byte-identical: discovery is stateless and idempotent.
func TestMCPServer_DiscoverStringIDAndIdempotent(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t,
		`{"jsonrpc":"2.0","id":"abc","method":"server/discover","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`,
	)
	if len(resps) != 2 || resps[0].Error != nil || resps[1].Error != nil {
		t.Fatalf("expected two non-error discovery responses, got %+v", resps)
	}
	if string(resps[0].ID) != `"abc"` {
		t.Errorf("string id should round-trip verbatim, got %s", resps[0].ID)
	}
	if string(resps[0].Result) != string(resps[1].Result) {
		t.Errorf("discovery must be idempotent:\n first: %s\n second: %s", resps[0].Result, resps[1].Result)
	}
}

// A probe that does carry a modern protocolVersion validates it; an
// unsupported one gets UnsupportedProtocolVersionError.
func TestMCPServer_DiscoverUnsupportedVersion(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1999-01-01"}}}`)
	if len(resps) != 1 || resps[0].Error == nil || resps[0].Error.Code != -32022 {
		t.Fatalf("discover with an unsupported version should return -32022, got %+v", resps)
	}
	var data struct {
		Requested string   `json:"requested"`
		Supported []string `json:"supported"`
	}
	if err := json.Unmarshal(resps[0].Error.Data, &data); err != nil {
		t.Fatalf("unmarshal -32022 data: %v (%s)", err, resps[0].Error.Data)
	}
	if data.Requested != "1999-01-01" || len(data.Supported) != 1 || data.Supported[0] != mcpModernProtocolVersion {
		t.Errorf("-32022 data = %+v, want requested 1999-01-01, supported [%s]", data, mcpModernProtocolVersion)
	}
}

// The probe-first flow: discovery, then the legacy initialize, in one process,
// both succeeding — the migration path dual-era clients take against this
// server.
func TestMCPServer_DiscoverBeforeInitialize(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t,
		`{"jsonrpc":"2.0","id":0,"method":"server/discover","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
	)
	if len(resps) != 2 || resps[0].Error != nil || resps[1].Error != nil {
		t.Fatalf("probe then initialize should both succeed, got %+v", resps)
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(resps[1].Result, &init); err != nil {
		t.Fatalf("unmarshal initialize result: %v", err)
	}
	if init.ProtocolVersion != "2024-11-05" {
		t.Errorf("initialize should still echo the client's version, got %q", init.ProtocolVersion)
	}
}

// --- Modern era: per-request metadata validation ---

// Modern requests missing any required _meta key, or carrying malformed
// clientInfo/clientCapabilities, are rejected with invalid params (-32602).
func TestMCPServer_ModernMetaValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		params string
	}{
		{"missing protocolVersion", `{"_meta":{"io.modelcontextprotocol/clientInfo":{"name":"c","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}`},
		{"missing clientInfo", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"` + mcpModernProtocolVersion + `","io.modelcontextprotocol/clientCapabilities":{}}}`},
		{"missing clientCapabilities", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"` + mcpModernProtocolVersion + `","io.modelcontextprotocol/clientInfo":{"name":"c","version":"1"}}}`},
		{"non-string protocolVersion", `{"_meta":{"io.modelcontextprotocol/protocolVersion":42,"io.modelcontextprotocol/clientInfo":{"name":"c","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}`},
		{"clientInfo not an object", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"` + mcpModernProtocolVersion + `","io.modelcontextprotocol/clientInfo":"nope","io.modelcontextprotocol/clientCapabilities":{}}}`},
		{"clientInfo without name", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"` + mcpModernProtocolVersion + `","io.modelcontextprotocol/clientInfo":{"version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}`},
		{"clientCapabilities not an object", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"` + mcpModernProtocolVersion + `","io.modelcontextprotocol/clientInfo":{"name":"c","version":"1"},"io.modelcontextprotocol/clientCapabilities":3}}`},
		{"clientCapabilities null", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"` + mcpModernProtocolVersion + `","io.modelcontextprotocol/clientInfo":{"name":"c","version":"1"},"io.modelcontextprotocol/clientCapabilities":null}}`},
	}
	for _, tc := range cases {
		resps := driveMCP(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":`+tc.params+`}`)
		if len(resps) != 1 || resps[0].Error == nil || resps[0].Error.Code != -32602 {
			t.Errorf("%s: want invalid params (-32602), got %+v", tc.name, resps)
		}
	}
}

// A well-formed but unsupported modern protocolVersion gets
// UnsupportedProtocolVersionError (-32022) with the required data.
func TestMCPServer_ModernUnsupportedProtocolVersion(t *testing.T) {
	t.Parallel()
	params := strings.Replace(mcpModernMeta("{}"), mcpModernProtocolVersion, "2025-06-18", 1)
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":`+params+`}`)
	if len(resps) != 1 || resps[0].Error == nil || resps[0].Error.Code != -32022 {
		t.Fatalf("modern request with a legacy version should return -32022, got %+v", resps)
	}
	var data struct {
		Requested string   `json:"requested"`
		Supported []string `json:"supported"`
	}
	if err := json.Unmarshal(resps[0].Error.Data, &data); err != nil {
		t.Fatalf("unmarshal -32022 data: %v (%s)", err, resps[0].Error.Data)
	}
	if data.Requested != "2025-06-18" || len(data.Supported) != 1 || data.Supported[0] != mcpModernProtocolVersion {
		t.Errorf("-32022 data = %+v, want requested 2025-06-18, supported [%s]", data, mcpModernProtocolVersion)
	}
}

// --- Modern era: tools ---

// Modern tools/list: same shared registry as legacy (definitions byte-equal),
// fixed deterministic order, modern cacheable envelope.
func TestMCPServer_ModernToolsList(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":`+mcpModernMeta("{}")+`}`,
	)
	if len(resps) != 2 || resps[0].Error != nil || resps[1].Error != nil {
		t.Fatalf("expected two non-error tools/list responses, got %+v", resps)
	}
	var legacy, modern struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(resps[0].Result, &legacy); err != nil {
		t.Fatalf("unmarshal legacy tools/list: %v", err)
	}
	if err := json.Unmarshal(resps[1].Result, &modern); err != nil {
		t.Fatalf("unmarshal modern tools/list: %v", err)
	}
	if !reflect.DeepEqual(legacy.Tools, modern.Tools) {
		t.Errorf("modern tools/list must serve the identical shared registry:\n legacy:  %v\n modern: %v", legacy.Tools, modern.Tools)
	}
	if len(modern.Tools) != 2 || modern.Tools[0]["name"] != "agent_help" || modern.Tools[1]["name"] != "entire_status" {
		t.Errorf("tool order must be deterministic (agent_help, entire_status), got %v", modern.Tools)
	}
	var envelope struct {
		ResultType string  `json:"resultType"`
		CacheScope string  `json:"cacheScope"`
		TTLMS      float64 `json:"ttlMs"`
	}
	if err := json.Unmarshal(resps[1].Result, &envelope); err != nil {
		t.Fatalf("unmarshal modern envelope: %v", err)
	}
	if envelope.ResultType != "complete" || envelope.CacheScope == "" || envelope.TTLMS <= 0 {
		t.Errorf("modern tools/list needs resultType/cacheScope/ttlMs, got %+v", envelope)
	}
	mcpAssertModernIdentity(t, resps[1].Result)
}

// Modern tools/call shares the legacy execution path; a successful call comes
// back in the modern envelope.
func TestMCPServer_ModernToolCallSuccess(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":`+mcpModernMeta(`{"name":"agent_help","arguments":{"command":""}}`)+`}`)
	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("modern tools/call should succeed, got %+v", resps)
	}
	var doc struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(mcpResultText(t, resps[0].Result)), &doc); err != nil {
		t.Fatalf("modern agent_help should return the agent-help JSON document: %v", err)
	}
	var envelope struct {
		ResultType string `json:"resultType"`
		IsError    bool   `json:"isError"`
	}
	if err := json.Unmarshal(resps[0].Result, &envelope); err != nil {
		t.Fatalf("unmarshal modern tool result: %v", err)
	}
	if envelope.ResultType != "complete" || envelope.IsError {
		t.Errorf("success path should be resultType=complete, isError=false, got %+v", envelope)
	}
	mcpAssertModernIdentity(t, resps[0].Result)
}

// A bad command path stays a tool-level error (isError) in the modern envelope,
// not a protocol error — same semantics as legacy.
func TestMCPServer_ModernToolCallExecutionFailure(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":`+mcpModernMeta(`{"name":"agent_help","arguments":{"command":"definitely-not-a-command"}}`)+`}`)
	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("tool-level failure should not be a protocol error, got %+v", resps)
	}
	var res struct {
		ResultType string `json:"resultType"`
		IsError    bool   `json:"isError"`
	}
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("unmarshal modern tool result: %v", err)
	}
	if !res.IsError || res.ResultType != "complete" {
		t.Errorf("execution failure should be isError=true inside a complete result, got %+v", res)
	}
}

// Unknown tool and empty tool name stay protocol errors (-32602) in the modern
// era, matching legacy semantics.
func TestMCPServer_ModernToolCallProtocolErrors(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":`+mcpModernMeta(`{"name":"no-such-tool"}`)+`}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":`+mcpModernMeta(`{"name":""}`)+`}`,
	)
	if len(resps) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(resps))
	}
	for i, resp := range resps {
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Errorf("response %d: want invalid params (-32602), got %+v", i, resp.Error)
		}
	}
}

// --- Dual-era integration: one stdio process serving both eras ---

// Modern and legacy traffic interleaved over a single server process: the
// modern side runs statelessly (no initialize anywhere), the legacy side runs
// the full handshake, and each request is answered with its own era's
// semantics.
func TestMCPServer_DualEraInterleaved(t *testing.T) {
	t.Parallel()
	resps := driveMCP(t,
		// Modern client: discover, list, call — no initialize.
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":`+mcpModernMeta("{}")+`}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":`+mcpModernMeta(`{"name":"agent_help","arguments":{"command":""}}`)+`}`,
		// Legacy client, same process: full handshake.
		`{"jsonrpc":"2.0","id":4,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":5,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"agent_help","arguments":{"command":"status"}}}`,
		// Modern request advertising a legacy version: UnsupportedProtocolVersionError.
		`{"jsonrpc":"2.0","id":8,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2025-06-18","io.modelcontextprotocol/clientInfo":{"name":"c","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`,
		// Unknown method: still method-not-found in both eras.
		`{"jsonrpc":"2.0","id":9,"method":"bogus/method"}`,
	)
	if len(resps) != 9 {
		t.Fatalf("expected 9 responses (the notification is silent), got %d: %+v", len(resps), resps)
	}
	for i, wantID := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		if string(resps[i].ID) != wantID {
			t.Errorf("response %d: id = %s, want %s", i, resps[i].ID, wantID)
		}
	}
	for i := range 7 {
		if resps[i].Error != nil {
			t.Errorf("response %d should succeed, got error %+v", i, resps[i].Error)
		}
	}
	if resps[7].Error == nil || resps[7].Error.Code != -32022 {
		t.Errorf("legacy-versioned modern request should be -32022, got %+v", resps[7].Error)
	}
	if resps[8].Error == nil || resps[8].Error.Code != -32601 {
		t.Errorf("unknown method should be -32601, got %+v", resps[8].Error)
	}
	// The legacy tools/list in the same stream must be untouched by the modern
	// traffic around it: no resultType, no cache fields, bare tool list.
	var legacyList struct {
		ResultType string `json:"resultType"`
		Tools      []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resps[5].Result, &legacyList); err != nil {
		t.Fatalf("unmarshal legacy tools/list: %v", err)
	}
	if legacyList.ResultType != "" || len(legacyList.Tools) != 2 {
		t.Errorf("legacy tools/list must keep its bare shape, got %+v", legacyList)
	}
}
