// Package otelpg gives every pgx statement an OpenTelemetry client span, as a
// child of the request that asked, using the OpenTelemetry API only: it
// imports no driver and no instrumentation library.
//
// The statement is recorded as its text with the placeholders in it and never
// with the arguments, which are user data; a trace store is not where they
// belong. Plug it into pgclient.Config.Tracer.
package otelpg

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Tracer implements pgx.QueryTracer.
type Tracer struct{ tracer trace.Tracer }

var _ pgx.QueryTracer = (*Tracer)(nil)

// New returns a Tracer on provider; nil means the global provider.
func New(provider trace.TracerProvider) *Tracer {
	if provider == nil {
		provider = otel.GetTracerProvider()
	}
	return &Tracer{tracer: provider.Tracer("github.com/truvity/cnpg/clients/go/pgclient")}
}

type spanKey struct{}

// TraceQueryStart starts the span.
func (t *Tracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	op := operation(data.SQL)
	cfg := conn.Config()
	attrs := []attribute.KeyValue{
		attribute.String("db.system.name", "postgresql"),
		attribute.String("db.namespace", cfg.Database),
		attribute.String("db.operation.name", op),
		attribute.String("db.query.text", data.SQL),
		attribute.String("server.address", cfg.Host),
		attribute.Int("server.port", int(cfg.Port)),
	}
	ctx, span := t.tracer.Start(ctx, op+" "+cfg.Database, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	return context.WithValue(ctx, spanKey{}, span)
}

// TraceQueryEnd ends it.
func (t *Tracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span, ok := ctx.Value(spanKey{}).(trace.Span)
	if !ok {
		return
	}
	defer span.End()
	span.SetAttributes(attribute.Int64("db.response.returned_rows", data.CommandTag.RowsAffected()))
	// A missing row is an answer, not a failure.
	if err := data.Err; err != nil && !errors.Is(err, pgx.ErrNoRows) {
		span.RecordError(err)
		span.SetStatus(codes.Error, "query failed")
	}
}

// operation is the statement's first keyword, a small bounded set, never the
// text itself (span names must not have unbounded cardinality).
func operation(sql string) string {
	s := strings.TrimLeft(sql, " \t\r\n(")
	end := strings.IndexAny(s, " \t\r\n(;")
	if end < 0 {
		end = len(s)
	}
	word := strings.ToUpper(s[:end])
	if word == "" || len(word) > 16 {
		return "QUERY"
	}
	for _, r := range word {
		if r < 'A' || r > 'Z' {
			return "QUERY"
		}
	}
	return word
}
