package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrRendererUnavailable means no headless browser is configured or found.
var ErrRendererUnavailable = errors.New("PDF renderer is unavailable")

// Renderer turns a complete HTML document into PDF bytes.
type Renderer interface {
	Render(ctx context.Context, html []byte) ([]byte, error)
}

const (
	DefaultTimeout     = 90 * time.Second
	DefaultMaxPDFBytes = 64 << 20
	maxHTMLBytes       = 16 << 20
	maxStderrBytes     = 16 << 10
)

// Candidates probed, in order, when PDF_RENDERER_BIN is not set.
var chromeCandidates = []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "brave-browser", "brave", "microsoft-edge"}

// DetectChrome resolves the renderer binary. An explicit path or command name
// must resolve; otherwise well-known Chromium-family browsers are probed.
func DetectChrome(explicit string) (string, error) {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		path, err := exec.LookPath(explicit)
		if err != nil {
			return "", fmt.Errorf("%w: PDF_RENDERER_BIN %q not found", ErrRendererUnavailable, explicit)
		}
		return path, nil
	}
	for _, name := range chromeCandidates {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", ErrRendererUnavailable
}

// Chrome renders through a headless Chromium-family browser.
type Chrome struct {
	Binary   string
	Timeout  time.Duration
	MaxBytes int64
	// TempDir is the parent of the per-render private directory; empty
	// means os.TempDir().
	TempDir string
}

// NewChrome returns a renderer for the given binary with default limits.
func NewChrome(binary string) *Chrome {
	return &Chrome{Binary: binary, Timeout: DefaultTimeout, MaxBytes: DefaultMaxPDFBytes}
}

func (c *Chrome) Render(ctx context.Context, html []byte) ([]byte, error) {
	if c == nil || c.Binary == "" {
		return nil, ErrRendererUnavailable
	}
	if len(html) == 0 || len(html) > maxHTMLBytes {
		return nil, errors.New("report HTML is empty or too large")
	}
	timeout, limit := c.Timeout, c.MaxBytes
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if limit <= 0 {
		limit = DefaultMaxPDFBytes
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dir, err := os.MkdirTemp(c.TempDir, "qwenchik-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	input := filepath.Join(dir, "report.html")
	output := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(input, html, 0o600); err != nil {
		return nil, err
	}
	inputURL := "file://" + filepath.ToSlash(input)
	if !strings.HasPrefix(filepath.ToSlash(input), "/") {
		inputURL = "file:///" + filepath.ToSlash(input)
	}
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-default-apps",
		"--disable-sync",
		"--disable-features=Translate,MediaRouter,OptimizationHints",
		"--no-first-run",
		"--no-default-browser-check",
		"--mute-audio",
		"--hide-scrollbars",
		// The document needs no network: refuse every host lookup and
		// route any remaining request to a closed local port.
		"--host-resolver-rules=MAP * ~NOTFOUND",
		"--proxy-server=127.0.0.1:9",
		"--proxy-bypass-list=<-loopback>",
		"--user-data-dir=" + filepath.Join(dir, "profile"),
		"--no-pdf-header-footer",
		"--print-to-pdf=" + output,
		// Scripts stay disabled through the document CSP (script-src
		// 'none'); --blink-settings=scriptEnabled=false makes headless
		// printing silently produce no file in current Chrome.
		inputURL,
	}
	cmd := exec.Command(c.Binary, args...)
	cmd.Dir = dir
	cmd.Env = rendererEnv(dir)
	var stderr limitedBuffer
	stderr.limit = maxStderrBytes
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	prepareProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRendererUnavailable, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
		// Chrome may leave helper processes behind; reap the group.
		killProcessGroup(cmd)
	case <-ctx.Done():
		killProcessGroup(cmd)
		<-done
		return nil, fmt.Errorf("PDF rendering aborted: %w", ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("PDF renderer failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	file, err := os.Open(output)
	if err != nil {
		return nil, errors.New("PDF renderer produced no output")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("rendered PDF exceeds the size limit")
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		return nil, errors.New("PDF renderer produced an invalid file")
	}
	return data, nil
}

// rendererEnv passes only what a headless browser needs, with HOME and
// caches pointed into the private render directory.
func rendererEnv(dir string) []string {
	env := []string{"HOME=" + dir, "XDG_CONFIG_HOME=" + filepath.Join(dir, "config"), "XDG_CACHE_HOME=" + filepath.Join(dir, "cache")}
	// Chrome binds its singleton socket under TMPDIR; Unix socket paths are
	// limited to ~108 bytes, so prefer the short system directory.
	tmp := dir
	if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
		tmp = "/tmp"
	}
	env = append(env, "TMPDIR="+tmp)
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "FONTCONFIG_PATH", "FONTCONFIG_FILE", "SYSTEMROOT", "WINDIR", "LOCALAPPDATA", "APPDATA", "TEMP", "TMP"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.Len(); remaining > 0 {
		if len(p) > remaining {
			b.Buffer.Write(p[:remaining])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}
