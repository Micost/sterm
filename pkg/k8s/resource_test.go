package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCategory(t *testing.T) {
	tests := []struct {
		name string
		gvr  schema.GroupVersionResource
		want string
	}{
		{"pods", gvr("", "v1", "pods"), "common"},
		{"deployments", gvr("apps", "v1", "deployments"), "common"},
		{"services", gvr("", "v1", "services"), "common"},
		{"configmaps", gvr("", "v1", "configmaps"), "common"},
		{"secrets", gvr("", "v1", "secrets"), "common"},
		{"namespaces", gvr("", "v1", "namespaces"), "common"},
		{"nodes", gvr("", "v1", "nodes"), "common"},
		{"ingresses", gvr("networking.k8s.io", "v1", "ingresses"), "common"},
		{"prometheuses", gvr("monitoring.coreos.com", "v1", "prometheuses"), "crd"},
		{"certificates", gvr("cert-manager.io", "v1", "certificates"), "crd"},
		{"replicasets", gvr("apps", "v1", "replicasets"), "other"},
		{"endpoints", gvr("", "v1", "endpoints"), "other"},
		{"events", gvr("", "v1", "events"), "other"},
		{"storageclasses", gvr("storage.k8s.io", "v1", "storageclasses"), "other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := category(tt.gvr); got != tt.want {
				t.Errorf("category() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsStandardGroup(t *testing.T) {
	tests := []struct {
		group string
		want  bool
	}{
		{"", true},
		{"apps", true},
		{"batch", true},
		{"networking.k8s.io", true},
		{"monitoring.coreos.com", false},
		{"cert-manager.io", false},
		{"custom.example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.group, func(t *testing.T) {
			if got := isStandardGroup(tt.group); got != tt.want {
				t.Errorf("isStandardGroup(%q) = %v, want %v", tt.group, got, tt.want)
			}
		})
	}
}

func gvr(group, version, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
}
