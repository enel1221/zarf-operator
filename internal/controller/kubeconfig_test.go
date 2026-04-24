/*
Copyright 2026.

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

package controller

import (
	"bytes"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// vclusterKubeconfig is a minimal but valid kubeconfig YAML document, mirroring
// the shape a vcluster-generated Secret places under the "config" key.
const vclusterKubeconfig = `apiVersion: v1
clusters:
- cluster:
    server: https://vcluster-hub.vcluster-hub
    insecure-skip-tls-verify: true
  name: kubernetes
contexts:
- context:
    cluster: kubernetes
    user: kubernetes-super-admin
  name: kubernetes-super-admin@kubernetes
current-context: kubernetes-super-admin@kubernetes
kind: Config
users:
- name: kubernetes-super-admin
  user:
    token: abc.def.ghi
`

// fakeArgoConfig is a minimal Argo-style cluster config JSON, matching
// kubeconfig-secret1.yaml's schema (tlsClientConfig with base64-encoded PEM
// blobs that the encoding/json package decodes into []byte automatically).
// The caData/certData/keyData values here are deliberately short (they do not
// need to be real PEMs for the normalizer to round-trip them — only the Argo
// Cluster schema matters at this layer).
const fakeArgoConfig = `{
  "tlsClientConfig": {
    "insecure": false,
    "caData": "Y2EtYnl0ZXM=",
    "certData": "Y2VydC1ieXRlcw==",
    "keyData": "a2V5LWJ5dGVz"
  }
}`

func newSecret(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default"},
		Data:       data,
	}
}

func TestNormalizeKubeconfigSecret_VclusterStyle(t *testing.T) {
	raw := []byte(vclusterKubeconfig)
	secret := newSecret(map[string][]byte{"config": raw})

	out, err := normalizeKubeconfigSecret(secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(out, raw) {
		t.Errorf("expected vcluster-style kubeconfig to be returned unchanged; got a different payload")
	}
}

func TestNormalizeKubeconfigSecret_ArgoStyle(t *testing.T) {
	secret := newSecret(map[string][]byte{
		"config": []byte(fakeArgoConfig),
		"server": []byte("https://example.cluster.local:6443"),
		"name":   []byte("example"),
	})

	out, err := normalizeKubeconfigSecret(secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := clientcmd.Load(out)
	if err != nil {
		t.Fatalf("synthesized kubeconfig does not parse: %v", err)
	}
	cluster, ok := cfg.Clusters["example"]
	if !ok {
		t.Fatalf("expected cluster named 'example' in synthesized kubeconfig, got clusters: %v", keysOf(cfg.Clusters))
	}
	if cluster.Server != "https://example.cluster.local:6443" {
		t.Errorf("unexpected server: %q", cluster.Server)
	}
	if !bytes.Equal(cluster.CertificateAuthorityData, []byte("ca-bytes")) {
		t.Errorf("unexpected caData: %q", cluster.CertificateAuthorityData)
	}
	user, ok := cfg.AuthInfos["example-user"]
	if !ok {
		t.Fatalf("expected authinfo named 'example-user'; got: %v", keysOfAuth(cfg.AuthInfos))
	}
	if !bytes.Equal(user.ClientCertificateData, []byte("cert-bytes")) {
		t.Errorf("unexpected certData: %q", user.ClientCertificateData)
	}
	if !bytes.Equal(user.ClientKeyData, []byte("key-bytes")) {
		t.Errorf("unexpected keyData: %q", user.ClientKeyData)
	}
}

func TestNormalizeKubeconfigSecret_ArgoStyle_BearerToken(t *testing.T) {
	argo := `{"tlsClientConfig":{"caData":"Y2EtYnl0ZXM="},"bearerToken":"tok-123"}`
	secret := newSecret(map[string][]byte{
		"config": []byte(argo),
		"server": []byte("https://tok.example:6443"),
	})

	out, err := normalizeKubeconfigSecret(secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg, err := clientcmd.Load(out)
	if err != nil {
		t.Fatalf("synthesized kubeconfig does not parse: %v", err)
	}
	// clusterName defaults to secret.Name when "name" data key is missing.
	user := cfg.AuthInfos["creds-user"]
	if user == nil {
		t.Fatalf("expected 'creds-user' authinfo; got: %v", keysOfAuth(cfg.AuthInfos))
	}
	if user.Token != "tok-123" {
		t.Errorf("expected bearer token, got %q", user.Token)
	}
	if len(user.ClientCertificateData) != 0 {
		t.Errorf("expected no client cert for token-only auth")
	}
}

func TestNormalizeKubeconfigSecret_MissingConfigKey(t *testing.T) {
	secret := newSecret(map[string][]byte{"other": []byte("x")})
	_, err := normalizeKubeconfigSecret(secret)
	if err == nil || !strings.Contains(err.Error(), "missing required") {
		t.Fatalf("expected missing-key error, got %v", err)
	}
}

func TestNormalizeKubeconfigSecret_MalformedConfig(t *testing.T) {
	secret := newSecret(map[string][]byte{"config": []byte("this is neither yaml kubeconfig nor argo json")})
	_, err := normalizeKubeconfigSecret(secret)
	if err == nil || !strings.Contains(err.Error(), "neither a kubeconfig YAML nor an Argo-style") {
		t.Fatalf("expected unsupported-format error, got %v", err)
	}
}

func TestNormalizeKubeconfigSecret_ArgoMissingServer(t *testing.T) {
	secret := newSecret(map[string][]byte{"config": []byte(fakeArgoConfig)})
	_, err := normalizeKubeconfigSecret(secret)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing-server error, got %v", err)
	}
}

func TestNormalizeKubeconfigSecret_NilSecret(t *testing.T) {
	_, err := normalizeKubeconfigSecret(nil)
	if err == nil {
		t.Fatalf("expected error for nil secret")
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysOfAuth[V any](m map[string]V) []string {
	return keysOf(m)
}
