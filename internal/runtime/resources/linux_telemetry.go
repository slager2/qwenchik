package resources

import (
	"encoding/csv"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

var (
	errInvalidLinuxCPUStat = errors.New("invalid Linux aggregate CPU counters")
	errInvalidLinuxGPUCSV  = errors.New("invalid nvidia-smi GPU telemetry CSV")
)

const linuxCPUCounterCount = 10

// linuxCPUCounters contains the counters from the aggregate "cpu" row in
// /proc/stat. Guest counters are retained for rollback detection, but are not
// added to the total because Linux already includes them in user and nice.
type linuxCPUCounters struct {
	values [linuxCPUCounterCount]uint64
	count  int
}

// parseLinuxCPUStat reads the aggregate CPU row, not the per-CPU rows. It
// accepts the four counters required by old kernels and the optional counters
// documented by Linux, while rejecting duplicate or malformed aggregate rows.
func parseLinuxCPUStat(data []byte) (linuxCPUCounters, error) {
	var result linuxCPUCounters
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}
		if found || len(fields) < 5 || len(fields) > linuxCPUCounterCount+1 {
			return linuxCPUCounters{}, errInvalidLinuxCPUStat
		}
		found = true
		result.count = len(fields) - 1
		for i, field := range fields[1:] {
			if !isDecimalDigits(field) {
				return linuxCPUCounters{}, errInvalidLinuxCPUStat
			}
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return linuxCPUCounters{}, errInvalidLinuxCPUStat
			}
			result.values[i] = value
		}
	}
	if !found {
		return linuxCPUCounters{}, errInvalidLinuxCPUStat
	}
	return result, nil
}

// linuxCPUUsage calculates usage from counter deltas. An unavailable metric is
// returned until a valid baseline and non-zero total delta are available.
func linuxCPUUsage(previous *linuxCPUCounters, current linuxCPUCounters, updatedAt time.Time) domain.Metric[float64] {
	if previous == nil {
		return domain.NewUnavailableMetricWithError[float64](domain.MetricUnitPercent, updatedAt, domain.MetricErrorTemporarilyUnavailable)
	}
	if previous.count < 4 || previous.count > linuxCPUCounterCount || current.count != previous.count {
		return domain.NewUnavailableMetricWithError[float64](domain.MetricUnitPercent, updatedAt, domain.MetricErrorCollectionFailed)
	}

	var totalDelta, idleDelta uint64
	for i := 0; i < current.count; i++ {
		if current.values[i] < previous.values[i] {
			return domain.NewUnavailableMetricWithError[float64](domain.MetricUnitPercent, updatedAt, domain.MetricErrorCollectionFailed)
		}
		delta := current.values[i] - previous.values[i]
		// The first eight counters (user through steal) form Linux's total.
		// Guest and guest_nice are already represented in user and nice.
		if i < 8 {
			var ok bool
			totalDelta, ok = addUint64(totalDelta, delta)
			if !ok {
				return domain.NewUnavailableMetricWithError[float64](domain.MetricUnitPercent, updatedAt, domain.MetricErrorCollectionFailed)
			}
		}
		if i == 3 || i == 4 { // idle and iowait
			var ok bool
			idleDelta, ok = addUint64(idleDelta, delta)
			if !ok {
				return domain.NewUnavailableMetricWithError[float64](domain.MetricUnitPercent, updatedAt, domain.MetricErrorCollectionFailed)
			}
		}
	}
	if totalDelta == 0 {
		return domain.NewUnavailableMetricWithError[float64](domain.MetricUnitPercent, updatedAt, domain.MetricErrorTemporarilyUnavailable)
	}
	if idleDelta > totalDelta {
		return domain.NewUnavailableMetricWithError[float64](domain.MetricUnitPercent, updatedAt, domain.MetricErrorCollectionFailed)
	}
	usage := float64(totalDelta-idleDelta) / float64(totalDelta) * 100
	return domain.NewExactMetric(usage, domain.MetricUnitPercent, updatedAt)
}

