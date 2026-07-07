//go:build integration
// +build integration

package k8s

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func setupClient(t *testing.T) *Client {
	t.Helper()
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = clientcmd.RecommendedHomeFile
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		config, err = rest.InClusterConfig()
		if err != nil {
			t.Fatalf("cannot build config: %v", err)
		}
	}
	c, err := NewClient(config)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestIntegration_Discover(t *testing.T) {
	c := setupClient(t)
	rr, err := c.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(rr) == 0 {
		t.Fatal("no resources discovered")
	}

	hasPod := false
	hasSvc := false
	hasNode := false
	for _, r := range rr {
		switch r.GVR.Resource {
		case "pods":
			hasPod = true
		case "services":
			hasSvc = true
		case "nodes":
			hasNode = true
		}
	}
	if !hasPod {
		t.Error("pods not found in discovery")
	}
	if !hasSvc {
		t.Error("services not found in discovery")
	}
	if !hasNode {
		t.Error("nodes not found in discovery")
	}
	t.Logf("discovered %d resources", len(rr))
}

func TestIntegration_CRUD(t *testing.T) {
	c := setupClient(t)
	ctx := context.Background()
	ns := "default"
	name := fmt.Sprintf("sterm-test-%d", time.Now().Unix())
	cmGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}

	// Create
	t.Logf("creating configmap %s", name)
	cm := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": ns,
		},
		"data": map[string]interface{}{"key1": "val1"},
	}}
	cli := c.Dynamic().Resource(cmGVR).Namespace(ns)
	_, err := cli.Create(ctx, cm, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// List
	data, err := c.List(ctx, cmGVR, ns)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, row := range data.Rows {
		if row.Cells[0] == name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("created configmap %s not found in list", name)
	}

	// Get
	obj, err := c.Get(ctx, cmGVR, ns, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if obj.GetName() != name {
		t.Errorf("Get name mismatch: %s", obj.GetName())
	}

	// Update
	obj.Object["data"].(map[string]interface{})["key2"] = "val2"
	_, err = cli.Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Delete
	err = c.Delete(ctx, cmGVR, ns, name)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	t.Log("CRUD cycle passed")
}

func TestIntegration_ListPods(t *testing.T) {
	c := setupClient(t)
	ctx := context.Background()
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

	data, err := c.List(ctx, podGVR, "")
	if err != nil {
		t.Fatalf("List pods: %v", err)
	}

	// pods should have NODE column
	hasNodeCol := false
	for _, col := range data.Columns {
		if col == "NODE" {
			hasNodeCol = true
			break
		}
	}
	if !hasNodeCol {
		t.Error("pods list missing NODE column")
	}

	t.Logf("listed %d pods across all namespaces", len(data.Rows))

	// if there are pods, verify they have node info
	for _, row := range data.Rows {
		t.Logf("  pod %s/%s node=%s", row.Cells[1], row.Cells[0], row.Cells[5])
	}
}

func TestIntegration_Describe(t *testing.T) {
	c := setupClient(t)
	ctx := context.Background()
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

	data, err := c.List(ctx, podGVR, "")
	if err != nil || len(data.Rows) == 0 {
		t.Skip("no pods to describe")
	}

	first := data.Rows[0]
	desc := Describe(first.Obj)
	if desc == "" {
		t.Error("describe returned empty")
	}
	t.Logf("describe %s: %d chars", first.Cells[0], len(desc))
}

func TestIntegration_Namespaces(t *testing.T) {
	c := setupClient(t)
	ns, err := c.Namespaces(context.Background())
	if err != nil {
		t.Fatalf("Namespaces: %v", err)
	}
	if len(ns) == 0 {
		t.Fatal("no namespaces found")
	}
	found := false
	for _, n := range ns {
		if n == "kube-system" {
			found = true
			break
		}
	}
	if !found {
		t.Error("kube-system namespace not found")
	}
	t.Logf("found %d namespaces", len(ns))
}

func TestIntegration_Exec(t *testing.T) {
	c := setupClient(t)
	ctx := context.Background()
	ns := "default"
	name := fmt.Sprintf("sterm-exec-test-%d", time.Now().Unix())
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

	// deploy a test pod with shell
	t.Logf("creating test pod %s", name)
	pod := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec": map[string]interface{}{
			"containers": []interface{}{
				map[string]interface{}{
					"name":    "test",
					"image":   "busybox",
					"command": []interface{}{"sleep", "60"},
				},
			},
		},
	}}
	_, err := c.Dynamic().Resource(podGVR).Namespace(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create pod: %v", err)
	}
	defer c.Delete(ctx, podGVR, ns, name)

	// wait for Running
	ready := false
	for i := 0; i < 30; i++ {
		obj, err := c.Get(ctx, podGVR, ns, name)
		if err == nil && obj != nil {
			phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
			if phase == "Running" {
				ready = true
				break
			}
		}
		time.Sleep(time.Second)
	}
	if !ready {
		t.Fatal("test pod did not become Running within 30s")
	}
	time.Sleep(2 * time.Second) // extra grace for container init

	var buf bytes.Buffer
	err = c.Exec(ns, name, "test",
		[]string{"sh", "-c", "echo hello-from-integration-test"}, nil, &buf, &buf)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(buf.String(), "hello-from-integration-test") {
		t.Errorf("unexpected exec output: %q", buf.String())
	}
	t.Logf("exec output: %s", buf.String())
}

func TestIntegration_Logs(t *testing.T) {
	c := setupClient(t)
	ctx := context.Background()
	ns := "default"
	name := fmt.Sprintf("sterm-log-test-%d", time.Now().Unix())
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

	t.Logf("creating test pod %s", name)
	pod := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec": map[string]interface{}{
			"containers": []interface{}{
				map[string]interface{}{
					"name":    "test",
					"image":   "busybox",
					"command": []interface{}{"sh", "-c", "echo log-line-1; echo log-line-2; sleep 60"},
				},
			},
		},
	}}
	_, err := c.Dynamic().Resource(podGVR).Namespace(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create pod: %v", err)
	}
	defer c.Delete(ctx, podGVR, ns, name)

	// wait for Running
	ready := false
	for i := 0; i < 30; i++ {
		obj, err := c.Get(ctx, podGVR, ns, name)
		if err == nil && obj != nil {
			phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
			if phase == "Running" {
				ready = true
				break
			}
		}
		time.Sleep(time.Second)
	}
	if !ready {
		t.Fatal("test pod did not become Running within 30s")
	}
	time.Sleep(2 * time.Second)

	logCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	ch, err := c.StreamLogs(logCtx, ns, name, "test", 10)
	if err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}

	found1 := false
	found2 := false
	for line := range ch {
		t.Logf("  log: %s", line)
		if strings.Contains(line, "log-line-1") {
			found1 = true
		}
		if strings.Contains(line, "log-line-2") {
			found2 = true
		}
	}
	if !found1 {
		t.Error("log-line-1 not found in logs")
	}
	if !found2 {
		t.Error("log-line-2 not found in logs")
	}
}
