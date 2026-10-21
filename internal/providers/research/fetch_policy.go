package research

import (
	"errors"
	"math"
	"strings"
	"time"
)

type FetchPolicy struct {
	TaskFetchBudgetBytes  int64
	SourceFetchBytes      int64
	PDFBytes              int64
	PDFDownloadTimeout    time.Duration
	SourceDownloadTimeout time.Duration
	TempRoot              string
	TempTaskBytes         int64
	TempGlobalBytes       int64
}

func DefaultFetchPolicy() FetchPolicy {
	return FetchPolicy{128 << 20, 16 << 20, 64 << 20, 120 * time.Second, 20 * time.Second, "./data/fetch-temp", 128 << 20, 256 << 20}
}

func (p FetchPolicy) Validate() error {
	for _, n := range []int64{p.TaskFetchBudgetBytes, p.SourceFetchBytes, p.PDFBytes, p.TempTaskBytes, p.TempGlobalBytes} {
		if n <= 0 || n == math.MaxInt64 {
			return errors.New("invalid fetch byte limit")
		}
	}
	if p.SourceFetchBytes > p.TaskFetchBudgetBytes || p.PDFBytes > p.TaskFetchBudgetBytes || p.PDFBytes > p.TempTaskBytes || p.TempTaskBytes > p.TempGlobalBytes || p.PDFDownloadTimeout <= 0 || p.SourceDownloadTimeout <= 0 || strings.TrimSpace(p.TempRoot) == "" {
		return errors.New("invalid fetch policy")
	}
	return nil
}
