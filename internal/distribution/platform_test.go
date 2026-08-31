package distribution

import "testing"

func TestTargetNaming(t *testing.T) {
	tests := []struct {
		os, arch, artifact, cloudflared string
	}{
		{"darwin", "amd64", "codexlink_darwin_amd64.tar.gz", "cloudflared-darwin-amd64.tgz"},
		{"linux", "arm64", "codexlink_linux_arm64.tar.gz", "cloudflared-linux-arm64"},
		{"windows", "amd64", "codexlink_windows_amd64.zip", "cloudflared-windows-amd64.exe"},
	}
	for _, test := range tests {
		target, err := ParseTarget(test.os, test.arch)
		if err != nil {
			t.Fatal(err)
		}
		if got := target.ArtifactName("codexlink"); got != test.artifact {
			t.Fatalf("artifact: got %q want %q", got, test.artifact)
		}
		if got := target.CloudflaredAsset(); got != test.cloudflared {
			t.Fatalf("cloudflared: got %q want %q", got, test.cloudflared)
		}
	}
	if _, err := ParseTarget("plan9", "amd64"); err == nil {
		t.Fatal("unsupported target was accepted")
	}
}
