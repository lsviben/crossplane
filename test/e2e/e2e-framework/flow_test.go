package e2e_framework

import (
	"context"
	_ "embed"
	"os"
	"testing"
	"time"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/resource/unstructured/composed"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/yaml"

	extv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	crossplanev1 "github.com/crossplane/crossplane/apis/pkg/v1"
	xapiv1 "github.com/crossplane/crossplane/apis/pkg/v1"
)

var (
	testenv env.Environment

	//go:embed testData/providerconfig.yaml
	providerConfigYAML []byte

	//go:embed testData/claim.yaml
	claimYAML []byte
)

func TestMain(m *testing.M) {
	testenv = env.NewInClusterConfig()

	testenv.Setup(
		setupSchema,
		setupDummyProvider,
	)

	testenv.Finish(
		teardownDummyProvider,
	)

	// launch package tests
	os.Exit(testenv.Run(m))
}

func TestFlow(t *testing.T) {

	f := features.New("create claim flow").
		WithLabel("type", "flow-claim").
		WithSetup("install the XRD", setupXRD).
		WithTeardown("teardown the XRD", teardownXRD).
		WithSetup("install the Composition", setupComposition).
		WithTeardown("teardown the Composition", teardownComposition).
		WithSetup("xrd is established and offered", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			xrd := genXRD()
			err := wait.For(func() (done bool, err error) {

				if err := cfg.Client().Resources().Get(ctx, xrd.Name, "", xrd); err != nil {
					return false, err
				}

				if xrd.Status.GetCondition(extv1.TypeEstablished).Status != corev1.ConditionTrue {
					t.Logf("XRD %q is not yet Established", xrd.GetName())
					return false, nil
				}

				if xrd.Status.GetCondition(extv1.TypeOffered).Status != corev1.ConditionTrue {
					t.Logf("XRD %q is not yet Offered", xrd.GetName())
					return false, nil
				}

				return true, nil
			}, wait.WithTimeout(time.Minute*1))
			if err != nil {
				t.Fatalf("failed to wait for xrd to be healthy: %v", err)
			}
			return ctx
		}).
		Setup(setupClaim).
		Teardown(teardownClaim).
		Assess("claim", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			claim, err := getClaim()
			if err != nil {
				t.Fatalf("failed to get claim: %v", err)
			}
			err = wait.For(func() (done bool, err error) {

				if err := cfg.Client().Resources().Get(ctx, claim.GetName(), "default", claim); err != nil {
					return false, err
				}

				claimObj := composed.Unstructured{Unstructured: *claim}
				isReady := claimObj.GetCondition(xpv1.TypeReady)
				if isReady.Status != corev1.ConditionTrue {
					t.Logf("claim %q is not yet Ready", claim.GetName())
					return false, nil
				}

				return true, nil
			}, wait.WithTimeout(time.Minute*1))
			if err != nil {
				t.Fatalf("failed to wait for claim to be Ready: %v", err)
			}
			return ctx
		}).Feature()

	// test feature
	testenv.Test(t, f)
}

func setupSchema(ctx context.Context, config *envconf.Config) (context.Context, error) {
	r, err := resources.New(config.Client().RESTConfig())
	if err != nil {
		return nil, err
	}
	if err := xapiv1.AddToScheme(r.GetScheme()); err != nil {
		return nil, err
	}
	if err := extv1.AddToScheme(r.GetScheme()); err != nil {
		return nil, err
	}

	return ctx, nil
}

