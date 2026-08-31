package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/joeykchen/codexlink/internal/buildinfo"
)

const (
	protocolVersionMetaKey      = "io.modelcontextprotocol/protocolVersion"
	clientInfoMetaKey           = "io.modelcontextprotocol/clientInfo"
	clientCapabilitiesMetaKey   = "io.modelcontextprotocol/clientCapabilities"
	serverInfoMetaKey           = "io.modelcontextprotocol/serverInfo"
	headerMismatchCode          = -32020
	unsupportedProtocolCode     = -32022
	missingClientCapabilityCode = -32021
)

type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type DispatchResult struct {
	Response     RPCResponse
	Notification bool
}

type requestMetadata struct {
	ProtocolVersion    string
	ClientCapabilities map[string]json.RawMessage
	ClientInfo         map[string]json.RawMessage
	HasMeta            bool
	HasCapabilities    bool
}

func parseRequestMetadata(params json.RawMessage) (requestMetadata, error) {
	var result requestMetadata
	if len(bytes.TrimSpace(params)) == 0 {
		return result, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(params, &object); err != nil {
		return result, err
	}
	rawMeta, ok := object["_meta"]
	if !ok {
		return result, nil
	}
	result.HasMeta = true
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(rawMeta, &metadata); err != nil {
		return result, err
	}
	if raw, ok := metadata[protocolVersionMetaKey]; ok {
		if err := json.Unmarshal(raw, &result.ProtocolVersion); err != nil {
			return result, err
		}
	}
	if raw, ok := metadata[clientCapabilitiesMetaKey]; ok {
		result.HasCapabilities = true
		if err := json.Unmarshal(raw, &result.ClientCapabilities); err != nil || result.ClientCapabilities == nil {
			if err != nil {
				return result, err
			}
			return result, fmt.Errorf("client capabilities must be an object")
		}
	}
	if raw, ok := metadata[clientInfoMetaKey]; ok {
		if err := json.Unmarshal(raw, &result.ClientInfo); err != nil || result.ClientInfo == nil {
			if err != nil {
				return result, err
			}
			return result, fmt.Errorf("client info must be an object")
		}
	}
	return result, nil
}

func serverMetadata() map[string]any {
	return map[string]any{
		serverInfoMetaKey: map[string]any{
			"name":    buildinfo.ServiceName,
			"version": buildinfo.Version,
		},
	}
}

func isNotification(request RPCRequest) bool {
	return len(request.ID) == 0 || bytes.Equal(bytes.TrimSpace(request.ID), []byte("null"))
}

func responseID(id json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(id)) == 0 {
		return json.RawMessage("null")
	}
	return id
}
