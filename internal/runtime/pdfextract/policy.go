package pdfextract

import (
	"errors"
	"math"
	"time"
	"unicode/utf8"
)

type Policy struct {
	MaxInputBytes            int64
	MaxPages                 int
	MaxTextRunes             int
	ExtractionTimeout        time.Duration
	MemoryBytes              int64
	TempBytes                int64
	CPUs                     float64
	PIDs                     int
	AllowUnsandboxedFallback bool
}

func DefaultPolicy() Policy {
	return Policy{
		MaxInputBytes:            MaxInputBytes,
		MaxPages:                 MaxPages,
		MaxTextRunes:             MaxTextRunes,
		ExtractionTimeout:        ExtractionBudget,
		MemoryBytes:              512 << 20,
		TempBytes:                64 << 20,
		CPUs:                     1,
		PIDs:                     32,
		AllowUnsandboxedFallback: false,
	}
}

func (p Policy) Validate() error {
	if p.MaxInputBytes < 1 || p.MaxInputBytes == math.MaxInt64 || p.MaxPages < 1 || p.MaxTextRunes < 1 || p.MaxTextRunes > (math.MaxInt-256)/utf8.UTFMax || p.ExtractionTimeout <= 0 || p.MemoryBytes < 1 || p.TempBytes < 1 || p.CPUs <= 0 || math.IsNaN(p.CPUs) || math.IsInf(p.CPUs, 0) || p.PIDs < 1 {
		return errors.New("invalid PDF extraction policy")
	}
	return nil
}

func (p Policy) stdoutBytes() int { return p.MaxTextRunes*utf8.UTFMax + 256 }
