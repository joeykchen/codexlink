package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/joeykchen/codexlink/internal/buildinfo"
)

type protocolContext struct {
	Version string
	Modern  bool
}

type requestFailure struct {
	Status int
	Error  RPCError
}

func validateHTTPRequest(request *http.Request, rpc RPCRequest) (protocolContext, *requestFailure) {
	versionHeader, versionPresent, versionHeaderErr := singleHeader(request.Header, "MCP-Protocol-Version")
	methodHeader, methodPresent, methodHeaderErr := singleHeader(request.Header, "Mcp-Method")
	nameHeader, namePresent, nameHeaderErr := singleHeader(request.Header, "Mcp-Name")

	metadata, metadataErr := parseRequestMetadata(rpc.Params)
	initializeVersion := initializeProtocolVersion(rpc)
	modern := modernRequestIntent(rpc.Method, versionHeader, metadata.ProtocolVersion)

	if versionHeaderErr != nil || methodHeaderErr != nil || nameHeaderErr != nil {
		return protocolContext{}, headerFailure(firstError(versionHeaderErr, methodHeaderErr, nameHeaderErr).Error())
	}
	if modern {
		if metadataErr != nil || !metadata.HasMeta {
			return protocolContext{}, invalidParamsFailure("Modern MCP requests require an object params._meta field.")
		}
		if metadata.ProtocolVersion == "" {
			return protocolContext{}, invalidParamsFailure("params._meta is missing io.modelcontextprotocol/protocolVersion.")
		}
		if !metadata.HasCapabilities {
			return protocolContext{}, invalidParamsFailure("params._meta is missing io.modelcontextprotocol/clientCapabilities.")
		}
		if !versionPresent || versionHeader == "" {
			return protocolContext{}, headerFailure("MCP-Protocol-Version is required for modern MCP requests.")
		}
		if !safePlainHeaderValue(versionHeader) {
			return protocolContext{}, headerFailure("MCP-Protocol-Version contains invalid characters.")
		}
		if versionHeader != metadata.ProtocolVersion {
			return protocolContext{}, headerFailure(fmt.Sprintf("MCP-Protocol-Version header value %q does not match body value %q.", versionHeader, metadata.ProtocolVersion))
		}
		if !buildinfo.SupportsProtocol(metadata.ProtocolVersion) || !buildinfo.IsModernProtocol(metadata.ProtocolVersion) {
			return protocolContext{}, unsupportedProtocolFailure(metadata.ProtocolVersion)
		}
		if !methodPresent || methodHeader == "" {
			return protocolContext{}, headerFailure("Mcp-Method is required for modern MCP requests.")
		}
		if !safePlainHeaderValue(methodHeader) {
			return protocolContext{}, headerFailure("Mcp-Method contains invalid characters.")
		}
		if methodHeader != rpc.Method {
			return protocolContext{}, headerFailure(fmt.Sprintf("Mcp-Method header value %q does not match body value %q.", methodHeader, rpc.Method))
		}
		if sourceName, required, err := routingName(rpc); required {
			if err != nil || sourceName == "" {
				return protocolContext{}, invalidParamsFailure("The request body is missing the routing name required by this method.")
			}
			if !namePresent || nameHeader == "" {
				return protocolContext{}, headerFailure("Mcp-Name is required for this MCP method.")
			}
			decoded, err := decodeMirroredHeader(nameHeader)
			if err != nil {
				return protocolContext{}, headerFailure("Mcp-Name is malformed: " + err.Error())
			}
			if decoded != sourceName {
				return protocolContext{}, headerFailure(fmt.Sprintf("Mcp-Name header value %q does not match body value %q.", decoded, sourceName))
			}
		} else if namePresent {
			return protocolContext{}, headerFailure("Mcp-Name is not valid for this MCP method.")
		}
		return protocolContext{Version: metadata.ProtocolVersion, Modern: true}, nil
	}

	// Legacy Streamable HTTP requests did not require mirrored routing headers,
	// but any supplied values are still checked to avoid two sources of truth.
	if metadataErr != nil {
		metadata = requestMetadata{}
	}
	if methodPresent {
		if !safePlainHeaderValue(methodHeader) || methodHeader != rpc.Method {
			return protocolContext{}, headerFailure(fmt.Sprintf("Mcp-Method header value %q does not match body value %q.", methodHeader, rpc.Method))
		}
	}
	if namePresent {
		sourceName, required, err := routingName(rpc)
		if err != nil || !required {
			return protocolContext{}, headerFailure("Mcp-Name does not correspond to a routing value in the request body.")
		}
		decoded, err := decodeMirroredHeader(nameHeader)
		if err != nil || decoded != sourceName {
			return protocolContext{}, headerFailure(fmt.Sprintf("Mcp-Name header value %q does not match body value %q.", nameHeader, sourceName))
		}
	}

	selected := selectLegacyProtocol(versionHeader, metadata.ProtocolVersion, initializeVersion)
	if versionPresent && versionHeader != "" && !buildinfo.SupportsProtocol(versionHeader) {
		return protocolContext{}, unsupportedProtocolFailure(versionHeader)
	}
	return protocolContext{Version: selected, Modern: false}, nil
}

