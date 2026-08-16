package local

import (
	"context"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/persist/telemetry"
)

// NewTelemetry wraps the platform host collector as host.Telemetry.
func NewTelemetry() host.Telemetry {
	return telemetryAdapter{host: telemetry.NewHost()}
}

type telemetryAdapter struct {
	host *telemetry.Host
}

func (a telemetryAdapter) Sample(ctx context.Context, root string) (host.TelemetrySample, error) {
	s, err := a.host.Collect(ctx, root)
	if err != nil {
		return host.TelemetrySample{}, err
	}
	return host.TelemetrySample{
		CPUHostPct:     s.CPUHostPct,
		CPUHostOK:      s.CPUHostOK,
		CPUProcPct:     s.CPUProcPct,
		CPUProcOK:      s.CPUProcOK,
		MemUsedBytes:   s.MemUsedBytes,
		MemTotalBytes:  s.MemTotalBytes,
		MemOK:          s.MemOK,
		MemCachedBytes: s.MemCachedBytes,
		MemCachedOK:    s.MemCachedOK,
		SwapUsedBytes:  s.SwapUsedBytes,
		SwapTotalBytes: s.SwapTotalBytes,
		SwapOK:         s.SwapOK,
		DiskUsedBytes:  s.DiskUsedBytes,
		DiskTotalBytes: s.DiskTotalBytes,
		DiskFreeBytes:  s.DiskFreeBytes,
		DiskOK:         s.DiskOK,
		DiskRoot:       s.DiskRoot,
		At:             s.At,
	}, nil
}
