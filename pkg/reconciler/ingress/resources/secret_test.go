/*
Copyright 2019 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package resources

import (
	"fmt"
	"testing"

	"knative.dev/pkg/system"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeinformers "k8s.io/client-go/informers"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	"knative.dev/net-istio/pkg/reconciler/ingress/config"
	. "knative.dev/pkg/logging/testing"
	"knative.dev/serving/pkg/apis/networking"
	"knative.dev/serving/pkg/apis/networking/v1alpha1"
)

var (
	testSecret = corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "secret0",
			Namespace: "knative-serving",
		},
		Data: map[string][]byte{
			"test": []byte("abcd"),
		},
	}

	ci = v1alpha1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ingress",
			Namespace: system.Namespace(),
		},
		Spec: v1alpha1.IngressSpec{
			TLS: []v1alpha1.IngressTLS{{
				Hosts:           []string{"example.com"},
				SecretName:      "secret0",
				SecretNamespace: "knative-serving",
			}},
		},
	}

	wildcardCert, _    = GenerateCertificate("*.example.com", "wildcard", "")
	nonWildcardCert, _ = GenerateCertificate("test.example.com", "nonWildcard", "")
)

func TestGetSecrets(t *testing.T) {
	kubeClient := fakek8s.NewSimpleClientset()
	secretClient := kubeinformers.NewSharedInformerFactory(kubeClient, 0).Core().V1().Secrets()
	createSecret := func(secret *corev1.Secret) {
		kubeClient.CoreV1().Secrets(secret.Namespace).Create(secret)
		secretClient.Informer().GetIndexer().Add(secret)
	}

	cases := []struct {
		name     string
		secret   *corev1.Secret
		ci       *v1alpha1.Ingress
		expected map[string]*corev1.Secret
		wantErr  bool
	}{{
		name:   "Get secrets successfully.",
		secret: &testSecret,
		ci:     &ci,
		expected: map[string]*corev1.Secret{
			"knative-serving/secret0": &testSecret,
		},
	}, {
		name:   "Fail to get secrets",
		secret: &corev1.Secret{},
		ci: &v1alpha1.Ingress{
			Spec: v1alpha1.IngressSpec{
				TLS: []v1alpha1.IngressTLS{{
					Hosts:           []string{"example.com"},
					SecretName:      "no-exist-secret",
					SecretNamespace: "no-exist-namespace",
				}},
			},
		},
		wantErr: true,
	}}
	for _, c := range cases {
		createSecret(c.secret)
		t.Run(c.name, func(t *testing.T) {
			secrets, err := GetSecrets(c.ci, secretClient.Lister())
			if (err != nil) != c.wantErr {
				t.Fatalf("Test: %s; GetSecrets error = %v, WantErr %v", c.name, err, c.wantErr)
			}
			if diff := cmp.Diff(c.expected, secrets); diff != "" {
				t.Errorf("Unexpected secrets (-want, +got): %v", diff)
			}
		})
	}
}

func TestMakeSecrets(t *testing.T) {
	ctx := TestContextWithLogger(t)
	ctx = config.ToContext(ctx, &config.Config{
		Istio: &config.Istio{
			IngressGateways: []config.Gateway{{
				Name: "test-gateway",
				// The namespace of Istio gateway service is istio-system.
				ServiceURL: "istio-ingressgateway.istio-system.svc.cluster.local",
			}},
		},
	})

	cases := []struct {
		name         string
		originSecret *corev1.Secret
		expected     []*corev1.Secret
		wantErr      bool
	}{{
		name: "target secret namespace (istio-system) is the same as the origin secret namespace (istio-system).",
		originSecret: &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "istio-system",
				UID:       "1234",
			},
			Data: map[string][]byte{
				"test-data": []byte("abcd"),
			}},
		expected: []*corev1.Secret{},
	}, {
		name: "target secret namespace (istio-system) is different from the origin secret namespace (knative-serving).",
		originSecret: &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "knative-serving",
				UID:       "1234",
			},
			Data: map[string][]byte{
				"test-data": []byte("abcd"),
			}},
		expected: []*corev1.Secret{{
			ObjectMeta: metav1.ObjectMeta{
				// Name is generated by TargetSecret function.
				Name: "ingress-1234",
				// Expected secret should be in istio-system which is
				// the ns of Istio gateway service.
				Namespace: "istio-system",
				Labels: map[string]string{
					networking.OriginSecretNameLabelKey:      "test-secret",
					networking.OriginSecretNamespaceLabelKey: "knative-serving",
				},
			},
			Data: map[string][]byte{
				"test-data": []byte("abcd"),
			},
		}},
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			originSecrets := map[string]*corev1.Secret{
				fmt.Sprintf("%s/%s", c.originSecret.Namespace, c.originSecret.Name): c.originSecret,
			}
			secrets, err := MakeSecrets(ctx, originSecrets, &ci)
			if (err != nil) != c.wantErr {
				t.Fatalf("Test: %q; MakeSecrets() error = %v, WantErr %v", c.name, err, c.wantErr)
			}
			if diff := cmp.Diff(c.expected, secrets); diff != "" {
				t.Errorf("Unexpected secrets (-want, +got): %v", diff)
			}
		})
	}
}

func TestMakeWildcardSecrets(t *testing.T) {
	ctx := TestContextWithLogger(t)
	ctx = config.ToContext(ctx, &config.Config{
		Istio: &config.Istio{
			IngressGateways: []config.Gateway{{
				Name: "test-gateway",
				// The namespace of Istio gateway service is istio-system.
				ServiceURL: "istio-ingressgateway.istio-system.svc.cluster.local",
			}},
		},
	})

	cases := []struct {
		name         string
		originSecret *corev1.Secret
		expected     []*corev1.Secret
		wantErr      bool
	}{{
		name: "target secret namespace (istio-system) is the same as the origin secret namespace (istio-system).",
		originSecret: &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "istio-system",
				UID:       "1234",
			},
			Data: map[string][]byte{
				"test-data": []byte("abcd"),
			}},
		expected: []*corev1.Secret{},
	}, {
		name: "target secret namespace (istio-system) is different from the origin secret namespace (knative-serving).",
		originSecret: &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "knative-serving",
				UID:       "1234",
			},
			Data: map[string][]byte{
				"test-data": []byte("abcd"),
			}},
		expected: []*corev1.Secret{{
			ObjectMeta: metav1.ObjectMeta{
				Name: targetWildcardSecretName("test-secret", "knative-serving"),
				// Expected secret should be in istio-system which is
				// the ns of Istio gateway service.
				Namespace: "istio-system",
				Labels:    map[string]string{},
			},
			Data: map[string][]byte{
				"test-data": []byte("abcd"),
			},
		}},
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			originSecrets := map[string]*corev1.Secret{
				fmt.Sprintf("%s/%s", c.originSecret.Namespace, c.originSecret.Name): c.originSecret,
			}
			secrets, err := MakeWildcardSecrets(ctx, originSecrets)
			if (err != nil) != c.wantErr {
				t.Fatalf("Test: %q; MakeWildcardSecrets() error = %v, WantErr %v", c.name, err, c.wantErr)
			}
			if diff := cmp.Diff(c.expected, secrets); diff != "" {
				t.Errorf("Unexpected secrets (-want, +got): %v", diff)
			}
		})
	}
}

func TestCategorizeSecrets(t *testing.T) {
	cases := []struct {
		name            string
		secrets         map[string]*corev1.Secret
		wantNonWildcard map[string]*corev1.Secret
		wantWildcard    map[string]*corev1.Secret
		wantErr         bool
	}{{
		name: "work correctly",
		secrets: map[string]*corev1.Secret{
			"wildcard":    wildcardCert,
			"nonwildcard": nonWildcardCert,
		},
		wantNonWildcard: map[string]*corev1.Secret{
			"nonwildcard": nonWildcardCert,
		},
		wantWildcard: map[string]*corev1.Secret{
			"wildcard": wildcardCert,
		},
	}, {
		name: "invalid secret",
		secrets: map[string]*corev1.Secret{
			"invalidSecret": &testSecret,
		},
		wantErr: true,
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotNonWildcard, gotWildcard, err := CategorizeSecrets(c.secrets)
			if gotErr := (err != nil); c.wantErr != gotErr {
				t.Fatalf("Test: %q; CategorizeSecrets() error = %v, WantErr %v", c.name, err, c.wantErr)
			}
			if diff := cmp.Diff(c.wantNonWildcard, gotNonWildcard); diff != "" {
				t.Fatalf("Unexpected non-wildcard secrets (-want, +got): %s", diff)
			}
			if diff := cmp.Diff(c.wantWildcard, gotWildcard); diff != "" {
				t.Fatalf("Unexpected wildcard secrets (-want, +got): %s", diff)
			}
		})
	}
}

func TestGetHostsFromCertSecret(t *testing.T) {
	cases := []struct {
		name      string
		secret    *corev1.Secret
		wantHosts []string
		wantErr   bool
	}{{
		name:      "wildcard host",
		secret:    wildcardCert,
		wantHosts: []string{"*.example.com"},
	}, {
		name:      "non-wildcard host",
		secret:    nonWildcardCert,
		wantHosts: []string{"test.example.com"},
	}, {
		name:    "invalid cert",
		secret:  &testSecret,
		wantErr: true,
	}}
	for _, c := range cases {
		hosts, err := GetHostsFromCertSecret(c.secret)
		if gotErr := (err != nil); c.wantErr != gotErr {
			t.Fatalf("Test: %q; GetHostsFromCertSecret() error = %v, WantErr %v", c.name, err, c.wantErr)
		}
		if diff := cmp.Diff(c.wantHosts, hosts); diff != "" {
			t.Fatalf("Unexpected hosts (-want, +got): %s", diff)
		}
	}
}
