package k8s

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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

func (c *Client) RESTConfig() *rest.Config { return c.config }
func (c *Client) Typed() kubernetes.Interface     { return c.typed }
func (c *Client) Dynamic() dynamic.Interface       { return c.dynamic }
func (c *Client) Discovery() discovery.DiscoveryInterface { return c.discovery }

func (c *Client) ForGVR(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return c.dynamic.Resource(gvr)
}
