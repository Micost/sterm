package k8s

import (
	"context"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func (c *Client) Describe(u *unstructured.Unstructured) string {
	var b strings.Builder

	kind := u.GetKind()
	name := u.GetName()
	ns := u.GetNamespace()
	apiVersion := u.GetAPIVersion()
	labels := u.GetLabels()
	annotations := u.GetAnnotations()

	b.WriteString(fmt.Sprintf("Name:         %s\n", name))
	if ns != "" {
		b.WriteString(fmt.Sprintf("Namespace:    %s\n", ns))
	}
	b.WriteString(fmt.Sprintf("Labels:       "))
	if len(labels) == 0 {
		b.WriteString("<none>\n")
	} else {
		first := true
		keys := sortedKeys(labels)
		for _, k := range keys {
			if !first {
				b.WriteString(fmt.Sprintf("%-*s", len("Labels:       "), ""))
			}
			b.WriteString(fmt.Sprintf("%s=%s\n", k, labels[k]))
			first = false
		}
	}

	b.WriteString(fmt.Sprintf("Annotations:  "))
	if len(annotations) == 0 {
		b.WriteString("<none>\n")
	} else {
		first := true
		keys := sortedKeys(annotations)
		for _, k := range keys {
			if !first {
				b.WriteString(fmt.Sprintf("%-*s", len("Annotations:  "), ""))
			}
			v := annotations[k]
			if len(v) > 80 {
				v = v[:80] + "..."
			}
			b.WriteString(fmt.Sprintf("%s=%s\n", k, v))
			first = false
		}
	}

	b.WriteString(fmt.Sprintf("API Version:  %s\n", apiVersion))
	b.WriteString(fmt.Sprintf("Kind:         %s\n", kind))

	switch kind {
	case "Pod":
		describePod(&b, u)
	case "Service":
		describeService(&b, u)
	case "Node":
		describeNode(&b, u)
	case "Deployment":
		describeDeployment(&b, u)
	default:
		writeGeneric(&b, u)
	}

	// Events
	events := c.fetchEvents(u.GetNamespace(), u.GetName(), u.GetKind(), u.GetUID())
	if len(events) > 0 {
		b.WriteString("\nEvents:\n")
		for _, ev := range events {
			b.WriteString(fmt.Sprintf("  %-10s %-30s %s\n",
				ev["type"], ev["reason"], ev["message"]))
		}
	}

	return b.String()
}

func (c *Client) fetchEvents(ns, name, kind string, uid types.UID) []map[string]string {
	eventGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "events"}
	var list *unstructured.UnstructuredList
	var err error

	if ns != "" {
		list, err = c.dynamic.Resource(eventGVR).Namespace(ns).List(context.Background(), metav1.ListOptions{})
	} else {
		list, err = c.dynamic.Resource(eventGVR).List(context.Background(), metav1.ListOptions{})
	}
	if err != nil {
		return nil
	}

	var out []map[string]string
	for _, item := range list.Items {
		involvedKind, _, _ := unstructured.NestedString(item.Object, "involvedObject", "kind")
		involvedName, _, _ := unstructured.NestedString(item.Object, "involvedObject", "name")
		involvedUID, _, _ := unstructured.NestedString(item.Object, "involvedObject", "uid")

		if involvedKind != kind {
			continue
		}
		// match by UID first, then by name
		if involvedUID != "" && uid != "" && involvedUID == string(uid) {
			// match
		} else if involvedName != name {
			continue
		}

		evType, _, _ := unstructured.NestedString(item.Object, "type")
		reason, _, _ := unstructured.NestedString(item.Object, "reason")
		msg, _, _ := unstructured.NestedString(item.Object, "message")

		if msg != "" {
			out = append(out, map[string]string{
				"type":    evType,
				"reason":  reason,
				"message": msg,
			})
		}
	}
	return out
}

func describePod(b *strings.Builder, u *unstructured.Unstructured) {
	// Node
	node, _, _ := unstructured.NestedString(u.Object, "spec", "nodeName")
	if node != "" {
		b.WriteString(fmt.Sprintf("Node:         %s\n", node))
	}

	// Status
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	b.WriteString(fmt.Sprintf("Status:       %s\n", phase))

	// POD IP
	podIP, _, _ := unstructured.NestedString(u.Object, "status", "podIP")
	if podIP != "" {
		b.WriteString(fmt.Sprintf("IP:           %s\n", podIP))
	}

	// Containers
	describeContainers(b, u)

	// Conditions
	describeConditions(b, u, "status", "conditions")
}

