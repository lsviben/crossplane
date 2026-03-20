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
	"net"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	pkgv1 "github.com/crossplane/crossplane/v2/apis/pkg/v1"
	fnv1 "github.com/crossplane/crossplane/v2/proto/fn/v1"
	fnv1beta1 "github.com/crossplane/crossplane/v2/proto/fn/v1beta1"
)

func TestPrometheusPayloadMetricsCreateInterceptor(t *testing.T) {
	errBoom := errors.New("boom")

	type args struct {
		method string
		req    any
		reply  any
		invoke grpc.UnaryInvoker
	}

	type want struct {
		requestBytes  float64
		responseBytes float64
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"SuccessfulRequest": {
			reason: "A successful RunFunction call should record request and response bytes with the final gRPC status code.",
			args: args{
				method: fnv1.FunctionRunnerService_RunFunction_FullMethodName,
				req: &fnv1.RunFunctionRequest{
					Meta: &fnv1.RequestMeta{Tag: "request"},
				},
				reply: &fnv1.RunFunctionResponse{},
				invoke: func(_ context.Context, _ string, _ any, reply any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
					out := reply.(*fnv1.RunFunctionResponse)
					*out = fnv1.RunFunctionResponse{
						Meta: &fnv1.ResponseMeta{Tag: "response"},
					}
					return nil
				},
			},
			want: want{
				requestBytes:  float64(proto.Size(&fnv1.RunFunctionRequest{Meta: &fnv1.RequestMeta{Tag: "request"}})),
				responseBytes: float64(proto.Size(&fnv1.RunFunctionResponse{Meta: &fnv1.ResponseMeta{Tag: "response"}})),
			},
		},
		"TerminalRPCError": {
			reason: "A failed RPC should still record request bytes but should not record response bytes.",
			args: args{
				method: fnv1.FunctionRunnerService_RunFunction_FullMethodName,
				req: &fnv1.RunFunctionRequest{
					Meta: &fnv1.RequestMeta{Tag: "request"},
				},
				reply: &fnv1.RunFunctionResponse{},
				invoke: func(_ context.Context, _ string, _ any, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
					return status.Error(codes.Unavailable, errBoom.Error())
				},
			},
			want: want{
				requestBytes:  float64(proto.Size(&fnv1.RunFunctionRequest{Meta: &fnv1.RequestMeta{Tag: "request"}})),
				responseBytes: 0,
			},
		},
		"SkipNonProtoPayloads": {
			reason: "Unexpected interceptor payload types should be ignored instead of causing metrics emission or panics.",
			args: args{
				method: fnv1.FunctionRunnerService_RunFunction_FullMethodName,
				req:    "not-a-proto",
				reply:  struct{}{},
				invoke: func(_ context.Context, _ string, _ any, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
					return nil
				},
			},
			want: want{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			registry := prometheus.NewRegistry()
			metrics := NewPrometheusPayloadMetrics()
			registry.MustRegister(metrics)

			conn, err := grpc.NewClient("dns:///localhost:9443", grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatalf("grpc.NewClient(...): %v", err)
			}
			defer func() {
				if err := conn.Close(); err != nil {
					t.Logf("conn.Close(): %v", err)
				}
			}()

			interceptor := metrics.CreateInterceptor("test-function", "xpkg.crossplane.io/test-function:v1.0.0")
			if err := interceptor(context.Background(), tc.args.method, tc.args.req, tc.args.reply, conn, tc.args.invoke); err != nil && name != "TerminalRPCError" {
				t.Fatalf("CreateInterceptor(...): %v", err)
			}

			families := gatherMetricFamilies(t, registry)

			if diff := cmp.Diff(tc.want.requestBytes, counterValue(t, families, "function_run_function_request_bytes_total", map[string]string{
				"function_package": "xpkg.crossplane.io/test-function:v1.0.0",
				"grpc_method":      tc.args.method,
			})); diff != "" {
				t.Errorf("\n%s\nrequest bytes: -want, +got:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.responseBytes, counterValue(t, families, "function_run_function_response_bytes_total", map[string]string{
				"function_package": "xpkg.crossplane.io/test-function:v1.0.0",
				"grpc_method":      tc.args.method,
			})); diff != "" {
				t.Errorf("\n%s\nresponse bytes: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestPrometheusPayloadMetricsWithPackagedFunctionRunner(t *testing.T) {
	listeners := make([]net.Listener, 0)
	t.Cleanup(func() {
		for _, lis := range listeners {
			if err := lis.Close(); err != nil {
				t.Logf("lis.Close(): %v", err)
			}
		}
	})

	type params struct {
		client client.Client
	}

	type args struct {
		name string
		req  *fnv1.RunFunctionRequest
	}

	type want struct {
		requestsByMethod  map[string]float64
		responsesByMethod map[string]float64
	}

	cases := map[string]struct {
		reason string
		params params
		args   args
		want   want
	}{
		"SuccessfulV1Call": {
			reason: "A successful v1 function call should emit request and response bytes for the v1 RPC method only.",
			params: params{
				client: &test.MockClient{
					MockList: test.NewMockListFn(nil, func(obj client.ObjectList) error {
						lis := NewGRPCServer(t, &MockFunctionServer{rsp: &fnv1.RunFunctionResponse{
							Meta: &fnv1.ResponseMeta{Tag: "response"},
						}})
						listeners = append(listeners, lis)

						l, ok := obj.(*pkgv1.FunctionRevisionList)
						if !ok {
							return nil
						}

						l.Items = []pkgv1.FunctionRevision{{
							ObjectMeta: metav1.ObjectMeta{Name: "cool-fn-revision-a"},
							Spec: pkgv1.FunctionRevisionSpec{
								PackageRevisionSpec: pkgv1.PackageRevisionSpec{
									DesiredState: pkgv1.PackageRevisionActive,
									Package:      "xpkg.crossplane.io/test-function:v1.0.0",
								},
							},
							Status: pkgv1.FunctionRevisionStatus{
								Endpoint: strings.Replace(lis.Addr().String(), "127.0.0.1", "dns:///localhost", 1),
							},
						}}

						return nil
					}),
				},
			},
			args: args{
				name: "cool-fn",
				req:  &fnv1.RunFunctionRequest{Meta: &fnv1.RequestMeta{Tag: "request"}},
			},
			want: want{
				requestsByMethod: map[string]float64{
					fnv1.FunctionRunnerService_RunFunction_FullMethodName: float64(proto.Size(&fnv1.RunFunctionRequest{Meta: &fnv1.RequestMeta{Tag: "request"}})),
				},
				responsesByMethod: map[string]float64{
					fnv1.FunctionRunnerService_RunFunction_FullMethodName: float64(proto.Size(&fnv1.RunFunctionResponse{Meta: &fnv1.ResponseMeta{Tag: "response"}})),
				},
			},
		},
		"FallbackToV1Beta1": {
			reason: "A v1beta1 fallback should emit request bytes for both attempts but response bytes only for the successful beta RPC.",
			params: params{
				client: &test.MockClient{
					MockList: test.NewMockListFn(nil, func(obj client.ObjectList) error {
						lis := NewBetaGRPCServer(t, &MockBetaFunctionServer{rsp: &fnv1beta1.RunFunctionResponse{
							Meta: &fnv1beta1.ResponseMeta{Tag: "response"},
						}})
						listeners = append(listeners, lis)

						l, ok := obj.(*pkgv1.FunctionRevisionList)
						if !ok {
							return nil
						}

						l.Items = []pkgv1.FunctionRevision{{
							ObjectMeta: metav1.ObjectMeta{Name: "cool-fn-revision-a"},
							Spec: pkgv1.FunctionRevisionSpec{
								PackageRevisionSpec: pkgv1.PackageRevisionSpec{
									DesiredState: pkgv1.PackageRevisionActive,
									Package:      "xpkg.crossplane.io/test-function:v1.0.0",
								},
							},
							Status: pkgv1.FunctionRevisionStatus{
								Endpoint: strings.Replace(lis.Addr().String(), "127.0.0.1", "dns:///localhost", 1),
							},
						}}

						return nil
					}),
				},
			},
			args: args{
				name: "cool-fn",
				req:  &fnv1.RunFunctionRequest{Meta: &fnv1.RequestMeta{Tag: "request"}},
			},
			want: want{
				requestsByMethod: map[string]float64{
					fnv1.FunctionRunnerService_RunFunction_FullMethodName:      float64(proto.Size(&fnv1.RunFunctionRequest{Meta: &fnv1.RequestMeta{Tag: "request"}})),
					fnv1beta1.FunctionRunnerService_RunFunction_FullMethodName: float64(proto.Size(&fnv1.RunFunctionRequest{Meta: &fnv1.RequestMeta{Tag: "request"}})),
				},
				responsesByMethod: map[string]float64{
					fnv1beta1.FunctionRunnerService_RunFunction_FullMethodName: float64(proto.Size(&fnv1.RunFunctionResponse{Meta: &fnv1.ResponseMeta{Tag: "response"}})),
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			registry := prometheus.NewRegistry()
			metrics := NewPrometheusPayloadMetrics()
			registry.MustRegister(metrics)

			runner := NewPackagedFunctionRunner(tc.params.client, WithInterceptorCreators(metrics))

			if _, err := runner.RunFunction(context.Background(), tc.args.name, tc.args.req); err != nil {
				t.Fatalf("RunFunction(...): %v", err)
			}

			families := gatherMetricFamilies(t, registry)

			if diff := cmp.Diff(tc.want.requestsByMethod, counterValuesByMethod(t, families, "function_run_function_request_bytes_total")); diff != "" {
				t.Errorf("\n%s\nrequest bytes by method: -want, +got:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.responsesByMethod, counterValuesByMethod(t, families, "function_run_function_response_bytes_total")); diff != "" {
				t.Errorf("\n%s\nresponse bytes by method: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestNewPrometheusPayloadMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewPrometheusPayloadMetrics()
	registry.MustRegister(metrics)

	conn, err := grpc.NewClient("dns:///localhost:9443", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient(...): %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("conn.Close(): %v", err)
		}
	}()

	interceptor := metrics.CreateInterceptor("test-function", "xpkg.crossplane.io/test-function:v1.0.0")
	req := &fnv1.RunFunctionRequest{Meta: &fnv1.RequestMeta{Tag: "request"}}
	reply := &fnv1.RunFunctionResponse{}
	if err := interceptor(context.Background(), fnv1.FunctionRunnerService_RunFunction_FullMethodName, req, reply, conn, func(_ context.Context, _ string, _ any, reply any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		out := reply.(*fnv1.RunFunctionResponse)
		*out = fnv1.RunFunctionResponse{Meta: &fnv1.ResponseMeta{Tag: "response"}}
		return nil
	}); err != nil {
		t.Fatalf("CreateInterceptor(...): %v", err)
	}

	families := gatherMetricFamilies(t, registry)

	type want struct {
		metricType dto.MetricType
		labels     []string
		buckets    []float64
	}

	cases := map[string]struct {
		reason string
		want   want
	}{
		"RequestSizeHistogram": {
			reason: "Request size histogram should expose the expected label set and fixed bucket boundaries.",
			want: want{
				metricType: dto.MetricType_HISTOGRAM,
				labels:     []string{"function_package", "grpc_method"},
				buckets:    payloadSizeBuckets(),
			},
		},
		"RequestBytesCounter": {
			reason: "Request byte counters should expose the expected label set.",
			want: want{
				metricType: dto.MetricType_COUNTER,
				labels:     []string{"function_package", "grpc_method"},
			},
		},
		"ResponseSizeHistogram": {
			reason: "Response size histogram should expose the expected label set and fixed bucket boundaries.",
			want: want{
				metricType: dto.MetricType_HISTOGRAM,
				labels:     []string{"function_package", "grpc_method"},
				buckets:    payloadSizeBuckets(),
			},
		},
		"ResponseBytesCounter": {
			reason: "Response byte counters should expose the expected label set.",
			want: want{
				metricType: dto.MetricType_COUNTER,
				labels:     []string{"function_package", "grpc_method"},
			},
		},
	}

	names := map[string]string{
		"RequestSizeHistogram":  "function_run_function_request_size_bytes",
		"RequestBytesCounter":   "function_run_function_request_bytes_total",
		"ResponseSizeHistogram": "function_run_function_response_size_bytes",
		"ResponseBytesCounter":  "function_run_function_response_bytes_total",
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			family, ok := families[names[name]]
			if !ok {
				t.Fatalf("expected metric family %q", names[name])
			}

			if diff := cmp.Diff(tc.want.metricType, family.GetType()); diff != "" {
				t.Errorf("\n%s\nmetric type: -want, +got:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff(sortedStrings(tc.want.labels), labelNames(family.GetMetric()[0])); diff != "" {
				t.Errorf("\n%s\nlabel names: -want, +got:\n%s", tc.reason, diff)
			}

			if len(tc.want.buckets) > 0 {
				if diff := cmp.Diff(tc.want.buckets, histogramBuckets(t, family)); diff != "" {
					t.Errorf("\n%s\nbucket boundaries: -want, +got:\n%s", tc.reason, diff)
				}
			}
		})
	}
}

func gatherMetricFamilies(t *testing.T, registry *prometheus.Registry) map[string]*dto.MetricFamily {
	t.Helper()

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("registry.Gather(): %v", err)
	}

	out := make(map[string]*dto.MetricFamily, len(families))
	for _, family := range families {
		out[family.GetName()] = family
	}

	return out
}

func counterValuesByMethod(t *testing.T, families map[string]*dto.MetricFamily, family string) map[string]float64 {
	t.Helper()

	out := map[string]float64{}
	mf, ok := families[family]
	if !ok {
		return out
	}

	for _, metric := range mf.GetMetric() {
		labels := labelMap(metric)
		out[labels["grpc_method"]] = metric.GetCounter().GetValue()
	}

	return out
}

func counterValue(t *testing.T, families map[string]*dto.MetricFamily, family string, labels map[string]string) float64 {
	t.Helper()

	mf, ok := families[family]
	if !ok {
		return 0
	}

	for _, metric := range mf.GetMetric() {
		if cmp.Equal(labels, labelMap(metric)) {
			return metric.GetCounter().GetValue()
		}
	}

	return 0
}

func histogramBuckets(t *testing.T, family *dto.MetricFamily) []float64 {
	t.Helper()

	if len(family.GetMetric()) == 0 {
		return nil
	}

	buckets := family.GetMetric()[0].GetHistogram().GetBucket()
	out := make([]float64, len(buckets))
	for i := range buckets {
		out[i] = buckets[i].GetUpperBound()
	}

	return out
}

func labelNames(metric *dto.Metric) []string {
	out := make([]string, 0, len(metric.GetLabel()))
	for _, label := range metric.GetLabel() {
		out = append(out, label.GetName())
	}

	return sortedStrings(out)
}

func labelMap(metric *dto.Metric) map[string]string {
	out := map[string]string{}
	for _, label := range metric.GetLabel() {
		out[label.GetName()] = label.GetValue()
	}

	return out
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
