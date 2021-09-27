// Copyright 2017 Google Inc.
// Copyright 2020 Acnodal Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	pfc "gitlab.com/acnodal/packet-forwarding-component/src/go/pfc"

	"purelb.io/internal/election"
	"purelb.io/internal/k8s"
	"purelb.io/internal/logging"
)

func main() {
	logger := logging.Init()

	var (
		memberlistNS = flag.String("memberlist-ns", os.Getenv("PURELB_ML_NAMESPACE"), "memberlist namespace (only needed when running outside of k8s)")
		kubeconfig   = flag.String("kubeconfig", os.Getenv("KUBECONFIG"), "absolute path to the kubeconfig file (only needed when running outside of k8s)")
		host         = flag.String("host", os.Getenv("PURELB_HOST"), "HTTP host address for Prometheus metrics")
		myNode       = flag.String("node-name", os.Getenv("PURELB_NODE_NAME"), "name of this Kubernetes node (spec.nodeName)")
		port         = flag.Int("port", 7472, "HTTP listening port for Prometheus metrics")
	)
	flag.Parse()

	if *myNode == "" {
		logger.Log("op", "startup", "error", "must specify --node-name or PURELB_NODE_NAME", "msg", "missing configuration")
		os.Exit(1)
	}

	stopCh := make(chan struct{})
	go func() {
		c1 := make(chan os.Signal)
		signal.Notify(c1, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
		<-c1
		logger.Log("op", "shutdown", "msg", "starting shutdown")
		signal.Stop(c1)
		close(stopCh)
	}()
	defer logger.Log("op", "shutdown", "msg", "done")

	// Set up controller
	ctrl, err := NewController(
		logger,
		*myNode,
		*host,
	)
	if err != nil {
		logger.Log("op", "startup", "error", err, "msg", "failed to create controller")
		os.Exit(1)
	}

	client, err := k8s.New(&k8s.Config{
		ProcessName:   "purelb-lbnodeagent",
		NodeName:      *myNode,
		Logger:        logger,
		Kubeconfig:    *kubeconfig,
		ReadEndpoints: true,
		PollInterval:  30 * time.Second,

		ServiceChanged: ctrl.ServiceChanged,
		ServiceDeleted: ctrl.DeleteBalancer,
		ConfigChanged:  ctrl.SetConfig,
		Shutdown:       ctrl.Shutdown,
	})
	if err != nil {
		logger.Log("op", "startup", "error", err, "msg", "failed to create k8s client")
		os.Exit(1)
	}

	ctrl.SetClient(client)

	election, err := election.New(&election.Config{
		Namespace: *memberlistNS,
		NodeName:  *myNode,
		BindAddr:  os.Getenv("PURELB_HOST"),
		BindPort:  7934,
		Secret:    []byte(os.Getenv("ML_GROUP")),
		Logger:    &logger,
		StopCh:    stopCh,
		Client:    client,
	})
	if err != nil {
		logger.Log("op", "startup", "error", err, "msg", "failed to create election client")
		os.Exit(1)
	}

	ctrl.SetElection(&election)

	iplist, err := client.GetPodsIPs(*memberlistNS)
	if err != nil {
		logger.Log("op", "startup", "error", err, "msg", "failed to get PodsIPs")
		os.Exit(1)
	}
	err = election.Join(iplist)
	if err != nil {
		logger.Log("op", "startup", "error", err, "msg", "failed to join election")
		os.Exit(1)
	}

	go k8s.RunMetrics(*host, *port)

	// See if the PFC is installed
	ok, message := pfc.Check()
	if ok {
		// print the version
		logger.Log("op", "pfc-check", "version", message)
	} else {
		logger.Log("error", "PFC Error", "message", message)
	}

	// the k8s client doesn't return until it's time to shut down
	if err := client.Run(stopCh); err != nil {
		logger.Log("op", "startup", "error", err, "msg", "failed to run k8s client")
	}
}
