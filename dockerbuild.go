package dockerbuild

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"text/template"
)

//go:embed Dockerfile.tmpl
var defaultDockerfileTmpl string

type Config struct {
	AppName string
	Version string
}

func Run(cfg Config, args []string) {
	binaryName := cfg.AppName + "-server"

	fs := flag.NewFlagSet("docker-build", flag.ContinueOnError)
	base := fs.String("base", "debian:12-slim", "base Docker image")
	dockerfile := fs.String("dockerfile", "", "path to custom Dockerfile (skips embedded template)")
	tag := fs.String("tag", "", "image tag (default: localitas/{app}:{version})")
	push := fs.Bool("push", false, "push image after building")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s docker-build [options]\n\n", binaryName)
		fmt.Fprintf(os.Stderr, "Build a Docker image containing the linux/amd64 binary.\n\n")
		fmt.Fprintf(os.Stderr, "Prerequisites:\n")
		fmt.Fprintf(os.Stderr, "  The linux/amd64 binary must exist next to this binary.\n")
		fmt.Fprintf(os.Stderr, "  Run 'make deploy-build' from the project root first.\n")
		fmt.Fprintf(os.Stderr, "  Expected path: ./%s-linux-amd64 (in the same directory)\n\n", binaryName)
		fmt.Fprintf(os.Stderr, "Options:\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  # Build with default base image (debian:12-slim)\n")
		fmt.Fprintf(os.Stderr, "  ./%s docker-build\n\n", binaryName)
		fmt.Fprintf(os.Stderr, "  # Use a different base OS\n")
		fmt.Fprintf(os.Stderr, "  ./%s docker-build --base ubuntu:24.04\n\n", binaryName)
		fmt.Fprintf(os.Stderr, "  # Use a fully custom Dockerfile\n")
		fmt.Fprintf(os.Stderr, "  ./%s docker-build --dockerfile ./my.Dockerfile\n\n", binaryName)
		fmt.Fprintf(os.Stderr, "  # Custom tag and push to registry\n")
		fmt.Fprintf(os.Stderr, "  ./%s docker-build --tag ghcr.io/myorg/%s:latest --push\n\n", binaryName, cfg.AppName)
	}

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if len(fs.Args()) > 0 && (fs.Args()[0] == "help" || fs.Args()[0] == "--help") {
		fs.Usage()
		os.Exit(0)
	}

	if *tag == "" {
		*tag = fmt.Sprintf("localitas/%s:%s", cfg.AppName, cfg.Version)
	}

	selfPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot find own executable: %v\n", err)
		os.Exit(1)
	}

	// Determine linux binary path
	// If we're already linux, use ourselves. Otherwise look for the linux binary next to us.
	var linuxBinaryPath string
	if runtime.GOOS == "linux" {
		linuxBinaryPath = selfPath
	} else {
		dir := filepath.Dir(selfPath)
		linuxBinaryPath = filepath.Join(dir, binaryName+"-linux-amd64")
		if _, err := os.Stat(linuxBinaryPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error: linux/amd64 binary not found\n\n")
			fmt.Fprintf(os.Stderr, "Expected: %s\n\n", linuxBinaryPath)
			fmt.Fprintf(os.Stderr, "This binary needs a linux/amd64 build of itself to package into\n")
			fmt.Fprintf(os.Stderr, "the Docker image. Build it first:\n\n")
			fmt.Fprintf(os.Stderr, "  make deploy-build\n\n")
			fmt.Fprintf(os.Stderr, "Then run docker-build from the same directory:\n\n")
			fmt.Fprintf(os.Stderr, "  cd saas/deploy/bin && ./%s-darwin-arm64 docker-build\n\n", binaryName)
			os.Exit(1)
		}
	}

	tmpDir, err := os.MkdirTemp("", "docker-build-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	// Copy linux binary into build context
	srcData, err := os.ReadFile(linuxBinaryPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read binary: %v\n", err)
		os.Exit(1)
	}
	dstPath := filepath.Join(tmpDir, binaryName)
	if err := os.WriteFile(dstPath, srcData, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write binary to build context: %v\n", err)
		os.Exit(1)
	}

	// Prepare Dockerfile
	dockerfilePath := filepath.Join(tmpDir, "Dockerfile")
	if *dockerfile != "" {
		// Custom Dockerfile — copy it into build context
		customData, err := os.ReadFile(*dockerfile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot read custom Dockerfile: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(dockerfilePath, customData, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "cannot write Dockerfile: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Render embedded template
		tmpl, err := template.New("Dockerfile").Parse(defaultDockerfileTmpl)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot parse Dockerfile template: %v\n", err)
			os.Exit(1)
		}
		f, err := os.Create(dockerfilePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot create Dockerfile: %v\n", err)
			os.Exit(1)
		}
		err = tmpl.Execute(f, struct {
			Base       string
			BinaryName string
		}{
			Base:       *base,
			BinaryName: binaryName,
		})
		f.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot render Dockerfile: %v\n", err)
			os.Exit(1)
		}
	}

	// Build
	fmt.Printf("Building Docker image: %s\n", *tag)
	buildArgs := []string{"build", "-t", *tag, "--platform", "linux/amd64", tmpDir}
	cmd := exec.Command("docker", buildArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "docker build failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Image built: %s\n", *tag)

	if *push {
		fmt.Printf("Pushing %s...\n", *tag)
		pushCmd := exec.Command("docker", "push", *tag)
		pushCmd.Stdout = os.Stdout
		pushCmd.Stderr = os.Stderr
		if err := pushCmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "docker push failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Pushed: %s\n", *tag)
	}

}
