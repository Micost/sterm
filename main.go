package main

import (
	"os"

	"github.com/Micost/sterm/pkg/k8s"
	"github.com/Micost/sterm/pkg/tui"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

func main() {
	klog.LogToStderr(false)

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = clientcmd.RecommendedHomeFile
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		config, err = rest.InClusterConfig()
		if err != nil {
			panic("cannot build kubeconfig")
		}
	}

	client, err := k8s.NewClient(config)
	if err != nil {
		panic(err)
	}

	app := tui.NewApp(client)
	if err := app.Run(); err != nil {
		panic(err)
	}
}
