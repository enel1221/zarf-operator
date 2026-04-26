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
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// zarfStateSecretKey is the Secret data key under which zarf serializes its
// internal state as JSON. The shape matches state.State in the zarf SDK.
const zarfStateSecretKey = "state"

// zarfState mirrors the subset of zarf's internal state blob that the
// operator consumes. Only registryInfo is parsed today; gitServer and
// artifactServer are listed here as a hook for follow-up PRs.
type zarfState struct {
	RegistryInfo zarfRegistryInfo `json:"registryInfo"`
}

// zarfRegistryInfo mirrors state.RegistryInfo from the zarf SDK — the fields
// that zarf's Deploy honors when set on DeployOptions.RegistryInfo.
type zarfRegistryInfo struct {
	Address      string `json:"address"`
	NodePort     int32  `json:"nodePort"`
	Secret       string `json:"secret"`
	PushUsername string `json:"pushUsername"`
	PushPassword string `json:"pushPassword"`
	PullUsername string `json:"pullUsername"`
	PullPassword string `json:"pullPassword"`
}

// parseZarfStateSecret decodes a zarf-state-shaped Secret. It returns an
// error if the Secret is nil, lacks the "state" key, or its contents are not
// valid JSON. A Secret where "state" is present but registryInfo is absent
// parses successfully and yields a zero-valued RegistryInfo — the caller
// decides whether that's acceptable given the rest of the config.
func parseZarfStateSecret(s *corev1.Secret) (*zarfState, error) {
	if s == nil {
		return nil, fmt.Errorf("zarf-state secret is nil")
	}
	raw, ok := s.Data[zarfStateSecretKey]
	if !ok {
		return nil, fmt.Errorf("secret %q is missing required data key %q (expected a zarf-state shaped Secret)", s.Name, zarfStateSecretKey)
	}
	var state zarfState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("secret %q data.%s is not valid JSON: %w", s.Name, zarfStateSecretKey, err)
	}
	return &state, nil
}
