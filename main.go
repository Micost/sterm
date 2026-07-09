package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Micost/sterm/pkg/k8s"
	"github.com/Micost/sterm/pkg/tui"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

func main() {
	showVersion := flag.Bool("version", false, "show version")
	flag.Parse()
	if *showVersion {
		fmt.Printf("sterm %s (commit: %s, built: %s)\n", version, commit, date)
		return
	}
	klog.LogToStderr(false)
	null, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stderr = null

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

	app := tui.NewApp(client, kubeconfig)
	if err := app.Run(); err != nil {
		panic(err)
	}
}
