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
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// clusterConfigKey is the Secret data key that holds the cluster connection
// payload for both supported shapes (vcluster-style and Argo CD cluster-style).
const clusterConfigKey = "config"

// argoTLSClientConfig mirrors the shape Argo CD uses for its cluster Secrets.
// Fields are base64-encoded in JSON; []byte fields have that handled by the
// encoding/json package automatically.
type argoTLSClientConfig struct {
	Insecure bool   `json:"insecure,omitempty"`
	CAData   []byte `json:"caData,omitempty"`
	CertData []byte `json:"certData,omitempty"`
	KeyData  []byte `json:"keyData,omitempty"`
}

type argoClusterConfig struct {
	TLSClientConfig argoTLSClientConfig `json:"tlsClientConfig"`
	BearerToken     string              `json:"bearerToken,omitempty"`
}

// normalizeKubeconfigSecret extracts a kubeconfig YAML document from the given
// Secret, accepting either of the two supported shapes:
//
//  1. vcluster-style: secret.Data["config"] is already a kubeconfig YAML. The
//     bytes are returned unchanged.
//  2. Argo CD cluster-secret style: secret.Data["config"] is a JSON object
//     with a "tlsClientConfig" and/or "bearerToken". The server URL is taken
//     from secret.Data["server"] and a minimal kubeconfig is synthesized.
//
// If neither shape matches, an error is returned.
func normalizeKubeconfigSecret(secret *corev1.Secret) ([]byte, error) {
	if secret == nil {
		return nil, errors.New("secret is nil")
	}
	raw, ok := secret.Data[clusterConfigKey]
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("secret %s/%s missing required %q data key",
			secret.Namespace, secret.Name, clusterConfigKey)
	}

	if cfg, err := clientcmd.Load(raw); err == nil && len(cfg.Clusters) > 0 {
		return raw, nil
	}

	var argo argoClusterConfig
	if err := json.Unmarshal(raw, &argo); err == nil && argoHasCredentials(&argo) {
		return buildKubeconfigFromArgo(secret, &argo)
	}

	return nil, fmt.Errorf(
		"secret %s/%s %q key is neither a kubeconfig YAML nor an Argo-style cluster config",
		secret.Namespace, secret.Name, clusterConfigKey)
}

func argoHasCredentials(a *argoClusterConfig) bool {
	tls := a.TLSClientConfig
	return a.BearerToken != "" || len(tls.CAData) > 0 || len(tls.CertData) > 0 || len(tls.KeyData) > 0
}

func buildKubeconfigFromArgo(secret *corev1.Secret, argo *argoClusterConfig) ([]byte, error) {
	server := string(secret.Data["server"])
	if server == "" {
		return nil, fmt.Errorf(
			"secret %s/%s uses Argo-style cluster config but is missing the %q key",
			secret.Namespace, secret.Name, "server")
	}

	clusterName := string(secret.Data["name"])
	if clusterName == "" {
		clusterName = secret.Name
	}
	userName := clusterName + "-user"
	contextName := clusterName

	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[clusterName] = &clientcmdapi.Cluster{
		Server:                   server,
		CertificateAuthorityData: argo.TLSClientConfig.CAData,
		InsecureSkipTLSVerify:    argo.TLSClientConfig.Insecure,
	}
	authInfo := clientcmdapi.NewAuthInfo()
	if len(argo.TLSClientConfig.CertData) > 0 {
		authInfo.ClientCertificateData = argo.TLSClientConfig.CertData
	}
	if len(argo.TLSClientConfig.KeyData) > 0 {
		authInfo.ClientKeyData = argo.TLSClientConfig.KeyData
	}
	if argo.BearerToken != "" {
		authInfo.Token = argo.BearerToken
	}
	cfg.AuthInfos[userName] = authInfo
	cfg.Contexts[contextName] = &clientcmdapi.Context{
		Cluster:  clusterName,
		AuthInfo: userName,
	}
	cfg.CurrentContext = contextName

	out, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal synthesized kubeconfig: %w", err)
	}
	return out, nil
}