// parseLinuxCPUTemperature accepts a sysfs millidegree reading only when its
// label or type explicitly identifies a CPU sensor. The bool is false when the
// source is not identified as a CPU sensor; invalid selected readings produce
// an unavailable metric and true.
func parseLinuxCPUTemperature(label, sensorType string, raw []byte, updatedAt time.Time) (domain.Metric[float64], bool) {
	if !isLinuxCPUSensor(label, sensorType) {
		return domain.Metric[float64]{}, false
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || value < -100_000 || value > 200_000 {
		return domain.NewUnavailableMetricWithError[float64](domain.MetricUnitCelsius, updatedAt, domain.MetricErrorCollectionFailed), true
	}
	return domain.NewExactMetric(float64(value)/1000, domain.MetricUnitCelsius, updatedAt), true
}

// parseLinuxMemoryInfo extracts the bounded MemTotal and MemAvailable values
// from /proc/meminfo. Invalid or duplicate fields are unavailable individually.
func parseLinuxMemoryInfo(data []byte) (total, available *int64) {
	total = parseKB(data, "MemTotal:")
	available = parseKB(data, "MemAvailable:")
	if total != nil && *total <= 0 {
		total = nil
	}
	return total, available
}

// parseLinuxProcessComm removes only procfs's record terminator. Trimming other
// whitespace would turn a non-exact process name into a possible match.
func parseLinuxProcessComm(data []byte) string {
	value := string(data)
	return strings.TrimSuffix(value, "\n")
}

func parseLinuxProcessRSS(data []byte) *int64 {
	return parseKB(data, "VmRSS:")
}

func legacyLinuxGPUGuards(gpus []domain.GPUTelemetry) (*int64, *float64) {
	if len(gpus) == 0 {
		return nil, nil
	}
	freeKnown, temperatureKnown := true, true
	var minFree *int64
	var maxTemperature *float64
	for _, gpu := range gpus {
		if gpu.VRAMFree.Value == nil || gpu.VRAMFree.Source == domain.MetricSourceUnavailable {
			freeKnown = false
		} else {
			mib := *gpu.VRAMFree.Value / (1 << 20)
			if minFree == nil || mib < *minFree {
				minFree = &mib
			}
		}
		if gpu.Temperature.Value == nil || gpu.Temperature.Source == domain.MetricSourceUnavailable || *gpu.Temperature.Value < 0 {
			temperatureKnown = false
		} else if maxTemperature == nil || *gpu.Temperature.Value > *maxTemperature {
			value := *gpu.Temperature.Value
			maxTemperature = &value
		}
	}
	if !freeKnown {
		minFree = nil
	}
	if !temperatureKnown {
		maxTemperature = nil
	}
	return minFree, maxTemperature
}

func isLinuxCPUSensor(label, sensorType string) bool {
	labelClass := classifyLinuxCPUSensor(label, false)
	typeClass := classifyLinuxCPUSensor(sensorType, true)
	if labelClass < 0 || typeClass < 0 {
		return false
	}
	return labelClass > 0 || typeClass > 0
}

// classifyLinuxCPUSensor returns 1 for an explicit CPU sensor, -1 for an
// explicit non-CPU sensor, and 0 when the text does not identify either.
func classifyLinuxCPUSensor(value string, isType bool) int {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return 0
	}
	if isType {
		switch value {
		case "2": // hwmon temp*_type: CPU
			return 1
		case "3": // hwmon temp*_type: GPU
			return -1
		case "nvidia", "nouveau", "amdgpu", "i915", "xe":
			return -1
		}
	}
	normalized := strings.NewReplacer("_", "-", " ", "-").Replace(value)
	fields := strings.Fields(strings.NewReplacer("-", " ").Replace(normalized))
	for _, field := range fields {
		switch field {
		case "gpu", "ambient", "system", "motherboard", "pch", "nvme", "memory", "acpi":
			return -1
		}
	}
	switch normalized {
	case "cpu", "cpu-temp", "cpu-temperature", "cpu-thermal", "x86-pkg-temp",
		"coretemp", "k10temp", "zenpower", "tdie", "tctl":
		return 1
	}
	for _, field := range fields {
		if field == "cpu" {
			return 1
		}
	}
	if len(fields) >= 2 && fields[0] == "package" && fields[1] == "id" {
		return 1
	}
	if len(fields) > 0 && fields[0] == "core" {
		if len(fields) == 1 || (len(fields) == 2 && isDecimalDigits(fields[1])) {
			return 1
		}
	}
	return 0
}

// parseLinuxGPUTelemetry parses the pinned nvidia-smi CSV query in device-row
// order. Any malformed row rejects the complete result, and parser details are
// deliberately replaced by a fixed error.
func parseLinuxGPUTelemetry(data []byte, updatedAt time.Time) ([]domain.GPUTelemetry, error) {
	if updatedAt.IsZero() {
		return nil, errInvalidLinuxGPUCSV
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return []domain.GPUTelemetry{}, nil
	}
	reader := csv.NewReader(strings.NewReader(string(data)))
	reader.FieldsPerRecord = 7
	reader.TrimLeadingSpace = true
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, errInvalidLinuxGPUCSV
	}
	gpus := make([]domain.GPUTelemetry, 0, len(rows))
	for _, row := range rows {
		gpu, ok := parseLinuxGPURecord(row, updatedAt)
		if !ok {
			return nil, errInvalidLinuxGPUCSV
		}
		gpus = append(gpus, gpu)
	}
	return gpus, nil
}

