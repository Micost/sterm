package k8s

import (
	"context"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

type TableRow struct {
	Cells []string
	Obj   *unstructured.Unstructured
}

type TableData struct {
	GVR     schema.GroupVersionResource
	Columns []string
	Rows    []TableRow
}

func (c *Client) List(ctx context.Context, gvr schema.GroupVersionResource, ns string) (*TableData, error) {
	cli := c.dynamic.Resource(gvr)
	var list *unstructured.UnstructuredList
	var err error

	if ns != "" {
		list, err = cli.Namespace(ns).List(ctx, metav1.ListOptions{})
	} else {
		list, err = cli.List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", gvr.Resource, err)
	}

	cols := []string{"NAME", "NAMESPACE", "KIND", "AGE", "STATUS"}
	rows := make([]TableRow, 0, len(list.Items))

	for i := range list.Items {
		item := &list.Items[i]
		cells := make([]string, len(cols))
		cells[0] = item.GetName()
		cells[1] = item.GetNamespace()
		cells[2] = item.GetKind()

		age := age(item.GetCreationTimestamp())
		cells[3] = age

		cells[4] = extractStatus(item)

		rows = append(rows, TableRow{Cells: cells, Obj: item})
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Cells[0] < rows[j].Cells[0]
	})

	return &TableData{
		GVR:     gvr,
		Columns: cols,
		Rows:    rows,
	}, nil
}

func extractStatus(u *unstructured.Unstructured) string {
	if u == nil {
		return ""
	}

	phase, ok, err := unstructured.NestedString(u.Object, "status", "phase")
	if ok && err == nil && phase != "" {
		return phase
	}

	state, ok, err := unstructured.NestedString(u.Object, "status", "state")
	if ok && err == nil && state != "" {
		return state
	}

	replicas, ok, err := unstructured.NestedInt64(u.Object, "status", "readyReplicas")
	if ok && err == nil {
		desired, _, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
		return fmt.Sprintf("%d/%d", replicas, desired)
	}

	available, ok, err := unstructured.NestedInt64(u.Object, "status", "availableReplicas")
	if ok && err == nil {
		desired, _, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
		return fmt.Sprintf("%d/%d", available, desired)
	}

	conditions, ok, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if ok && err == nil && len(conditions) > 0 {
		if last, ok := conditions[len(conditions)-1].(map[string]interface{}); ok {
			if t, ok := last["type"].(string); ok {
				return t
			}
		}
	}

	return ""
}

func age(t metav1.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t.Time)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func (c *Client) Get(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error) {
	cli := c.dynamic.Resource(gvr)
	if ns != "" {
		return cli.Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	}
	return cli.Get(ctx, name, metav1.GetOptions{})
}

func ToYAML(u *unstructured.Unstructured) (string, error) {
	raw, err := yaml.Marshal(u.Object)
	if err != nil {
		return "", fmt.Errorf("marshal yaml: %w", err)
	}
	return string(raw), nil
}

func (c *Client) Namespaces(ctx context.Context) ([]string, error) {
	nsGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	data, err := c.List(ctx, nsGVR, "")
	if err != nil {
		return nil, err
	}
	names := make([]string, len(data.Rows))
	for i, r := range data.Rows {
		names[i] = r.Cells[0]
	}
	sort.Strings(names)
	return names, nil
}