func describeService(b *strings.Builder, u *unstructured.Unstructured) {
	svcType, _, _ := unstructured.NestedString(u.Object, "spec", "type")
	if svcType == "" {
		svcType = "ClusterIP"
	}
	b.WriteString(fmt.Sprintf("Type:         %s\n", svcType))

	clusterIP, _, _ := unstructured.NestedString(u.Object, "spec", "clusterIP")
	if clusterIP != "" {
		b.WriteString(fmt.Sprintf("Cluster IP:   %s\n", clusterIP))
	}

	externalIPs, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "externalIPs")
	if len(externalIPs) > 0 {
		b.WriteString(fmt.Sprintf("External IPs: %s\n", strings.Join(externalIPs, ", ")))
	}

	// Ports
	ports, ok, _ := unstructured.NestedSlice(u.Object, "spec", "ports")
	if ok && len(ports) > 0 {
		b.WriteString("Ports:\n")
		for _, p := range ports {
			pm, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := pm["name"]
			port, _ := pm["port"]
			proto, _ := pm["protocol"]
			target, _ := pm["targetPort"]
			nodePort, _ := pm["nodePort"]
			line := fmt.Sprintf("  %v/%s", port, proto)
			if target != nil {
				line += fmt.Sprintf(" → %v", target)
			}
			if name != nil {
				line = fmt.Sprintf("%-20s %s", fmt.Sprintf("  %v", name), fmt.Sprintf("%v/%s", port, proto))
				b.WriteString(fmt.Sprintf("  %-20s %v/%s", name, port, proto))
				if target != nil {
					b.WriteString(fmt.Sprintf(" → %v", target))
				}
				if nodePort != nil {
					b.WriteString(fmt.Sprintf(" (NodePort: %v)", nodePort))
				}
				b.WriteString("\n")
				continue
			}
			if target != nil {
				line += fmt.Sprintf(" → %v", target)
			}
			if nodePort != nil {
				line += fmt.Sprintf(" (NodePort: %v)", nodePort)
			}
			b.WriteString(line + "\n")
		}
	}

	// Selector
	selector, _, _ := unstructured.NestedStringMap(u.Object, "spec", "selector")
	if len(selector) > 0 {
		b.WriteString(fmt.Sprintf("Selector:     "))
		first := true
		for k, v := range selector {
			if !first {
				b.WriteString(fmt.Sprintf("%-*s", len("Selector:     "), ""))
			}
			b.WriteString(fmt.Sprintf("%s=%s\n", k, v))
			first = false
		}
	}
}

func describeNode(b *strings.Builder, u *unstructured.Unstructured) {
	describeConditions(b, u, "status", "conditions")

	// Capacity
	capacity, ok, _ := unstructured.NestedMap(u.Object, "status", "capacity")
	if ok && len(capacity) > 0 {
		b.WriteString("Capacity:\n")
		for k, v := range capacity {
			b.WriteString(fmt.Sprintf("  %-15s %v\n", k+":", v))
		}
	}

	// Addresses
	addrs, ok, _ := unstructured.NestedSlice(u.Object, "status", "addresses")
	if ok && len(addrs) > 0 {
		b.WriteString("Addresses:\n")
		for _, a := range addrs {
			am, ok := a.(map[string]interface{})
			if !ok {
				continue
			}
			typ, _ := am["type"].(string)
			addr, _ := am["address"].(string)
			b.WriteString(fmt.Sprintf("  %-15s %s\n", typ+":", addr))
		}
	}

	// Taints
	taints, ok, _ := unstructured.NestedSlice(u.Object, "spec", "taints")
	if ok && len(taints) > 0 {
		b.WriteString("Taints:\n")
		for _, t := range taints {
			tm, ok := t.(map[string]interface{})
			if !ok {
				continue
			}
			key, _ := tm["key"].(string)
			value, _ := tm["value"].(string)
			effect, _ := tm["effect"].(string)
			if value != "" {
				b.WriteString(fmt.Sprintf("  %s=%s:%s\n", key, value, effect))
			} else {
				b.WriteString(fmt.Sprintf("  %s:%s\n", key, effect))
			}
		}
	}
}

func describeDeployment(b *strings.Builder, u *unstructured.Unstructured) {
	replicas, _, _ := unstructured.NestedInt64(u.Object, "status", "replicas")
	ready, _, _ := unstructured.NestedInt64(u.Object, "status", "readyReplicas")
	available, _, _ := unstructured.NestedInt64(u.Object, "status", "availableReplicas")
	desired, _, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")

	b.WriteString(fmt.Sprintf("Replicas:     %d desired | %d updated | %d total | %d available | %d unavailable\n",
		desired, replicas, replicas, available, replicas-ready))

	// Strategy
	strategyType, _, _ := unstructured.NestedString(u.Object, "spec", "strategy", "type")
	if strategyType != "" {
		b.WriteString(fmt.Sprintf("Strategy:     %s\n", strategyType))
		if strategyType == "RollingUpdate" {
			maxSurge, _, _ := unstructured.NestedString(u.Object, "spec", "strategy", "rollingUpdate", "maxSurge")
			maxUnavail, _, _ := unstructured.NestedString(u.Object, "spec", "strategy", "rollingUpdate", "maxUnavailable")
			if maxSurge != "" || maxUnavail != "" {
				b.WriteString(fmt.Sprintf("  MaxSurge: %s, MaxUnavailable: %s\n", maxSurge, maxUnavail))
			}
		}
	}

	describeConditions(b, u, "status", "conditions")

	// Selector
	selector, _, _ := unstructured.NestedMap(u.Object, "spec", "selector", "matchLabels")
	if len(selector) == 0 {
		selector, _, _ = unstructured.NestedMap(u.Object, "spec", "selector")
	}
	if s2, _, _ := unstructured.NestedStringMap(u.Object, "spec", "selector", "matchLabels"); len(s2) > 0 {
		b.WriteString("Selector:\n")
		for k, v := range s2 {
			b.WriteString(fmt.Sprintf("  %s=%s\n", k, v))
		}
	}
}

func describeContainers(b *strings.Builder, u *unstructured.Unstructured) {
	containers, ok, _ := unstructured.NestedSlice(u.Object, "spec", "containers")
	if !ok || len(containers) == 0 {
		return
	}
	b.WriteString("Containers:\n")
	for _, c := range containers {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := cm["name"].(string)
		image, _ := cm["image"].(string)
		b.WriteString(fmt.Sprintf("  %s:\n", name))
		b.WriteString(fmt.Sprintf("    Image:      %s\n", image))

		// Ports
		ports, _ := cm["ports"].([]interface{})
		if len(ports) > 0 {
			b.WriteString("    Ports:\n")
			for _, p := range ports {
				pm, _ := p.(map[string]interface{})
				cPort, _ := pm["containerPort"]
				proto, _ := pm["protocol"]
				if proto == "" {
					proto = "TCP"
				}
				b.WriteString(fmt.Sprintf("      %v/%s\n", cPort, proto))
			}
		}

		// Resources
		resources, ok := cm["resources"].(map[string]interface{})
		if ok {
			requests, _ := resources["requests"].(map[string]interface{})
			limits, _ := resources["limits"].(map[string]interface{})
			if len(requests) > 0 || len(limits) > 0 {
				b.WriteString("    Resources:\n")
				if len(requests) > 0 {
					b.WriteString("      Requests:\n")
					for k, v := range requests {
						b.WriteString(fmt.Sprintf("        %s: %v\n", k, v))
					}
				}
				if len(limits) > 0 {
					b.WriteString("      Limits:\n")
					for k, v := range limits {
						b.WriteString(fmt.Sprintf("        %s: %v\n", k, v))
					}
				}
			}
		}

		// Env
		env, _ := cm["env"].([]interface{})
		if len(env) > 0 {
			b.WriteString("    Environment:\n")
			for _, e := range env {
				em, _ := e.(map[string]interface{})
				envName, _ := em["name"].(string)
				if val, ok := em["value"].(string); ok {
					b.WriteString(fmt.Sprintf("      %s: %s\n", envName, val))
				} else if ref, ok := em["valueFrom"].(map[string]interface{}); ok {
					if field, ok := ref["fieldRef"].(map[string]interface{}); ok {
						b.WriteString(fmt.Sprintf("      %s: %v (fieldRef)\n", envName, field["fieldPath"]))
					}
				}
			}
		}

		// Mounts
		mounts, _ := cm["volumeMounts"].([]interface{})
		if len(mounts) > 0 {
			b.WriteString("    Mounts:\n")
			for _, m := range mounts {
				mm, _ := m.(map[string]interface{})
				mountPath, _ := mm["mountPath"].(string)
				mountName, _ := mm["name"].(string)
				b.WriteString(fmt.Sprintf("      %s from %s\n", mountPath, mountName))
			}
		}

		// State
		state := getContainerState(u, name)
		if state != "" {
			b.WriteString(fmt.Sprintf("    State:      %s\n", state))
		}

		// Ready
		readyStr := getContainerReady(u, name)
		if readyStr != "" {
			b.WriteString(fmt.Sprintf("    Ready:      %s\n", readyStr))
		}
	}
}

func getContainerState(u *unstructured.Unstructured, containerName string) string {
	containers, _, _ := unstructured.NestedSlice(u.Object, "status", "containerStatuses")
	for _, c := range containers {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := cm["name"].(string)
		if name != containerName {
			continue
		}
		if running, ok := cm["state"].(map[string]interface{})["running"]; ok && running != nil {
			started, _ := cm["state"].(map[string]interface{})["running"].(map[string]interface{})["startedAt"]
			return fmt.Sprintf("Running (started: %v)", started)
		}
		if waiting, ok := cm["state"].(map[string]interface{})["waiting"]; ok && waiting != nil {
			reason, _ := waiting.(map[string]interface{})["reason"].(string)
			msg, _ := waiting.(map[string]interface{})["message"].(string)
			if msg != "" {
				return fmt.Sprintf("Waiting (%s: %s)", reason, msg)
			}
			return fmt.Sprintf("Waiting (%s)", reason)
		}
		if terminated, ok := cm["state"].(map[string]interface{})["terminated"]; ok && terminated != nil {
			reason, _ := terminated.(map[string]interface{})["reason"].(string)
			exitCode, _ := terminated.(map[string]interface{})["exitCode"]
			return fmt.Sprintf("Terminated (%s, exit code: %v)", reason, exitCode)
		}
	}
	return ""
}

func getContainerReady(u *unstructured.Unstructured, containerName string) string {
	containers, _, _ := unstructured.NestedSlice(u.Object, "status", "containerStatuses")
	for _, c := range containers {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := cm["name"].(string)
		if name != containerName {
			continue
		}
		ready, _, _ := unstructured.NestedBool(cm, "ready")
		restarts, _, _ := unstructured.NestedInt64(cm, "restartCount")
		return fmt.Sprintf("%v (restarts: %d)", ready, restarts)
	}
	return ""
}

func describeConditions(b *strings.Builder, u *unstructured.Unstructured, path ...string) {
	conditions, ok, _ := unstructured.NestedSlice(u.Object, path...)
	if !ok || len(conditions) == 0 {
		return
	}
	b.WriteString("Conditions:\n")
	for _, c := range conditions {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		typ, _ := cm["type"].(string)
		status, _ := cm["status"].(string)
		reason, _ := cm["reason"].(string)
		msg, _ := cm["message"].(string)
		b.WriteString(fmt.Sprintf("  %-25s %-10s", typ, status))
		if reason != "" {
			b.WriteString(fmt.Sprintf(" %s", reason))
		}
		b.WriteString("\n")
		if msg != "" {
			b.WriteString(fmt.Sprintf("    %s\n", msg))
		}
	}
}

func writeGeneric(b *strings.Builder, u *unstructured.Unstructured) {
	writeSection(b, u, "spec", "Spec", 0)
	writeSection(b, u, "status", "Status", 0)
}

func writeSection(b *strings.Builder, u *unstructured.Unstructured, path, title string, depth int) {
	val, ok, err := unstructured.NestedMap(u.Object, path)
	if !ok || err != nil || len(val) == 0 {
		return
	}
	b.WriteString(fmt.Sprintf("%s:\n", title))
	writeMap(b, val, depth+1)
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
				for _, item := range val {
					if m2, ok := item.(map[string]interface{}); ok {
						writeMap(b, m2, indent+1)
						b.WriteString("\n")
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
		case int64:
			b.WriteString(fmt.Sprintf("%s%s: %d\n", prefix, k, val))
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
