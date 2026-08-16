package daemon

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const meterName = "github.com/jonbaldie/gastown/daemon"

// daemonMetrics holds OTel instruments for the daemon.
// All methods are nil-safe so callers don't need to guard against disabled telemetry.
type daemonMetrics struct {
	// heartbeatTotal counts daemon heartbeat cycles.
	heartbeatTotal metric.Int64Counter

	// restartTotal counts agent session restarts, labeled by agent type.
	restartTotal metric.Int64Counter

	// polecatSpawns counts polecat session spawns, labeled by rig name.
	polecatSpawns metric.Int64Counter

	// doltMu protects dolt gauge values written by the health check goroutine.
	doltMu             sync.RWMutex
	doltConnections    int64
	doltMaxConnections int64
	doltLatencyMs      float64
	doltDiskBytes      int64
	doltHealthy        int64 // 1 = healthy, 0 = unhealthy
}

// newDaemonMetrics registers all daemon OTel instruments against the global
// MeterProvider. Must be called after telemetry.Init so the provider is set.
// Returns a no-op struct if no provider is configured.
func newDaemonMetrics() (*daemonMetrics, error) {
	m := otel.GetMeterProvider().Meter(meterName)
	dm := &daemonMetrics{}

	var err error

	dm.heartbeatTotal, err = m.Int64Counter("gastown.daemon.heartbeat.total",
		metric.WithDescription("Total number of daemon heartbeat cycles"),
	)
	if err != nil {
		return nil, err
	}

	dm.restartTotal, err = m.Int64Counter("gastown.daemon.restart.total",
		metric.WithDescription("Total number of agent session restarts"),
	)
	if err != nil {
		return nil, err
	}

	dm.polecatSpawns, err = m.Int64Counter("gastown.polecat.spawns.total",
		metric.WithDescription("Total number of polecat session spawns"),
	)
	if err != nil {
		return nil, err
	}

	// Dolt observable gauges — values are updated by health checks and
	// collected by the SDK on each export interval.
	connGauge, err := m.Int64ObservableGauge("gastown.dolt.connections",
		metric.WithDescription("Active Dolt server connections"),
	)
	if err != nil {
		return nil, err
	}

	maxConnGauge, err := m.Int64ObservableGauge("gastown.dolt.max_connections",
		metric.WithDescription("Configured maximum Dolt server connections"),
	)
	if err != nil {
		return nil, err
	}

	latencyGauge, err := m.Float64ObservableGauge("gastown.dolt.query_latency_ms",
		metric.WithDescription("Dolt health probe round-trip latency in milliseconds"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}

	diskGauge, err := m.Int64ObservableGauge("gastown.dolt.disk_usage_bytes",
		metric.WithDescription("Dolt data directory disk usage"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, err
	}

	healthyGauge, err := m.Int64ObservableGauge("gastown.dolt.healthy",
		metric.WithDescription("Dolt server health (1=healthy, 0=unhealthy)"),
	)
	if err != nil {
		return nil, err
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		dm.doltMu.RLock()
		defer dm.doltMu.RUnlock()
		o.ObserveInt64(connGauge, dm.doltConnections)
		o.ObserveInt64(maxConnGauge, dm.doltMaxConnections)
		o.ObserveFloat64(latencyGauge, dm.doltLatencyMs)
		o.ObserveInt64(diskGauge, dm.doltDiskBytes)
		o.ObserveInt64(healthyGauge, dm.doltHealthy)
		return nil
	}, connGauge, maxConnGauge, latencyGauge, diskGauge, healthyGauge)
	if err != nil {
		return nil, err
	}

	return dm, nil
}

// recordHeartbeat increments the heartbeat counter.
func (dm *daemonMetrics) recordHeartbeat(ctx context.Context) {
	if dm == nil {
		return
	}
	dm.heartbeatTotal.Add(ctx, 1)
}

// recordRestart increments the restart counter, labeled with the agent type
// (e.g. "deacon", "witness", "refinery", "polecat").
func (dm *daemonMetrics) recordRestart(ctx context.Context, agentType string) {
	if dm == nil {
		return
	}
	dm.restartTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("agent.type", agentType)),
	)
}

// recordPolecatSpawn increments the polecat spawn counter, labeled with the rig name.
func (dm *daemonMetrics) recordPolecatSpawn(ctx context.Context, rigName string) {
	if dm == nil {
		return
	}
	dm.polecatSpawns.Add(ctx, 1,
		metric.WithAttributes(attribute.String("rig", rigName)),
	)
}

// updateDoltHealth stores the latest Dolt health snapshot for observable gauges.
func (dm *daemonMetrics) updateDoltHealth(conns, maxConns int64, latencyMs float64, diskBytes int64, healthy bool) {
	if dm == nil {
		return
	}
	var healthyInt int64
	if healthy {
		healthyInt = 1
	}
	dm.doltMu.Lock()
	defer dm.doltMu.Unlock()
	dm.doltConnections = conns
	dm.doltMaxConnections = maxConns
	dm.doltLatencyMs = latencyMs
	dm.doltDiskBytes = diskBytes
	dm.doltHealthy = healthyInt
}
