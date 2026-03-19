/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use
this file except in compliance with the License. You may obtain a copy of the
License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed
under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
CONDITIONS OF ANY KIND, either express or implied. See the License for the
specific language governing permissions and limitations under the License.
*/

package xfn

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// PrometheusPayloadMetrics expose RunFunction payload size metrics.
//
// NOTE(phisco): Keep payload sizing in a dedicated interceptor instead of
// extending PrometheusMetrics above. This keeps the fork-local instrumentation
// isolated and reduces churn when syncing with upstream.
type PrometheusPayloadMetrics struct {
	requestSize  *prometheus.HistogramVec
	requestBytes *prometheus.CounterVec

	responseSize  *prometheus.HistogramVec
	responseBytes *prometheus.CounterVec
}

// NewPrometheusPayloadMetrics creates metrics for RunFunction payload sizes.
func NewPrometheusPayloadMetrics() *PrometheusPayloadMetrics {
	labels := []string{"function_name", "function_package", "grpc_target", "grpc_method", "grpc_code"}

	return &PrometheusPayloadMetrics{
		requestSize: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Subsystem: "function",
			Name:      "run_function_request_size_bytes",
			Help:      "Histogram of RunFunctionRequest payload sizes in bytes.",
			Buckets:   payloadSizeBuckets(),
		}, labels),

		requestBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Subsystem: "function",
			Name:      "run_function_request_bytes_total",
			Help:      "Total RunFunctionRequest payload bytes sent.",
		}, labels),

		responseSize: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Subsystem: "function",
			Name:      "run_function_response_size_bytes",
			Help:      "Histogram of RunFunctionResponse payload sizes in bytes.",
			Buckets:   payloadSizeBuckets(),
		}, labels),

		responseBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Subsystem: "function",
			Name:      "run_function_response_bytes_total",
			Help:      "Total RunFunctionResponse payload bytes received.",
		}, labels),
	}
}

// Describe sends the super-set of all possible descriptors of metrics
// collected by this Collector to the provided channel and returns once
// the last descriptor has been sent.
func (m *PrometheusPayloadMetrics) Describe(ch chan<- *prometheus.Desc) {
	m.requestSize.Describe(ch)
	m.requestBytes.Describe(ch)
	m.responseSize.Describe(ch)
	m.responseBytes.Describe(ch)
}

// Collect is called by the Prometheus registry when collecting
// metrics. The implementation sends each collected metric via the
// provided channel and returns once the last metric has been sent.
func (m *PrometheusPayloadMetrics) Collect(ch chan<- prometheus.Metric) {
	m.requestSize.Collect(ch)
	m.requestBytes.Collect(ch)
	m.responseSize.Collect(ch)
	m.responseBytes.Collect(ch)
}

// CreateInterceptor returns a gRPC UnaryClientInterceptor for the named
// function. The supplied package (pkg) should be the package's OCI reference.
func (m *PrometheusPayloadMetrics) CreateInterceptor(name, pkg string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		err := invoker(ctx, method, req, reply, cc, opts...)

		s, _ := status.FromError(err)
		l := prometheus.Labels{
			"function_name":    name,
			"function_package": pkg,
			"grpc_target":      cc.Target(),
			"grpc_method":      method,
			"grpc_code":        s.Code().String(),
		}

		if msg, ok := req.(proto.Message); ok {
			size := float64(proto.Size(msg))
			m.requestSize.With(l).Observe(size)
			m.requestBytes.With(l).Add(size)
		}

		if err == nil {
			if msg, ok := reply.(proto.Message); ok {
				size := float64(proto.Size(msg))
				m.responseSize.With(l).Observe(size)
				m.responseBytes.With(l).Add(size)
			}
		}

		return err
	}
}

func payloadSizeBuckets() []float64 {
	// 1 KiB, 4 KiB, 16 KiB, 64 KiB, 256 KiB, 1 MiB, 4 MiB.
	return prometheus.ExponentialBuckets(1024, 4, 7)
}
