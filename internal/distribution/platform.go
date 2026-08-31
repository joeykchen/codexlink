package distribution

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// Target identifies one supported release platform. Keeping platform naming in
// one package prevents installer, CI, and packaging scripts from drifting.
type Target struct {
	OS   string
	Arch string
}

var supportedTargets = map[Target]struct{}{
	{OS: "darwin", Arch: "amd64"}:  {},
	{OS: "darwin", Arch: "arm64"}:  {},
	{OS: "linux", Arch: "amd64"}:   {},
	{OS: "linux", Arch: "arm64"}:   {},
	{OS: "windows", Arch: "amd64"}: {},
	{OS: "windows", Arch: "arm64"}: {},
}

func CurrentTarget() (Target, error) {
	return ParseTarget(runtime.GOOS, runtime.GOARCH)
}

func ParseTarget(goos, goarch string) (Target, error) {
	target := Target{OS: strings.ToLower(strings.TrimSpace(goos)), Arch: strings.ToLower(strings.TrimSpace(goarch))}
	if _, ok := supportedTargets[target]; !ok {
		return Target{}, fmt.Errorf("unsupported release target %s/%s", target.OS, target.Arch)
	}
	return target, nil
}

func (t Target) String() string { return t.OS + "/" + t.Arch }

func (t Target) Executable(name string) string {
	if t.OS == "windows" {
		return name + ".exe"
	}
	return name
}

func (t Target) ArchiveExtension() string {
	if t.OS == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

// ArtifactName intentionally remains stable across versions. GitHub's
// /releases/latest/download endpoint can therefore install the latest release
// without requiring a JSON parser in the bootstrap script.
func (t Target) ArtifactName(product string) string {
	return fmt.Sprintf("%s_%s_%s%s", product, t.OS, t.Arch, t.ArchiveExtension())
}

func (t Target) CloudflaredAsset() string {
	switch t.OS {
	case "darwin":
		return fmt.Sprintf("cloudflared-darwin-%s.tgz", t.Arch)
	case "linux":
		return fmt.Sprintf("cloudflared-linux-%s", t.Arch)
	case "windows":
		return fmt.Sprintf("cloudflared-windows-%s.exe", t.Arch)
	default:
		panic("validated target has unknown OS")
	}
}

func (t Target) JoinExecutable(dir, name string) string {
	return filepath.Join(dir, t.Executable(name))
}
