package pdfextract

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"

	pdf "github.com/dslipak/pdf"
)

func init() {
	if os.Getenv(workerModeEnv) != workerMode {
		return
	}
	token := os.Getenv(workerTokenEnv)
	if len(token) != 24 {
		return
	}
	if _, err := hex.DecodeString(token); err != nil {
		return
	}
	os.Exit(runPureGoWorker())
}

// dslipak/pdf has no context-aware parser API, so it runs only in a re-executed
// worker that CommandContext can kill. This is a wall-clock deadline, not CPU
// time accounting.
func extractPureGoFile(ctx context.Context, file *os.File, size int64, policy Policy) (Result, error) {
	if !policy.AllowUnsandboxedFallback {
		return Result{}, ErrUnavailable
	}
	if err := policy.Validate(); err != nil {
		return Result{}, err
	}
	if size > policy.MaxInputBytes {
		return Result{}, ErrInputTooLarge
	}
	executable, err := os.Executable()
	if err != nil {
		return Result{}, fmt.Errorf("%w: pure-Go worker executable is unavailable", ErrUnavailable)
	}
	token, err := randomID()
	if err != nil {
		return Result{}, fmt.Errorf("%w: pure-Go worker token could not be generated", ErrUnavailable)
	}
	command := exec.CommandContext(ctx, executable)
	command.WaitDelay = time.Second
	encoded, err := json.Marshal(policy)
	if err != nil {
		return Result{}, err
	}
	command.Env = append(workerEnvironment(token), "QAGENT_PDFEXTRACT_POLICY="+string(encoded))
	command.ExtraFiles = []*os.File{file}
	stdout := &limitedBuffer{limit: policy.stdoutBytes()}
	stderr := &limitedBuffer{limit: maxStderrBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if stdout.exceeded || stderr.exceeded {
		return Result{}, ErrOutputExceeded
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			switch exitErr.ExitCode() {
			case 23:
				return Result{}, ErrEncrypted
			case 24:
				return Result{}, ErrTooManyPages
			case 25:
				return Result{}, ErrTextTooLarge
			case 26:
				return Result{}, ErrInputTooLarge
			case 27:
				return Result{}, ErrNoText
			default:
				return Result{}, ErrMalformed
			}
		}
		return Result{}, fmt.Errorf("%w: pure-Go worker could not be started", ErrUnavailable)
	}
	result, err := parseOutputWithPolicy(stdout.Bytes(), policy)
	if err != nil {
		return Result{}, err
	}
	result.Method = methodPureGo
	return result, nil
}

func workerEnvironment(token string) []string {
	return []string{workerModeEnv + "=" + workerMode, workerTokenEnv + "=" + token}
}

func runPureGoWorker() int {
	var policy Policy
	if json.Unmarshal([]byte(os.Getenv("QAGENT_PDFEXTRACT_POLICY")), &policy) != nil || policy.Validate() != nil || !policy.AllowUnsandboxedFallback {
		return 1
	}
	debug.SetMemoryLimit(policy.MemoryBytes)
	debug.SetMaxStack(16 << 20)
	debug.SetMaxThreads(32)
	file := os.NewFile(3, "pdf-input")
	if file == nil {
		return 1
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return 1
	}
	if info.Size() > policy.MaxInputBytes {
		return 26
	}
	result, err := parsePureGoReader(file, info.Size(), policy)
	if err != nil {
		switch {
		case errors.Is(err, ErrEncrypted):
			return 23
		case errors.Is(err, ErrTooManyPages):
			return 24
		case errors.Is(err, ErrTextTooLarge):
			return 25
		case errors.Is(err, ErrInputTooLarge):
			return 26
		case errors.Is(err, ErrNoText):
			return 27
		default:
			return 1
		}
	}
	if _, err := fmt.Fprintf(os.Stdout, "%s%d\n%s", outputPrefix, result.PageCount, result.Text); err != nil {
		return 1
	}
	return 0
}

func parsePureGoPDF(document []byte) (result Result, err error) {
	return parsePureGoReader(bytes.NewReader(document), int64(len(document)), DefaultPolicy())
}

func parsePureGoReader(document io.ReaderAt, size int64, policy Policy) (result Result, err error) {
	defer func() {
		if recover() != nil {
			result = Result{}
			err = ErrMalformed
		}
	}()
	if size > policy.MaxInputBytes {
		return Result{}, ErrInputTooLarge
	}
	reader, err := pdf.NewReader(document, size)
	if err != nil {
		if errors.Is(err, pdf.ErrInvalidPassword) || strings.Contains(strings.ToLower(err.Error()), "encrypt") {
			return Result{}, ErrEncrypted
		}
		return Result{}, ErrMalformed
	}
	if !reader.Trailer().Key("Encrypt").IsNull() {
		return Result{}, ErrEncrypted
	}
	pageCount := reader.NumPage()
	if pageCount > policy.MaxPages {
		return Result{}, ErrTooManyPages
	}
	if pageCount < 1 {
		return Result{}, ErrMalformed
	}

	var text strings.Builder
	text.Grow(4_096)
	textRunes := 0
	fonts := make(map[string]*pdf.Font)
	pageTextLimit := policy.MaxTextRunes*utf8.UTFMax + 1
	for pageNumber := 1; pageNumber <= pageCount; pageNumber++ {
		page := reader.Page(pageNumber)
		if page.V.IsNull() {
			return Result{}, ErrMalformed
		}
		pageText, pageErr := page.GetPlainText(fonts)
		if pageErr != nil {
			return Result{}, ErrMalformed
		}
		if len(pageText) >= pageTextLimit {
			return Result{}, ErrTextTooLarge
		}
		cleanPage, cleanErr := stripPDFControls(pageText)
		if cleanErr != nil {
			return Result{}, cleanErr
		}
		if pageNumber > 1 {
			text.WriteByte('\n')
			textRunes++
		}
		text.WriteString(cleanPage)
		textRunes += utf8.RuneCountInString(cleanPage)
		if textRunes > policy.MaxTextRunes {
			return Result{}, ErrTextTooLarge
		}
	}
	fullText := text.String()
	if strings.TrimSpace(fullText) == "" {
		return Result{}, ErrNoText
	}
	return Result{Text: fullText, PageCount: pageCount}, nil
}
