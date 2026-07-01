package k8s

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

type ResourceMeta struct {
	GVR        schema.GroupVersionResource
	Kind       string
	Namespaced bool
	Category   string
}

func (r ResourceMeta) Name() string {
	return r.GVR.Resource
}

func (r ResourceMeta) APIVersion() string {
	return r.GVR.GroupVersion().String()
}

func (c *Client) Discover() ([]ResourceMeta, error) {
	apiResources, err := discovery.ServerPreferredResources(c.discovery)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var out []ResourceMeta

	for _, list := range apiResources {
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			continue
		}

		for _, r := range list.APIResources {
			if !strings.Contains(r.Verbs.String(), "list") {
				continue
			}
			if strings.Contains(r.Name, "/") {
				continue
			}

			gvr := schema.GroupVersionResource{
				Group:    gv.Group,
				Version:  gv.Version,
				Resource: r.Name,
			}

			key := gvr.String()
			if seen[key] {
				continue
			}
			seen[key] = true

			cat := category(gvr)
			out = append(out, ResourceMeta{
				GVR:        gvr,
				Kind:       r.Kind,
				Namespaced: r.Namespaced,
				Category:   cat,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name() < out[j].Name()
	})

	return out, nil
}

func category(gvr schema.GroupVersionResource) string {
	if gvr.Group != "" {
		return "custom"
	}

	switch gvr.Resource {
	case "pods", "replicationcontrollers", "replicasets", "deployments",
		"statefulsets", "daemonsets", "jobs", "cronjobs":
		return "workloads"
	case "services", "endpoints", "endpointslices", "ingresses":
		return "network"
	case "configmaps", "secrets", "persistentvolumeclaims",
		"persistentvolumes", "storageclasses":
		return "config"
	case "namespaces", "nodes", "events":
		return "cluster"
	case "serviceaccounts", "roles", "rolebindings",
		"clusterroles", "clusterrolebindings":
		return "rbac"
	default:
		return "other"
	}
}
