// zeta-setup installs the libzeta.a native library for the
// github.com/genezhang/zeta-go/embedded package.
//
// The archive is downloaded from the zeta-go GitHub Releases page for the
// requested version, sha256-verified against the published SHA256SUMS file,
// and placed at <prefix>/lib/zeta/libzeta.a. The embedded package's cgo
// preamble references /usr/local/lib/zeta/libzeta.a by default — install
// there unless you know what you're doing.
//
// Typical usage:
//
//	go install github.com/genezhang/zeta-go/cmd/zeta-setup@latest
//	sudo zeta-setup install
//	# now  go build ./...  works
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	defaultVersion = "v0.1.0"
	defaultPrefix  = "/usr/local"
	releaseBaseURL = "https://github.com/genezhang/zeta-go/releases/download"

	libSubDir   = "lib/zeta"
	libName     = "libzeta.a"
	versionFile = "VERSION"
)

var supportedPlatforms = map[string]bool{
	"linux-amd64":  true,
	"linux-arm64":  true,
	"darwin-amd64": true,
	"darwin-arm64": true,
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)

	switch cmd {
	case "install":
		version := fs.String("version", defaultVersion, "Zeta version to install (e.g. v0.1.0)")
		prefix := fs.String("prefix", defaultPrefix, "install prefix (libzeta.a goes to <prefix>/"+libSubDir+"/"+libName+")")
		force := fs.Bool("force", false, "reinstall even if the same version is already present")
		noVerify := fs.Bool("no-verify", false, "skip sha256 checksum verification")
		_ = fs.Parse(os.Args[2:])
		if err := install(*version, *prefix, *force, *noVerify); err != nil {
			fatal(err)
		}

	case "uninstall":
		prefix := fs.String("prefix", defaultPrefix, "install prefix")
		_ = fs.Parse(os.Args[2:])
		if err := uninstall(*prefix); err != nil {
			fatal(err)
		}

	case "version":
		prefix := fs.String("prefix", defaultPrefix, "install prefix")
		_ = fs.Parse(os.Args[2:])
		v, err := installedVersion(*prefix)
		if err != nil {
			fatal(err)
		}
		fmt.Println(v)

	case "-h", "--help", "help":
		usage()

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func install(version, prefix string, force, noVerify bool) error {
	platform := currentPlatform()
	if !supportedPlatforms[platform] {
		return fmt.Errorf("unsupported platform %q (supported: linux-amd64, linux-arm64, darwin-amd64, darwin-arm64)", platform)
	}

	targetDir := filepath.Join(prefix, libSubDir)
	targetFile := filepath.Join(targetDir, libName)
	versionPath := filepath.Join(targetDir, versionFile)

	if !force {
		if data, err := os.ReadFile(versionPath); err == nil {
			if strings.TrimSpace(string(data)) == version {
				fmt.Printf("zeta %s already installed at %s\n", version, targetFile)
				return nil
			}
		}
	}

	artifactName := fmt.Sprintf("libzeta-%s.a", platform)
	artifactURL := fmt.Sprintf("%s/%s/%s", releaseBaseURL, version, artifactName)

	fmt.Printf("downloading %s\n", artifactURL)
	tmp, err := downloadToTemp(artifactURL)
	if err != nil {
		return fmt.Errorf("download %s: %w", artifactURL, err)
	}
	defer os.Remove(tmp)

	if !noVerify {
		sumsURL := fmt.Sprintf("%s/%s/SHA256SUMS", releaseBaseURL, version)
		fmt.Printf("verifying sha256 against %s\n", sumsURL)
		if err := verifySha256(tmp, sumsURL, artifactName); err != nil {
			return fmt.Errorf("sha256 verify: %w", err)
		}
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w (try: sudo zeta-setup install)", targetDir, err)
	}
	if err := copyFile(tmp, targetFile); err != nil {
		return fmt.Errorf("install to %s: %w (try: sudo zeta-setup install)", targetFile, err)
	}
	if err := os.WriteFile(versionPath, []byte(version+"\n"), 0o644); err != nil {
		return fmt.Errorf("write version file: %w", err)
	}

	fmt.Printf("installed zeta %s → %s\n", version, targetFile)
	if prefix != defaultPrefix {
		fmt.Fprintf(os.Stderr, "\nNOTE: prefix is not %s; you will need to set\n"+
			"  CGO_LDFLAGS=\"%s -lpthread -ldl -lm -lstdc++ -lgcc_s -lrt\"\n"+
			"(or the macOS equivalent) before building against github.com/genezhang/zeta-go/embedded.\n",
			defaultPrefix, targetFile)
	}
	return nil
}

func uninstall(prefix string) error {
	targetDir := filepath.Join(prefix, libSubDir)
	if _, err := os.Stat(targetDir); errors.Is(err, os.ErrNotExist) {
		fmt.Printf("nothing to remove at %s\n", targetDir)
		return nil
	}
	if err := os.RemoveAll(targetDir); err != nil {
		return fmt.Errorf("remove %s: %w (try: sudo zeta-setup uninstall)", targetDir, err)
	}
	fmt.Printf("removed %s\n", targetDir)
	return nil
}

func installedVersion(prefix string) (string, error) {
	versionPath := filepath.Join(prefix, libSubDir, versionFile)
	data, err := os.ReadFile(versionPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("zeta is not installed at %s", filepath.Join(prefix, libSubDir))
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func currentPlatform() string {
	return fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
}

func downloadToTemp(url string) (string, error) {
	resp, err := http.Get(url) //nolint:gosec // URL is built from trusted constants + semver flag.
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "libzeta-*.a")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

func verifySha256(file, sumsURL, artifactName string) error {
	resp, err := http.Get(sumsURL) //nolint:gosec // URL is built from trusted constants + semver flag.
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch SHA256SUMS: HTTP %d", resp.StatusCode)
	}
	sums, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var want string
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == artifactName {
			want = fields[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no entry for %s in SHA256SUMS", artifactName)
	}

	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("mismatch: want %s, got %s", want, got)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, 0o644)
}

func usage() {
	fmt.Fprintf(os.Stderr, `zeta-setup — install libzeta.a for github.com/genezhang/zeta-go/embedded

USAGE
    zeta-setup <command> [flags]

COMMANDS
    install       Download and install libzeta.a for the current platform
    uninstall     Remove an installed libzeta.a
    version       Print the version of the installed libzeta.a

INSTALL FLAGS
    -version <v>   Zeta version to install (default: %s)
    -prefix <dir>  Install prefix (default: %s)
    -force         Reinstall even if the same version is present
    -no-verify     Skip sha256 checksum verification

EXAMPLES
    sudo zeta-setup install
    zeta-setup install -version v0.2.0
    zeta-setup install -prefix $HOME/.local     # then set CGO_LDFLAGS manually
    zeta-setup uninstall
    zeta-setup version
`, defaultVersion, defaultPrefix)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "zeta-setup: %s\n", err)
	os.Exit(1)
}
