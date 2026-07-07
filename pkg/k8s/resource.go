package k8s

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

type ResourceMeta struct {
	GVR        schema.GroupVersionResource
	Kind       string
	ShortName  string
	Namespaced bool
	Category   string
}

func (r ResourceMeta) Name() string {
	if r.ShortName != "" {
		return r.ShortName
	}
	return r.GVR.Resource
}

func (r ResourceMeta) APIVersion() string {
	return r.GVR.GroupVersion().String()
}

func (c *Client) Discover() ([]ResourceMeta, error) {
	groups, err := c.discovery.ServerGroups()
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var out []ResourceMeta

	for _, group := range groups.Groups {
		for _, version := range group.Versions {
			list, err := c.discovery.ServerResourcesForGroupVersion(version.GroupVersion)
			if err != nil {
				continue
			}

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

				key := r.Name
				if seen[key] {
					continue
				}
				seen[key] = true

				shortName := ""
				if len(r.ShortNames) > 0 {
					shortName = r.ShortNames[0]
				}
				cat := category(gvr)
				out = append(out, ResourceMeta{
					GVR:        gvr,
					Kind:       r.Kind,
					ShortName:  shortName,
					Namespaced: r.Namespaced,
					Category:   cat,
				})
			}
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

func isStandardGroup(group string) bool {
	switch group {
	case "", "apps", "batch", "networking.k8s.io",
		"rbac.authorization.k8s.io", "storage.k8s.io",
		"policy", "autoscaling", "admissionregistration.k8s.io",
		"scheduling.k8s.io", "coordination.k8s.io",
		"certificates.k8s.io", "node.k8s.io", "events.k8s.io",
		"discovery.k8s.io", "flowcontrol.apiserver.k8s.io",
		"resource.k8s.io", "storagemigration.k8s.io":
		return true
	}
	return false
}

func isCommonResource(resource string) bool {
	switch resource {
	case "pods", "deployments", "statefulsets", "daemonsets",
		"jobs", "cronjobs",
		"services", "ingresses",
		"configmaps", "secrets",
		"namespaces", "nodes",
		"persistentvolumeclaims", "persistentvolumes",
		"serviceaccounts":
		return true
	}
	return false
}

func category(gvr schema.GroupVersionResource) string {
	if !isStandardGroup(gvr.Group) {
		return "crd"
	}
	if isCommonResource(gvr.Resource) {
		return "common"
	}
	return "other"
}
