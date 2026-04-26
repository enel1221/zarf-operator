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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// internalRegistryStateJSON mirrors the shape written by `zarf init` when
// using the in-cluster registry (distro=k3d). Taken verbatim from the
// k3d-jade cluster in the current test environment, with sensitive values
// replaced by recognizable placeholders.
const internalRegistryStateJSON = `{
  "zarfAppliance": false,
  "distro": "k3d",
  "storageClass": "local-path",
  "registryInfo": {
    "pushUsername": "zarf-push",
    "pushPassword": "push-pw-placeholder",
    "pullUsername": "zarf-pull",
    "pullPassword": "pull-pw-placeholder",
    "address": "127.0.0.1:31999",
    "nodePort": 31999,
    "secret": "zarf-agent-secret-placeholder",
    "registryMode": "nodeport"
  },
  "gitServer": {
    "pushUsername": "zarf-git-user",
    "pushPassword": "gitpush-placeholder",
    "address": "http://zarf-gitea-http.zarf.svc.cluster.local:3000"
  }
}`

// externalRegistryStateJSON mirrors the shape written by `zarf init
// --registry-url ...` against an external (e.g. Azure Container Registry)
// registry. Taken verbatim from the sedptest cluster, values redacted.
const externalRegistryStateJSON = `{
  "distro": "aks",
  "registryInfo": {
    "pushUsername": "write",
    "pushPassword": "external-pw-placeholder",
    "pullUsername": "write",
    "pullPassword": "external-pw-placeholder",
    "address": "sedptestregistry.azurecr.us",
    "nodePort": 0,
    "secret": "external-agent-secret-placeholder"
  }
}`

func makeStateSecret(t *testing.T, name string, data map[string][]byte) *corev1.Secret {
	t.Helper()
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Data:       data,
	}
}

func TestParseZarfStateSecret_InternalRegistry(t *testing.T) {
	s := makeStateSecret(t, "zarf-state", map[string][]byte{"state": []byte(internalRegistryStateJSON)})
	out, err := parseZarfStateSecret(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := out.RegistryInfo
	if r.Address != "127.0.0.1:31999" {
		t.Errorf("address: got %q, want 127.0.0.1:31999", r.Address)
	}
	if r.NodePort != 31999 {
		t.Errorf("nodePort: got %d, want 31999", r.NodePort)
	}
	if r.PushUsername != "zarf-push" {
		t.Errorf("pushUsername: got %q", r.PushUsername)
	}
	if r.PullUsername != "zarf-pull" {
		t.Errorf("pullUsername: got %q", r.PullUsername)
	}
	if r.Secret != "zarf-agent-secret-placeholder" {
		t.Errorf("secret: got %q", r.Secret)
	}
}

func TestParseZarfStateSecret_ExternalRegistry(t *testing.T) {
	s := makeStateSecret(t, "zarf-state", map[string][]byte{"state": []byte(externalRegistryStateJSON)})
	out, err := parseZarfStateSecret(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := out.RegistryInfo
	if r.Address != "sedptestregistry.azurecr.us" {
		t.Errorf("address: got %q", r.Address)
	}
	if r.NodePort != 0 {
		t.Errorf("external registry should have nodePort=0, got %d", r.NodePort)
	}
	if r.PushUsername != "write" {
		t.Errorf("pushUsername: got %q", r.PushUsername)
	}
}

func TestParseZarfStateSecret_MissingStateKey(t *testing.T) {
	s := makeStateSecret(t, "bad", map[string][]byte{"other": []byte("{}")})
	_, err := parseZarfStateSecret(s)
	if err == nil || !strings.Contains(err.Error(), "missing required data key") {
		t.Fatalf("expected missing-key error, got %v", err)
	}
}

func TestParseZarfStateSecret_MalformedJSON(t *testing.T) {
	s := makeStateSecret(t, "bad", map[string][]byte{"state": []byte("this is not json")})
	_, err := parseZarfStateSecret(s)
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("expected malformed-JSON error, got %v", err)
	}
}

func TestParseZarfStateSecret_NilSecret(t *testing.T) {
	_, err := parseZarfStateSecret(nil)
	if err == nil {
		t.Fatalf("expected error for nil secret")
	}
}

func TestParseZarfStateSecret_EmptyRegistryInfo(t *testing.T) {
	// A zarf-state secret with no registryInfo is schema-valid but yields
	// zero values — caller decides if that's acceptable.
	s := makeStateSecret(t, "partial", map[string][]byte{"state": []byte(`{"distro":"k3d"}`)})
	out, err := parseZarfStateSecret(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.RegistryInfo.Address != "" {
		t.Errorf("expected empty address, got %q", out.RegistryInfo.Address)
	}
}
