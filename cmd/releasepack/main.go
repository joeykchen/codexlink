package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/joeykchen/codexlink/internal/distribution"
)

func main() {
	var (
		goos        = flag.String("os", "", "target GOOS")
		goarch      = flag.String("arch", "", "target GOARCH")
		outputDir   = flag.String("output", "dist", "output directory")
		codexlink   = flag.String("codexlink", "", "CodexLink binary")
		cloudflared = flag.String("cloudflared", "", "cloudflared binary")
		license     = flag.String("license", "LICENSE", "license file")
		readme      = flag.String("readme", "README.md", "English README")
		readmeZH    = flag.String("readme-zh", "README.zh-CN.md", "Chinese README")
		installerSH = flag.String("install-sh", "install.sh", "Unix installer")
		installerPS = flag.String("install-ps1", "install.ps1", "PowerShell installer")
	)
	flag.Parse()
	target, err := distribution.ParseTarget(*goos, *goarch)
	fatalIf(err)
	required := map[string]string{"codexlink": *codexlink, "cloudflared": *cloudflared}
	for name, value := range required {
		if value == "" {
			fatalIf(fmt.Errorf("--%s is required", name))
		}
	}
	mode := fs.FileMode(0o755)
	entries := []distribution.Entry{
		{Source: *codexlink, Name: target.Executable("codexlink"), Mode: mode},
		{Source: *cloudflared, Name: target.Executable("cloudflared"), Mode: mode},
		{Source: *license, Name: "LICENSE", Mode: 0o644},
		{Source: *readme, Name: "README.md", Mode: 0o644},
		{Source: *readmeZH, Name: "README.zh-CN.md", Mode: 0o644},
	}
	if target.OS == "windows" {
		entries = append(entries, distribution.Entry{Source: *installerPS, Name: "install.ps1", Mode: 0o644})
	} else {
		entries = append(entries, distribution.Entry{Source: *installerSH, Name: "install.sh", Mode: mode})
	}
	output := filepath.Join(*outputDir, target.ArtifactName("codexlink"))
	fatalIf(distribution.WriteArchive(target, output, entries))
	checksum, err := distribution.WriteSHA256(output)
	fatalIf(err)
	fmt.Println(output)
	fmt.Println(checksum)
}

func fatalIf(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "releasepack:", err)
	os.Exit(1)
}
