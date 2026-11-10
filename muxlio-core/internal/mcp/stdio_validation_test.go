package mcp

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestStdioDecodeMatchesHTTP(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		code int
	}{
		{"mixed", `{"jsonrpc":"2.0","id":1,"method":"ping","result":{}}`, -32600},
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, -32600},
		{"null id", `{"jsonrpc":"2.0","id":null,"method":"ping"}`, -32600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, code, _ := decodeHTTPMessage([]byte(tc.body))
			if code != tc.code {
				t.Fatalf("decode code=%d want %d", code, tc.code)
			}
		})
	}
}

func TestStdioTransportRejectsInvalidMessages(t *testing.T) {
	say := func(line string) jsonRPCResponse {
		var buf bytes.Buffer
		srv := &mcpServer{tools: &server{}, enc: json.NewEncoder(&buf)}
		srv.handleTransportLine([]byte(line))
		var resp jsonRPCResponse
		if buf.Len() == 0 {
			return resp
		}
		_ = json.NewDecoder(bytes.NewReader(buf.Bytes())).Decode(&resp)
		return resp
	}
	if resp := say(`{"jsonrpc":"2.0","id":1,"method":"ping","result":{}}`); resp.Error == nil || resp.Error.Code != -32600 {
		t.Fatalf("mixed: %+v", resp.Error)
	}
	if resp := say(`{"jsonrpc":"2.0","id":null,"method":"ping"}`); resp.Error == nil || resp.Error.Code != -32600 {
		t.Fatalf("null id: %+v", resp.Error)
	}
	if resp := say(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"x"}}`); resp.Error == nil || resp.Error.Code != -32600 {
		t.Fatalf("missing id: %+v", resp.Error)
	}
	if resp := say(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`); resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("initialize: %+v", resp.Error)
	}
	if resp := say(`{"jsonrpc":"2.0","id":1,"result":{}}`); resp.Error != nil {
		t.Fatalf("client response should be silent: %+v", resp.Error)
	}
}
