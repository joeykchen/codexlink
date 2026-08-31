package buildinfo

const (
	ProductName          = "CodexLink"
	ServiceName          = "codexlink-bridge"
	Version              = "1.0.0"
	ModernProtocol       = "2026-07-28"
	LatestLegacyProtocol = "2025-11-25"
)

// SupportedProtocolVersions lists the modern stateless protocol first, then
// the Streamable HTTP revisions supported through the legacy initialize flow.
// The deprecated 2024 HTTP+SSE transport is deliberately not advertised.
var SupportedProtocolVersions = []string{
	ModernProtocol,
	LatestLegacyProtocol,
	"2025-06-18",
	"2025-03-26",
}

func SupportsProtocol(version string) bool {
	for _, candidate := range SupportedProtocolVersions {
		if version == candidate {
			return true
		}
	}
	return false
}

func IsModernProtocol(version string) bool { return version == ModernProtocol }

func IsLegacyProtocol(version string) bool {
	return version != ModernProtocol && SupportsProtocol(version)
}
