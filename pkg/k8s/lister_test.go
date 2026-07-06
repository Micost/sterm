package k8s

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAge(t *testing.T) {
	now := metav1.Now()

	tests := []struct {
		name string
		t    metav1.Time
		want string
	}{
		{"zero", metav1.Time{}, ""},
		{"seconds", metav1.NewTime(now.Add(-30 * time.Second)), "30s"},
		{"minutes", metav1.NewTime(now.Add(-5 * time.Minute)), "5m"},
		{"hours", metav1.NewTime(now.Add(-3 * time.Hour)), "3h0m"},
		{"days", metav1.NewTime(now.Add(-48 * time.Hour)), "2d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := age(tt.t)
			if got != tt.want {
				t.Errorf("age() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractStatus(t *testing.T) {
	// nil
	if got := extractStatus(nil); got != "" {
		t.Errorf("nil obj: got %q, want empty", got)
	}

	// phase
	pod := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{"phase": "Running"},
	}}
	if got := extractStatus(pod); got != "Running" {
		t.Errorf("pod phase: got %q, want Running", got)
	}

	// readyReplicas
	deploy := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{"readyReplicas": int64(2)},
		"spec":   map[string]interface{}{"replicas": int64(3)},
	}}
	if got := extractStatus(deploy); got != "2/3" {
		t.Errorf("deploy ready: got %q, want 2/3", got)
	}

	// availableReplicas
	sts := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{"availableReplicas": int64(1)},
		"spec":   map[string]interface{}{"replicas": int64(1)},
	}}
	if got := extractStatus(sts); got != "1/1" {
		t.Errorf("sts avail: got %q, want 1/1", got)
	}

	// conditions
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready"},
			},
		},
	}}
	if got := extractStatus(obj); got != "Ready" {
		t.Errorf("conditions: got %q, want Ready", got)
	}
}
