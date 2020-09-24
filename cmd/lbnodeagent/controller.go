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
	"net"

	"purelb.io/internal/acnodal"
	"purelb.io/internal/election"
	"purelb.io/internal/k8s"
	"purelb.io/internal/lbnodeagent"
	"purelb.io/internal/local"
	purelbv1 "purelb.io/pkg/apis/v1"

	"github.com/go-kit/kit/log"
	v1 "k8s.io/api/core/v1"
)

type controller struct {
	client     k8s.ServiceEvent
	logger     log.Logger
	myNode     string
	announcers []lbnodeagent.Announcer
	svcIP      map[string]net.IP // service name -> assigned IP
}

// NewController configures a new controller. If error is non-nil then
// the controller object shouldn't be used.
func NewController(l log.Logger, myNode string) (*controller, error) {
	con := &controller{
		logger: l,
		myNode: myNode,
		announcers: []lbnodeagent.Announcer{
			local.NewAnnouncer(l, myNode),
			acnodal.NewAnnouncer(l, myNode),
		},
		svcIP: map[string]net.IP{},
	}

	return con, nil
}

// SetClient configures this controller and its announcers to use the
// provided client.
func (c *controller) SetClient(client *k8s.Client) {
	c.client = client
	for _, announcer := range c.announcers {
		announcer.SetClient(client)
	}
}

func (c *controller) ServiceChanged(name string, svc *v1.Service, endpoints *v1.Endpoints) k8s.SyncState {
	defer c.logger.Log("event", "serviceUpdated", "service", name)

	if len(svc.Status.LoadBalancer.Ingress) != 1 {
		return c.deleteBalancer(name, "noIPAllocated")
	}

	lbIP := net.ParseIP(svc.Status.LoadBalancer.Ingress[0].IP)
	if lbIP == nil {
		c.logger.Log("op", "setBalancer", "error", "invalid LoadBalancer IP", svc.Status.LoadBalancer.Ingress[0].IP)
		return c.deleteBalancer(name, "invalidIP")
	}

	// If we didn't allocate the address then we shouldn't announce it.
	if svc.Annotations != nil && svc.Annotations[purelbv1.BrandAnnotation] != purelbv1.Brand {
		c.logger.Log("msg", "notAllocatedByPureLB", "node", c.myNode, "service", name)
		return k8s.SyncStateSuccess
	}

	// give each announcer a chance to announce
	announceError := k8s.SyncStateSuccess
	for _, announcer := range c.announcers {
		if err := announcer.SetBalancer(name, svc, endpoints); err != nil {
			c.logger.Log("op", "setBalancer", "error", err, "msg", "failed to announce service")
			announceError = k8s.SyncStateError
		}
	}

	c.logger.Log("event", "serviceAnnounced", "node", c.myNode, "msg", "service has IP, announcing")

	c.svcIP[name] = lbIP

	return announceError
}

func (c *controller) DeleteBalancer(name string) k8s.SyncState {
	return c.deleteBalancer(name, "cluster event")
}

func (c *controller) deleteBalancer(name, reason string) k8s.SyncState {
	retval := k8s.SyncStateSuccess

	for _, announcer := range c.announcers {
		if err := announcer.DeleteBalancer(name, reason); err != nil {
			c.logger.Log("op", "deleteBalancer", "error", err, "msg", "failed to clear balancer state")
			retval = k8s.SyncStateError
		}
	}

	delete(c.svcIP, name)
	// Spamming the log, temporatly removed.
	// c.logger.Log("event", "serviceWithdrawn", "ip", c.svcIP[name], "reason", reason, "msg", "withdrawing service announcement")

	return retval
}

func (c *controller) SetConfig(cfg *purelbv1.Config) k8s.SyncState {
	c.logger.Log("op", "setConfig")

	retval := k8s.SyncStateReprocessAll

	for _, announcer := range c.announcers {
		if err := announcer.SetConfig(cfg); err != nil {
			c.logger.Log("op", "setConfig", "error", err)
			retval = k8s.SyncStateError
		}
	}

	return retval
}

func (c *controller) SetElection(election *election.Election) {
	for _, announcer := range c.announcers {
		announcer.SetElection(election)
	}
}

func (c *controller) Shutdown() {
	for _, announcer := range c.announcers {
		announcer.Shutdown()
	}
}