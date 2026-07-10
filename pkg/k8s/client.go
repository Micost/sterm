package k8s

import (
	"io"
	"os/exec"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
)

type Client struct {
	config    *rest.Config
	typed     kubernetes.Interface
	dynamic   dynamic.Interface
	discovery discovery.DiscoveryInterface
}

type ContextInfo struct {
	Name    string
	Cluster string
	User    string
}

func ListContexts(kubeconfig string) ([]ContextInfo, error) {
	if kubeconfig == "" {
		kubeconfig = clientcmd.RecommendedHomeFile
	}
	cfg, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		return nil, err
	}
	var out []ContextInfo
	for name, ctx := range cfg.Contexts {
		out = append(out, ContextInfo{
			Name:    name,
			Cluster: ctx.Cluster,
			User:    ctx.AuthInfo,
		})
	}
	return out, nil
}

func BuildConfigForContext(kubeconfig, contextName string) (*rest.Config, error) {
	if kubeconfig == "" {
		kubeconfig = clientcmd.RecommendedHomeFile
	}
	loader := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loader, overrides).ClientConfig()
}

func NewClient(config *rest.Config) (*Client, error) {
	typed, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	disc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}

	return &Client{
		config:    config,
		typed:     typed,
		dynamic:   dyn,
		discovery: disc,
	}, nil
}

func (c *Client) RESTConfig() *rest.Config                    { return c.config }
func (c *Client) Typed() kubernetes.Interface                 { return c.typed }
func (c *Client) Dynamic() dynamic.Interface                  { return c.dynamic }
func (c *Client) Discovery() discovery.DiscoveryInterface     { return c.discovery }

func (c *Client) ForGVR(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return c.dynamic.Resource(gvr)
}

func (c *Client) Exec(namespace, pod, container string, cmd []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	req := c.typed.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdin:     stdin != nil,
			Stdout:    stdout != nil,
			Stderr:    stderr != nil,
			TTY:       false,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(c.config, "POST", req.URL())
	if err != nil {
		return err
	}

	return executor.Stream(remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Tty:    false,
	})
}

func (c *Client) CordonNode(name string) error {
	return exec.Command("kubectl", "cordon", name).Run()
}

func (c *Client) UncordonNode(name string) error {
	return exec.Command("kubectl", "uncordon", name).Run()
}