func setupDummyProvider(ctx context.Context, config *envconf.Config) (context.Context, error) {
	// create a dummy provider
	provider := genDummyProvider()
	if err := config.Client().Resources().Create(ctx, provider); err != nil {
		return nil, err
	}

	// create a dummy provider server and service
	providerServer := genDummyProviderDeployment()
	if err := config.Client().Resources().Create(ctx, providerServer); err != nil {
		return nil, err
	}
	providerService := genDummyProviderService()
	if err := config.Client().Resources().Create(ctx, providerService); err != nil {
		return nil, err
	}

	// wait for provider to be ready
	err := wait.For(func() (done bool, err error) {

		if err := config.Client().Resources().Get(ctx, provider.Name, "", provider); err != nil {
			return false, err
		}
		for _, c := range provider.Status.Conditions {
			if c.Type == xapiv1.TypeHealthy && c.Reason == xapiv1.ReasonHealthy && c.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	}, wait.WithTimeout(time.Minute*1))
	if err != nil {
		return nil, err
	}

	// create provider config
	providerConfig, err := getDummyProviderConfig()
	if err != nil {
		return nil, err
	}
	if err = config.Client().Resources().Create(ctx, providerConfig); err != nil {
		return nil, err
	}

	return ctx, nil
}

func teardownDummyProvider(ctx context.Context, config *envconf.Config) (context.Context, error) {
	// delete provider config
	providerConfig, err := getDummyProviderConfig()
	if err != nil {
		return nil, err
	}
	if err = config.Client().Resources().Delete(ctx, providerConfig); err != nil {
		return nil, err
	}

	// delete provider server and service
	providerServer := genDummyProviderDeployment()
	if err := config.Client().Resources().Delete(ctx, providerServer); err != nil {
		return nil, err
	}
	providerService := genDummyProviderService()
	if err := config.Client().Resources().Delete(ctx, providerService); err != nil {
		return nil, err
	}

	// delete provider
	provider := genDummyProvider()
	if err := config.Client().Resources().Delete(ctx, provider); err != nil {
		return nil, err
	}

	return ctx, nil
}

func setupXRD(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
	xrd := genXRD()
	if err := config.Client().Resources().Create(ctx, xrd); err != nil {
		t.Fatalf("error creating xrd: %v", err)
	}
	return ctx
}

func teardownXRD(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
	xrd := genXRD()
	if err := config.Client().Resources().Delete(ctx, xrd); err != nil {
		t.Fatalf("error deleting xrd: %v", err)
	}
	return ctx
}

func setupComposition(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
	c := genComposition()
	if err := config.Client().Resources().Create(ctx, c); err != nil {
		t.Fatalf("error creating composition: %v", err)
	}
	return ctx
}

func teardownComposition(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
	c := genComposition()
	if err := config.Client().Resources().Delete(ctx, c); err != nil {
		t.Fatalf("error deleting composition: %v", err)
	}
	return ctx
}

func setupClaim(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
	c, err := getClaim()
	if err != nil {
		t.Fatalf("error generating claim: %v", err)
	}
	if err = config.Client().Resources().Create(ctx, c); err != nil {
		t.Fatalf("error creating claim: %v", err)
	}
	return ctx
}

func teardownClaim(ctx context.Context, t *testing.T, config *envconf.Config) context.Context {
	c, err := getClaim()
	if err != nil {
		t.Fatalf("error generating claim: %v", err)
	}
	if err = config.Client().Resources().Delete(ctx, c); err != nil {
		t.Fatalf("error creating claim: %v", err)
	}
	return ctx
}

func genDummyProvider() *crossplanev1.Provider {
	return &crossplanev1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: "provider-dummy"},
		Spec: crossplanev1.ProviderSpec{
			PackageSpec: crossplanev1.PackageSpec{
				Package: "xpkg.upbound.io/upbound/provider-dummy:v0.3.0",
			},
		},
	}
}

func getDummyProviderConfig() (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	err := yaml.Unmarshal(providerConfigYAML, obj)
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func genDummyProviderService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server-dummy",
			Namespace: "crossplane-system",
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": "server-dummy",
			},
			Ports: []corev1.ServicePort{
				{
					Name: "http",
					Port: 80,
					TargetPort: intstr.IntOrString{
						IntVal: 9090,
					},
				},
			},
		},
	}
}

func genDummyProviderDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server-dummy",
			Namespace: "crossplane-system",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointer.Int32(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "server-dummy",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "server-dummy",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "server",
							Image:           "ghcr.io/upbound/provider-dummy-server:main",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: 9090,
								},
							},
						},
					},
				},
			},
		},
	}
}

func genXRD() *extv1.CompositeResourceDefinition {
	return &extv1.CompositeResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "xrobots.dummy.crossplane.io",
			Labels: map[string]string{
				"provider": "dummy-provider",
			},
		},
		Spec: extv1.CompositeResourceDefinitionSpec{
			DefaultCompositionRef: &extv1.CompositionReference{
				Name: "robots-test",
			},
			Group: "dummy.crossplane.io",
			Names: kextv1.CustomResourceDefinitionNames{
				Kind:   "XRobot",
				Plural: "xrobots",
			},
			ClaimNames: &kextv1.CustomResourceDefinitionNames{
				Kind:   "Robot",
				Plural: "robots",
			},
			Versions: []extv1.CompositeResourceDefinitionVersion{{
				Name:          "v1alpha1",
				Served:        true,
				Referenceable: true,
				Schema: &extv1.CompositeResourceValidation{
					OpenAPIV3Schema: runtime.RawExtension{Raw: []byte(`{
									"type": "object",
									"properties": {
										"spec": {
											"type": "object",
											"properties": {
												"color": {
													"type": "string"
												}
											},
											"required": ["color"]
										}
									}
								}`)},
				},
			}},
		},
	}
}

func genComposition() *extv1.Composition {
	return &extv1.Composition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "robots-test",
			Labels: map[string]string{
				"provider":          "dummy-provider",
				"crossplane.io/xrd": "xrobots.dummy.crossplane.io",
			},
		},
		Spec: extv1.CompositionSpec{
			CompositeTypeRef: extv1.TypeReference{
				APIVersion: "dummy.crossplane.io/v1alpha1",
				Kind:       "XRobot",
			},
			WriteConnectionSecretsToNamespace: pointer.String("default"),
			Resources: []extv1.ComposedTemplate{
				{
					Name: pointer.String("robot"),
					Base: runtime.RawExtension{Raw: []byte(`{
						"apiVersion": "iam.dummy.upbound.io/v1alpha1",
						"kind": "Robot",
						"spec": {
							"forProvider": {}
						}}`)},
					Patches: []extv1.Patch{
						{
							Type:          extv1.PatchTypeFromCompositeFieldPath,
							FromFieldPath: pointer.String("spec.color"),
							ToFieldPath:   pointer.String("spec.forProvider.color"),
						},
					},
				},
			},
		},
	}
}

func getClaim() (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	err := yaml.Unmarshal(claimYAML, obj)
	if err != nil {
		return nil, err
	}
	return obj, nil
}
