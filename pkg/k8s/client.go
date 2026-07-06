package k8s

import (
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
)

type Client struct {
	config    *rest.Config
	typed     kubernetes.Interface
	dynamic   dynamic.Interface
	discovery discovery.DiscoveryInterface
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

func (c *Client) ExecTTY(namespace, pod, container string, cmd []string) (io.WriteCloser, io.ReadCloser, error) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	req := c.typed.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(c.config, "POST", req.URL())
	if err != nil {
		stdinW.Close()
		stdoutW.Close()
		stdoutR.Close()
		return nil, nil, err
	}

	go func() {
		defer stdinR.Close()
		defer stdoutW.Close()
		executor.Stream(remotecommand.StreamOptions{
			Stdin:  stdinR,
			Stdout: stdoutW,
			Stderr: stdoutW,
			Tty:    true,
		})
	}()

	return stdinW, stdoutR, nil
}
