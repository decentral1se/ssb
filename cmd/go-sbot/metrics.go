// SPDX-FileCopyrightText: 2021 The Go-SSB Authors
//
// SPDX-License-Identifier: MIT

package main

import (
	"net"
	"net/http"
	"time"

	"github.com/go-kit/kit/metrics/prometheus"
	stdprometheus "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/ssbc/go-netwrap"
	"go.mindeco.de/logging/countconn"
)

var (
	SystemEvents  *prometheus.Counter
	SystemSummary *prometheus.Summary
	RepoStats     *prometheus.Gauge
)

//	muxrpcSummary *prometheus.Summary

/*
type latencyMuxH struct {
	root muxrpc.Handler
	sum  *prometheus.Summary
}

func (lm *latencyMuxH) HandleCall(ctx context.Context, req *muxrpc.Request, edp muxrpc.Endpoint) {
	start := time.Now()
	lm.root.HandleCall(ctx, req, EndpointWithLatency(lm.sum)(edp))
	lm.sum.With("method", req.Method.String(), "type", string(req.Type), "error", "undefined").Observe(time.Since(start).Seconds())

}

func (lm *latencyMuxH) HandleConnect(ctx context.Context, edp muxrpc.Endpoint) {
	start := time.Now()
	lm.root.HandleConnect(ctx, EndpointWithLatency(lm.sum)(edp))
	lm.sum.With("method", "none", "type", "connect", "error", "undefined").Observe(time.Since(start).Seconds())
}

func HandlerWithLatency(s *prometheus.Summary) muxrpc.HandlerWrapper {
	return func(root muxrpc.Handler) muxrpc.Handler {
		return &latencyMuxH{
			root: root,
			sum:  s,
		}
	}
}
*/

func startDebug() {
	if debugAddr == "" {
		return
	}

	SystemEvents = prometheus.NewCounterFrom(stdprometheus.CounterOpts{
		Namespace: "gossb",
		Subsystem: "events",
		Name:      "ssb_sysevents",
	}, []string{"event"})

	RepoStats = prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
		Namespace: "gossb",
		Subsystem: "repo",
		Name:      "ssb_repostats",
	}, []string{"part"})

	// muxrpcSummary = prometheus.NewSummaryFrom(stdprometheus.SummaryOpts{
	// 	Namespace: "gossb",
	// 	Subsystem: "muxrpc",
	// 	Name:      "muxrpc_durrations_seconds",
	// }, []string{"method", "type", "error"})

	SystemSummary = prometheus.NewSummaryFrom(stdprometheus.SummaryOpts{
		Namespace: "gossb",
		Subsystem: "sbot",
		Name:      "general_durrations",
	}, []string{"part"})

	go func() {
		http.Handle("/metrics", promhttp.Handler())
		log.Log("starting", "metrics", "addr", debugAddr)
		err := http.ListenAndServe(debugAddr, nil)
		checkAndLog(err)
	}()
}

