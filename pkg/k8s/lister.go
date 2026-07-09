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

	extraCol := ""
	isPod := gvr.Resource == "pods"
	if isPod {
		cols = []string{"NAME", "NAMESPACE", "KIND", "AGE", "READY", "STATUS", "NODE"}
		extraCol = "NODE"
	} else {
		switch gvr.Resource {
		case "jobs":
			extraCol = "COMPLETIONS"
		case "deployments", "statefulsets", "daemonsets", "replicasets":
			extraCol = "READY"
		}
		if extraCol != "" {
			cols = append(cols, extraCol)
		}
	}

	rows := make([]TableRow, 0, len(list.Items))

	for i := range list.Items {
		item := &list.Items[i]
		nc := len(cols)
		cells := make([]string, nc)
		cells[0] = item.GetName()
		cells[1] = item.GetNamespace()
		cells[2] = item.GetKind()

		age := age(item.GetCreationTimestamp())
		cells[3] = age

		if isPod {
			ready, total := podReadyContainers(item)
			cells[4] = fmt.Sprintf("%d/%d", ready, total)
			cells[5] = extractStatus(item)
			nodeName, _, _ := unstructured.NestedString(item.Object, "spec", "nodeName")
			cells[6] = nodeName
		} else {
			cells[4] = extractStatus(item)
			if extraCol == "COMPLETIONS" {
				succeeded, _, _ := unstructured.NestedInt64(item.Object, "status", "succeeded")
				desired, _, _ := unstructured.NestedInt64(item.Object, "spec", "completions")
				if desired == 0 {
					desired = 1
				}
				cells[5] = fmt.Sprintf("%d/%d", succeeded, desired)
			} else if extraCol == "READY" {
				ready, _, _ := unstructured.NestedInt64(item.Object, "status", "readyReplicas")
				desired, _, _ := unstructured.NestedInt64(item.Object, "spec", "replicas")
				cells[5] = fmt.Sprintf("%d/%d", ready, desired)
			}
		}

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

	kind := u.GetKind()

	// Pod-specific status: check container states for detail
	if kind == "Pod" {
		status := podStatus(u)
		if status != "" {
			return status
		}
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

func podStatus(u *unstructured.Unstructured) string {
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")

	// Check deletion timestamp for Terminating
	ts, found, _ := unstructured.NestedString(u.Object, "metadata", "deletionTimestamp")
	if found && ts != "" {
		return "Terminating"
	}

	evicted, found, _ := unstructured.NestedString(u.Object, "status", "reason")
	if found && evicted == "Evicted" {
		return "Evicted"
	}

	// Check container statuses for detailed state
	containers, ok, _ := unstructured.NestedSlice(u.Object, "status", "containerStatuses")
	if !ok {
		return ""
	}
	for _, c := range containers {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		ready, _, _ := unstructured.NestedBool(cm, "ready")
		if !ready {
			// Check waiting state
			reason, found, _ := unstructured.NestedString(cm, "state", "waiting", "reason")
			if found && reason != "" {
				return reason
			}
			// Check terminated state
			reason, found, _ = unstructured.NestedString(cm, "state", "terminated", "reason")
			if found && reason != "" {
				if reason == "Completed" && phase == "Succeeded" {
					return "Succeeded"
				}
				return reason
			}
		}
	}

	// Check init container statuses
	initContainers, ok, _ := unstructured.NestedSlice(u.Object, "status", "initContainerStatuses")
	if ok {
		for _, c := range initContainers {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			reason, found, _ := unstructured.NestedString(cm, "state", "waiting", "reason")
			if found && reason != "" {
				return "Init:" + reason
			}
			terminated, found, _ := unstructured.NestedInt64(cm, "state", "terminated", "exitCode")
			if found {
				if finished, _, _ := unstructured.NestedBool(cm, "state", "terminated", "finished"); finished {
					continue
				}
				return fmt.Sprintf("Init:ExitCode:%d", terminated)
			}
		}
	}

	return phase
}

func podReadyContainers(u *unstructured.Unstructured) (ready, total int) {
	containers, ok, _ := unstructured.NestedSlice(u.Object, "status", "containerStatuses")
	if !ok {
		return 0, 0
	}
	total = len(containers)
	for _, c := range containers {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		r, _, _ := unstructured.NestedBool(cm, "ready")
		if r {
			ready++
		}
	}
	return
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

func (c *Client) Delete(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) error {
	cli := c.dynamic.Resource(gvr)
	if ns != "" {
		return cli.Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	}
	return cli.Delete(ctx, name, metav1.DeleteOptions{})
}

func (c *Client) Update(ctx context.Context, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	cli := c.dynamic.Resource(gvr)
	if ns := obj.GetNamespace(); ns != "" {
		_, err := cli.Namespace(ns).Update(ctx, obj, metav1.UpdateOptions{})
		return err
	}
	_, err := cli.Update(ctx, obj, metav1.UpdateOptions{})
	return err
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