func modernRequestIntent(method, headerVersion, metadataVersion string) bool {
	if method == "server/discover" {
		return true
	}
	for _, version := range []string{headerVersion, metadataVersion} {
		if version == "" {
			continue
		}
		if buildinfo.IsModernProtocol(version) || !buildinfo.IsLegacyProtocol(version) {
			return true
		}
	}
	return false
}

func selectLegacyProtocol(headerVersion, metadataVersion, initializeVersion string) string {
	for _, version := range []string{initializeVersion, headerVersion, metadataVersion} {
		if buildinfo.IsLegacyProtocol(version) {
			return version
		}
	}
	if initializeVersion != "" {
		return buildinfo.LatestLegacyProtocol
	}
	// 2025-03-26 predates the mandatory protocol-version header and is the
	// safest interpretation for a request with no negotiated version attached.
	return "2025-03-26"
}

func initializeProtocolVersion(rpc RPCRequest) string {
	if rpc.Method != "initialize" {
		return ""
	}
	var parameters struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(rpc.Params, &parameters) != nil {
		return ""
	}
	return strings.TrimSpace(parameters.ProtocolVersion)
}

func routingName(rpc RPCRequest) (string, bool, error) {
	var parameters struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
	}
	switch rpc.Method {
	case "tools/call", "prompts/get":
		if err := json.Unmarshal(rpc.Params, &parameters); err != nil {
			return "", true, err
		}
		return parameters.Name, true, nil
	case "resources/read":
		if err := json.Unmarshal(rpc.Params, &parameters); err != nil {
			return "", true, err
		}
		return parameters.URI, true, nil
	default:
		return "", false, nil
	}
}

func singleHeader(headers http.Header, name string) (string, bool, error) {
	values := headers.Values(name)
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) != 1 {
		return "", true, fmt.Errorf("%s must appear exactly once", name)
	}
	return values[0], true, nil
}

func safePlainHeaderValue(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

func decodeMirroredHeader(value string) (string, error) {
	const prefix = "=?base64?"
	const suffix = "?="
	if strings.HasPrefix(value, prefix) && strings.HasSuffix(value, suffix) {
		encoded := strings.TrimSuffix(strings.TrimPrefix(value, prefix), suffix)
		if encoded == "" {
			return "", fmt.Errorf("empty base64 payload")
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", fmt.Errorf("invalid base64 payload")
		}
		if !utf8.Valid(decoded) {
			return "", fmt.Errorf("decoded value is not UTF-8")
		}
		return string(decoded), nil
	}
	if strings.HasPrefix(value, prefix) || strings.HasSuffix(value, suffix) {
		return "", fmt.Errorf("invalid base64 sentinel")
	}
	if !safePlainHeaderValue(value) {
		return "", fmt.Errorf("plain value contains unsafe characters")
	}
	return value, nil
}

func headerFailure(message string) *requestFailure {
	return &requestFailure{Status: http.StatusBadRequest, Error: RPCError{Code: headerMismatchCode, Message: "Header mismatch: " + message}}
}

func invalidParamsFailure(message string) *requestFailure {
	return &requestFailure{Status: http.StatusBadRequest, Error: RPCError{Code: -32602, Message: message}}
}

func unsupportedProtocolFailure(requested string) *requestFailure {
	supported := append([]string(nil), buildinfo.SupportedProtocolVersions...)
	return &requestFailure{
		Status: http.StatusBadRequest,
		Error: RPCError{
			Code:    unsupportedProtocolCode,
			Message: "Unsupported protocol version.",
			Data: map[string]any{
				"supported": supported,
				"requested": requested,
			},
		},
	}
}

func firstError(errors ...error) error {
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return fmt.Errorf("invalid request headers")
}