/* TODO: refactor for luigi-less api
type latencyWrapper struct {
	start time.Time
	root  muxrpc.Endpoint
	sum   *prometheus.Summary
}

func EndpointWithLatency(sum *prometheus.Summary) func(r muxrpc.Endpoint) muxrpc.Endpoint {
	return func(r muxrpc.Endpoint) muxrpc.Endpoint {
		var lw latencyWrapper
		lw.root = r
		lw.start = time.Now()
		lw.sum = sum
		return &lw
	}
}

func (lw *latencyWrapper) Async(ctx context.Context, ret interface{}, tipe muxrpc.RequestEncoding, method muxrpc.Method, args ...interface{}) error {
	start := time.Now()
	err := lw.root.Async(ctx, ret, tipe, method, args...)
	lw.sum.With("method", method.String(), "type", "async", "error", err.Error()).Observe(time.Since(start).Seconds())
	return err
}

func (lw *latencyWrapper) Source(ctx context.Context, tipe muxrpc.RequestEncoding, method muxrpc.Method, args ...interface{}) (luigi.Source, error) {
	start := time.Now()
	rootSrc, err := lw.root.Source(ctx, tipe, method, args...)
	if err != nil {
		lw.sum.With("method", method.String(), "type", "source", "error", err.Error()).Observe(time.Since(start).Seconds())
		return nil, err
	}

	pSrc, pSink := luigi.NewPipe()
	go func() {
		var errStr = "nil"
		err := luigi.Pump(ctx, pSink, rootSrc.AsStream())
		if err != nil {
			errStr = errors.Cause(err).Error()
		}
		pSink.Close()
		lw.sum.With("method", method.String(), "type", "source", "error", errStr).Observe(time.Since(start).Seconds())
	}()

	return pSrc, nil
}

func (lw *latencyWrapper) Sink(ctx context.Context, tipe muxrpc.RequestEncoding, method muxrpc.Method, args ...interface{}) (luigi.Sink, error) {
	start := time.Now()
	rootSink, err := lw.root.Sink(ctx, tipe, method, args...)
	if err != nil {
		lw.sum.With("method", method.String(), "type", "sink", "error", err.Error()).Observe(time.Since(start).Seconds())
		return nil, err
	}

	pSrc, pSink := luigi.NewPipe()
	go func() {
		var errStr = "nil"
		err := luigi.Pump(ctx, rootSink.AsStream(), pSrc)
		if err != nil {
			errStr = errors.Cause(err).Error()
		}
		rootSink.Close()
		lw.sum.With("method", method.String(), "type", "sink", "error", errStr).Observe(time.Since(start).Seconds())
	}()

	return pSink, nil
}

func (lw *latencyWrapper) Duplex(ctx context.Context, tipe muxrpc.RequestEncoding, method muxrpc.Method, args ...interface{}) (*muxrpc.ByteSource, *muxrpc.ByteSink, error) {
	start := time.Now()
	rootSrc, rootSink, err := lw.root.Duplex(ctx, tipe, method, args...)
	if err != nil {
		lw.sum.With("method", method.String(), "type", "sink", "error", err.Error()).Observe(time.Since(start).Seconds())
		return nil, nil, err
	}

	roottoSrc, roottoSink := luigi.NewPipe()
	go func() {
		var errStr = "nil"
		err := luigi.Pump(ctx, rootSink, roottoSrc)
		if err != nil {
			errStr = errors.Cause(err).Error()
		}
		rootSink.Close()
		lw.sum.With("method", method.String(), "type", "duplex sink", "error", errStr).Observe(time.Since(start).Seconds())
	}()

	rootfromSrc, rootfromSink := luigi.NewPipe()
	go func() {
		var errStr = "nil"
		err := luigi.Pump(ctx, rootfromSink, rootSrc)
		if err != nil {
			errStr = errors.Cause(err).Error()
		}
		rootfromSink.Close()
		lw.sum.With("method", method.String(), "type", "duplex source", "error", errStr).Observe(time.Since(start).Seconds())
	}()

	return rootfromSrc, roottoSink, nil
}

// Assuming evrything goes through the above
func (lw *latencyWrapper) Do(ctx context.Context, req *muxrpc.Request) error {
	return lw.root.Do(ctx, req)
}

func (lw *latencyWrapper) Terminate() error {
	err := lw.root.Terminate()
	lw.sum.With("method", "terminate", "type", "close", "error", err.Error()).Observe(time.Since(lw.start).Seconds())
	return err
}

func (lw *latencyWrapper) Remote() net.Addr {
	return lw.root.Remote()
}

func (lw *latencyWrapper) Serve() error {
	srv, ok := lw.root.(muxrpc.Server)
	if !ok {
		return fmt.Errorf("latencywrapper: server interface not implemented")
	}
	// this looses the wrapped endpoint again maybe?
	return srv.Serve()
}
*/

type promCount struct {
	*countconn.Reader
	*countconn.Writer
	conn net.Conn
}

func promCountConn() netwrap.ConnWrapper {
	return func(c net.Conn) (net.Conn, error) {
		wrap := &promCount{
			conn: c,
		}
		wrap.Reader = countconn.NewReader(c)
		wrap.Writer = countconn.NewWriter(c)
		return wrap, nil
	}
}

func (c *promCount) Close() error {
	err := c.conn.Close()
	SystemEvents.With("event", "bytes.tx").Add(float64(c.Writer.N()))
	SystemEvents.With("event", "bytes.rx").Add(float64(c.Reader.N()))
	return err
}

func (c *promCount) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *promCount) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *promCount) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *promCount) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *promCount) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }
