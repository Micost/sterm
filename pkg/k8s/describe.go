package k8s

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func Describe(u *unstructured.Unstructured) string {
	var b strings.Builder

	name := u.GetName()
	ns := u.GetNamespace()
	kind := u.GetKind()
	apiVersion := u.GetAPIVersion()
	labels := u.GetLabels()
	annotations := u.GetAnnotations()

	b.WriteString(fmt.Sprintf("Name:         %s\n", name))
	if ns != "" {
		b.WriteString(fmt.Sprintf("Namespace:    %s\n", ns))
	}
	b.WriteString(fmt.Sprintf("Kind:         %s\n", kind))
	b.WriteString(fmt.Sprintf("APIVersion:   %s\n", apiVersion))

	if u.GetCreationTimestamp().Time.IsZero() {
		b.WriteString(fmt.Sprintf("Created:      <unknown>\n"))
	} else {
		b.WriteString(fmt.Sprintf("Created:      %s\n", u.GetCreationTimestamp().Time.Format("Mon Jan 2 15:04:05 2006")))
	}

	if u.GetDeletionTimestamp() != nil {
		b.WriteString(fmt.Sprintf("Deleted:      %s\n", u.GetDeletionTimestamp().Time.Format("Mon Jan 2 15:04:05 2006")))
	}

	if len(labels) > 0 {
		b.WriteString("Labels:\n")
		keys := sortedKeys(labels)
		for _, k := range keys {
			v := labels[k]
			if len(v) > 50 {
				v = v[:50] + "..."
			}
			b.WriteString(fmt.Sprintf("  %-30s %s\n", k+":", v))
		}
	}

	if len(annotations) > 0 {
		b.WriteString("Annotations:\n")
		keys := sortedKeys(annotations)
		for _, k := range keys {
			v := annotations[k]
			if len(v) > 70 {
				v = v[:70] + "..."
			}
			b.WriteString(fmt.Sprintf("  %-30s %s\n", k+":", v))
		}
	}

	writeSection(&b, u, "spec", "Spec", 2)
	writeSection(&b, u, "status", "Status", 2)

	b.WriteString("\n")
	return b.String()
}

func writeSection(b *strings.Builder, u *unstructured.Unstructured, path, title string, depth int) {
	val, ok, err := unstructured.NestedMap(u.Object, path)
	if !ok || err != nil || len(val) == 0 {
		return
	}

	b.WriteString(fmt.Sprintf("%s:\n", title))
	writeMap(b, val, depth)
}

func writeMap(b *strings.Builder, m map[string]interface{}, indent int) {
	prefix := strings.Repeat("  ", indent)
	keys := sortedKeys(m)

	for _, k := range keys {
		v := m[k]
		switch val := v.(type) {
		case map[string]interface{}:
			if len(val) > 0 {
				b.WriteString(fmt.Sprintf("%s%s:\n", prefix, k))
				writeMap(b, val, indent+1)
			}
		case []interface{}:
			if len(val) > 0 {
				b.WriteString(fmt.Sprintf("%s%s:\n", prefix, k))
				for i, item := range val {
					if m2, ok := item.(map[string]interface{}); ok {
						b.WriteString(fmt.Sprintf("%s  %d:\n", prefix, i))
						writeMap(b, m2, indent+2)
					} else {
						b.WriteString(fmt.Sprintf("%s  - %v\n", prefix, item))
					}
				}
			}
		case string:
			if val != "" {
				b.WriteString(fmt.Sprintf("%s%s: %s\n", prefix, k, val))
			}
		case float64:
			b.WriteString(fmt.Sprintf("%s%s: %v\n", prefix, k, val))
		case bool:
			b.WriteString(fmt.Sprintf("%s%s: %v\n", prefix, k, val))
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
