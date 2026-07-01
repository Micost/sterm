package k8s

import (
	"bufio"
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (c *Client) PodContainers(u *unstructured.Unstructured) []string {
	containers, ok, err := unstructured.NestedSlice(u.Object, "spec", "containers")
	if !ok || err != nil {
		return nil
	}
	var names []string
	for _, c := range containers {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if name, ok := m["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

func (c *Client) StreamLogs(ctx context.Context, ns, podName, container string, tailLines int64) (<-chan string, error) {
	opts := &v1.PodLogOptions{
		Container: container,
		Follow:    true,
		TailLines: &tailLines,
	}

	req := c.typed.CoreV1().Pods(ns).GetLogs(podName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("stream logs: %w", err)
	}

	ch := make(chan string, 100)

	go func() {
		defer stream.Close()
		scanner := bufio.NewScanner(stream)
		for scanner.Scan() {
			line := scanner.Text()
			ch <- line
		}
		close(ch)
	}()

	return ch, nil
}
