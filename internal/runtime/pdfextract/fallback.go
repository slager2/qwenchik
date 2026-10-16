package pdfextract

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
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
func extractPureGoSubprocess(ctx context.Context, document []byte) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if len(document) > MaxInputBytes {
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
	runCtx, cancel := context.WithTimeout(ctx, ExtractionBudget)
	defer cancel()
	command := exec.CommandContext(runCtx, executable)
	command.Env = workerEnvironment(token)
	command.Stdin = bytes.NewReader(document)
	stdout := &limitedBuffer{limit: maxStdoutBytes}
	stderr := &limitedBuffer{limit: maxStderrBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()
	if err := runCtx.Err(); err != nil {
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
	result, err := parseOutput(stdout.Bytes())
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
	debug.SetMemoryLimit(256 << 20)
	debug.SetMaxStack(16 << 20)
	debug.SetMaxThreads(32)
	document, err := io.ReadAll(io.LimitReader(os.Stdin, MaxInputBytes+1))
	if err != nil {
		return 1
	}
	if len(document) > MaxInputBytes {
		return 26
	}
	result, err := parsePureGoPDF(document)
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
	defer func() {
		if recover() != nil {
			result = Result{}
			err = ErrMalformed
		}
	}()
	if len(document) > MaxInputBytes {
		return Result{}, ErrInputTooLarge
	}
	reader, err := pdf.NewReader(bytes.NewReader(document), int64(len(document)))
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
	if pageCount > MaxPages {
		return Result{}, ErrTooManyPages
	}
	if pageCount < 1 {
		return Result{}, ErrMalformed
	}

	var text strings.Builder
	text.Grow(4_096)
	textRunes := 0
	fonts := make(map[string]*pdf.Font)
	pageTextLimit := MaxTextRunes*utf8.UTFMax + 1
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
		if textRunes > MaxTextRunes {
			return Result{}, ErrTextTooLarge
		}
	}
	fullText := text.String()
	if strings.TrimSpace(fullText) == "" {
		return Result{}, ErrNoText
	}
	return Result{Text: fullText, PageCount: pageCount}, nil
}
