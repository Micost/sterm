package k8s

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestListWithFakeClient(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

	pod1 := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]interface{}{"name": "pod-a", "namespace": "default"},
		"status":     map[string]interface{}{"phase": "Running"},
	}}
	pod2 := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]interface{}{"name": "pod-b", "namespace": "default"},
		"status":     map[string]interface{}{"phase": "Pending"},
	}}

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClient(scheme, pod1, pod2)

	c := &Client{dynamic: dynClient}

	data, err := c.List(context.Background(), gvr, "default")
	if err != nil {
		t.Fatalf("List error: %v", err)
	}

	if len(data.Rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(data.Rows))
	}
	if data.Columns[0] != "NAME" {
		t.Errorf("expected NAME column, got %s", data.Columns[0])
	}
}

func TestDeleteWithFakeClient(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

	pod := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]interface{}{"name": "delete-me", "namespace": "default"},
	}}

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClient(scheme, pod)

	c := &Client{dynamic: dynClient}

	err := c.Delete(context.Background(), gvr, "default", "delete-me")
	if err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	_, err = c.Get(context.Background(), gvr, "default", "delete-me")
	if err == nil {
		t.Error("expected not found after delete")
	}
}

func TestListConditionalColumns(t *testing.T) {
	pod := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]interface{}{"name": "my-pod", "namespace": "default"},
		"spec":       map[string]interface{}{"nodeName": "node-1"},
		"status":     map[string]interface{}{"phase": "Running"},
	}}

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClient(scheme, pod)
	c := &Client{dynamic: dynClient}

	data, err := c.List(context.Background(), schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}, "default")
	if err != nil {
		t.Fatalf("List error: %v", err)
	}

	if len(data.Columns) != 6 {
		t.Errorf("pod should have 6 columns (with NODE), got %d: %v", len(data.Columns), data.Columns)
	}
	if data.Columns[5] != "NODE" {
		t.Errorf("expected NODE column, got %s", data.Columns[5])
	}
	if len(data.Rows) > 0 && data.Rows[0].Cells[5] != "node-1" {
		t.Errorf("expected node-1, got %s", data.Rows[0].Cells[5])
	}
}