func parseLinuxGPURecord(row []string, updatedAt time.Time) (domain.GPUTelemetry, bool) {
	if len(row) != 7 {
		return domain.GPUTelemetry{}, false
	}
	name, ok := parseLinuxGPUName(row[0], updatedAt)
	if !ok {
		return domain.GPUTelemetry{}, false
	}
	utilization, ok := parseLinuxGPUFloat(row[1], domain.MetricUnitPercent, 0, 100, updatedAt)
	if !ok {
		return domain.GPUTelemetry{}, false
	}
	temperature, ok := parseLinuxGPUFloat(row[2], domain.MetricUnitCelsius, -100, 200, updatedAt)
	if !ok {
		return domain.GPUTelemetry{}, false
	}
	used, ok := parseLinuxGPUMemory(row[3], updatedAt)
	if !ok {
		return domain.GPUTelemetry{}, false
	}
	free, ok := parseLinuxGPUMemory(row[4], updatedAt)
	if !ok {
		return domain.GPUTelemetry{}, false
	}
	total, ok := parseLinuxGPUMemory(row[5], updatedAt)
	if !ok || !validLinuxGPUMemoryTotals(used, free, total) {
		return domain.GPUTelemetry{}, false
	}
	power, ok := parseLinuxGPUFloat(row[6], domain.MetricUnitWatts, 0, math.MaxFloat64, updatedAt)
	if !ok {
		return domain.GPUTelemetry{}, false
	}
	return domain.GPUTelemetry{
		Name:        name,
		Utilization: utilization,
		Temperature: temperature,
		VRAMUsed:    used,
		VRAMFree:    free,
		VRAMTotal:   total,
		PowerWatts:  power,
	}, true
}

func parseLinuxGPUName(raw string, updatedAt time.Time) (domain.Metric[string], bool) {
	value := strings.TrimSpace(raw)
	if isLinuxNVIDIAUnavailable(value) {
		return domain.NewUnavailableMetricWithError[string](domain.MetricUnitName, updatedAt, domain.MetricErrorTemporarilyUnavailable), true
	}
	if value == "" {
		return domain.Metric[string]{}, false
	}
	return domain.NewExactMetric(value, domain.MetricUnitName, updatedAt), true
}

func parseLinuxGPUFloat(raw, unit string, min, max float64, updatedAt time.Time) (domain.Metric[float64], bool) {
	value := strings.TrimSpace(raw)
	if isLinuxNVIDIAUnavailable(value) {
		return domain.NewUnavailableMetricWithError[float64](unit, updatedAt, domain.MetricErrorTemporarilyUnavailable), true
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < min || number > max {
		return domain.Metric[float64]{}, false
	}
	return domain.NewExactMetric(number, unit, updatedAt), true
}

func parseLinuxGPUMemory(raw string, updatedAt time.Time) (domain.Metric[int64], bool) {
	value := strings.TrimSpace(raw)
	if isLinuxNVIDIAUnavailable(value) {
		return domain.NewUnavailableMetricWithError[int64](domain.MetricUnitBytes, updatedAt, domain.MetricErrorTemporarilyUnavailable), true
	}
	if !isDecimalDigits(value) {
		return domain.Metric[int64]{}, false
	}
	mib, err := strconv.ParseUint(value, 10, 64)
	if err != nil || mib > uint64(math.MaxInt64)/(1<<20) {
		return domain.Metric[int64]{}, false
	}
	bytes := int64(mib * (1 << 20))
	return domain.NewExactMetric(bytes, domain.MetricUnitBytes, updatedAt), true
}

func validLinuxGPUMemoryTotals(used, free, total domain.Metric[int64]) bool {
	if total.Value != nil && *total.Value <= 0 {
		return false
	}
	if used.Value != nil && total.Value != nil && *used.Value > *total.Value {
		return false
	}
	if free.Value != nil && total.Value != nil && *free.Value > *total.Value {
		return false
	}
	if used.Value != nil && free.Value != nil && total.Value != nil && *used.Value > *total.Value-*free.Value {
		return false
	}
	return true
}

func isLinuxNVIDIAUnavailable(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "N/A")
}

func isDecimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func addUint64(left, right uint64) (uint64, bool) {
	if right > math.MaxUint64-left {
		return 0, false
	}
	return left + right, true
}
