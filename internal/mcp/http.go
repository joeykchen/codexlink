package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/joeykchen/codexlink/internal/auth"
	"github.com/joeykchen/codexlink/internal/buildinfo"
	"github.com/joeykchen/codexlink/internal/logging"
)

// Handler implements the stateless MCP Streamable HTTP binding. It keeps
// transport validation separate from Dispatcher so tool execution remains
// independent from HTTP and protocol-era compatibility decisions.
type Handler struct{ dispatcher *Dispatcher }

func NewHandler(registry *Registry, logger *logging.Logger) *Handler {
	return &Handler{dispatcher: NewDispatcher(registry, logger)}
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeRPCJSON(response, http.StatusMethodNotAllowed, RPCResponse{
			JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &RPCError{Code: -32600, Message: "Only HTTP POST is supported by this stateless MCP endpoint."},
		})
		return
	}

	request.Body = http.MaxBytesReader(response, request.Body, 8*1024*1024)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		writeRPCJSON(response, http.StatusBadRequest, rpcFailure(nil, -32700, "Unable to read JSON-RPC request.", nil))
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		writeRPCJSON(response, http.StatusBadRequest, rpcFailure(nil, -32700, "Empty JSON-RPC request.", nil))
		return
	}
	if body[0] == '[' {
		h.handleLegacyBatch(response, request, body)
		return
	}

	var rpc RPCRequest
	if err := json.Unmarshal(body, &rpc); err != nil {
		writeRPCJSON(response, http.StatusBadRequest, rpcFailure(nil, -32700, "Invalid JSON.", nil))
		return
	}
	protocol, failure := validateHTTPRequest(request, rpc)
	if failure != nil {
		writeRPCJSON(response, failure.Status, RPCResponse{JSONRPC: "2.0", ID: responseID(rpc.ID), Error: &failure.Error})
		return
	}
	response.Header().Set("MCP-Protocol-Version", protocol.Version)

	principal, _ := auth.PrincipalFromContext(request.Context())
	result := h.dispatcher.Dispatch(request.Context(), principal, rpc, protocol.Version)
	if result.Notification {
		response.WriteHeader(http.StatusAccepted)
		return
	}
	status := http.StatusOK
	if protocol.Modern && result.Response.Error != nil {
		switch result.Response.Error.Code {
		case -32600, -32602, missingClientCapabilityCode:
			status = http.StatusBadRequest
		case -32601:
			status = http.StatusNotFound
		}
	}
	writeRPCJSON(response, status, result.Response)
}

func (h *Handler) handleLegacyBatch(response http.ResponseWriter, request *http.Request, body []byte) {
	var requests []RPCRequest
	if err := json.Unmarshal(body, &requests); err != nil || len(requests) == 0 {
		writeRPCJSON(response, http.StatusBadRequest, rpcFailure(nil, -32600, "Invalid JSON-RPC batch.", nil))
		return
	}

	// Modern Streamable HTTP requires exactly one JSON-RPC message per POST.
	// A modern version in either the header or any request body therefore makes
	// the entire batch invalid rather than partially executing it.
	versionHeader, _, headerErr := singleHeader(request.Header, "MCP-Protocol-Version")
	if headerErr != nil {
		writeRPCJSON(response, http.StatusBadRequest, RPCResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &headerFailure(headerErr.Error()).Error})
		return
	}
	if buildinfo.IsModernProtocol(versionHeader) || (versionHeader != "" && !buildinfo.IsLegacyProtocol(versionHeader)) {
		writeRPCJSON(response, http.StatusBadRequest, rpcFailure(nil, -32600, "Modern MCP Streamable HTTP accepts one JSON-RPC message per POST.", nil))
		return
	}
	if request.Header.Get("Mcp-Method") != "" || request.Header.Get("Mcp-Name") != "" {
		failure := headerFailure("Routing headers cannot represent more than one request in a JSON-RPC batch.")
		writeRPCJSON(response, failure.Status, RPCResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &failure.Error})
		return
	}

	principal, _ := auth.PrincipalFromContext(request.Context())
	responses := make([]RPCResponse, 0, len(requests))
	selected := ""
	for _, rpc := range requests {
		protocol, failure := validateHTTPRequest(request, rpc)
		if failure != nil {
			writeRPCJSON(response, failure.Status, RPCResponse{JSONRPC: "2.0", ID: responseID(rpc.ID), Error: &failure.Error})
			return
		}
		if protocol.Modern {
			writeRPCJSON(response, http.StatusBadRequest, rpcFailure(rpc.ID, -32600, "Modern MCP Streamable HTTP accepts one JSON-RPC message per POST.", nil))
			return
		}
		if selected == "" {
			selected = protocol.Version
		} else if selected != protocol.Version {
			writeRPCJSON(response, http.StatusBadRequest, rpcFailure(rpc.ID, -32600, "A JSON-RPC batch cannot mix MCP protocol versions.", nil))
			return
		}
		result := h.dispatcher.Dispatch(request.Context(), principal, rpc, protocol.Version)
		if !result.Notification {
			responses = append(responses, result.Response)
		}
	}
	if selected != "" {
		response.Header().Set("MCP-Protocol-Version", selected)
	}
	if len(responses) == 0 {
		response.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPCJSON(response, http.StatusOK, responses)
}

func rpcFailure(id json.RawMessage, code int, message string, data any) RPCResponse {
	return RPCResponse{JSONRPC: "2.0", ID: responseID(id), Error: &RPCError{Code: code, Message: message, Data: data}}
}

func writeRPCJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
