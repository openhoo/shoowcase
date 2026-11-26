package telemetry

import (
	"context"
	"database/sql"

	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/semconv/v1.43.0/dbconv"
)

// The application has one connection pool. Sample Go's in-memory counters
// once per collection: a slow or unavailable database cannot delay export.
func observePool(m metric.Meter, stats func() sql.DBStats) error {
	count, err := dbconv.NewClientConnectionCountObservable(m)
	if err != nil {
		return err
	}
	max, err := dbconv.NewClientConnectionMaxObservable(m)
	if err != nil {
		return err
	}
	waits, err := m.Int64ObservableCounter("shoowcase.db.pool.waits", metric.WithUnit("{wait}"), metric.WithDescription("Cumulative number of waits for a database connection."))
	if err != nil {
		return err
	}
	waitTime, err := m.Float64ObservableCounter("shoowcase.db.pool.wait.duration", metric.WithUnit("s"), metric.WithDescription("Cumulative time spent waiting for database connections."))
	if err != nil {
		return err
	}
	pool := semconv.DBClientConnectionPoolName("store")
	poolAttrs := metric.WithAttributes(pool)
	idle := metric.WithAttributes(pool, semconv.DBClientConnectionStateIdle)
	used := metric.WithAttributes(pool, semconv.DBClientConnectionStateUsed)
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := stats()
		o.ObserveInt64(count.Inst(), int64(s.Idle), idle)
		o.ObserveInt64(count.Inst(), int64(s.InUse), used)
		o.ObserveInt64(max.Inst(), int64(s.MaxOpenConnections), poolAttrs)
		o.ObserveInt64(waits, s.WaitCount, poolAttrs)
		o.ObserveFloat64(waitTime, s.WaitDuration.Seconds(), poolAttrs)
		return nil
	}, count.Inst(), max.Inst(), waits, waitTime)
	return err
}
