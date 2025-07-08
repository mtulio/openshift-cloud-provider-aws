/*
Copyright 2018 The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
   http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eservice "k8s.io/kubernetes/test/e2e/framework/service"
	imageutils "k8s.io/kubernetes/test/utils/image"
	admissionapi "k8s.io/pod-security-admission/api"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

const (
	annotationLBType             = "service.beta.kubernetes.io/aws-load-balancer-type"
	annotationLBInternal         = "service.beta.kubernetes.io/aws-load-balancer-internal"
	annotationLBTargetNodeLabels = "service.beta.kubernetes.io/aws-load-balancer-target-node-labels"
	annotationLBSecurityGroups   = "service.beta.kubernetes.io/aws-load-balancer-security-groups"
	nlbSecurityGroupModeManaged  = "Managed"
)

var (
	// lookupNodeSelectors are valid compute/node/worker selectors commonly used in different kubernetes
	// distributions.
	lookupNodeSelectors = []string{
		"node-role.kubernetes.io/worker", // used in must distributions
		"node-role.kubernetes.io/node",   // used in ccm-aws CI
	}
)

// loadbalancer tests
var _ = Describe("[cloud-provider-aws-e2e] loadbalancer", func() {
	f := framework.NewDefaultFramework("cloud-provider-aws")
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	var (
		cs clientset.Interface
		ns *v1.Namespace
	)

	BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
	})

	AfterEach(func() {
		// After each test
	})

	type loadBalancerTestCases struct {
		// Overall test case configuration.
		name             string
		resourceSuffix   string
		extraAnnotations map[string]string
		listenerCount    int

		// Hooks
		// HookPostServiceConfig hook runs after the service manifest is created, and before the service is created.
		hookPostServiceConfig func(cfg *e2eTestConfig)
		// HookPostServiceCreate hook runs after the test is run.
		hookPostServiceCreate func(cfg *e2eTestConfig)
		// HookPreTest hook runs before the test is run.
		hookPreTest func(cfg *e2eTestConfig)

		// Flags to override default test behavior.
		overrideTestRunInClusterReachableHTTP bool
		requireAffinity                       bool

		// Test verification
		skipTestFailure bool
	}
	cases := []loadBalancerTestCases{
		{
			name:             "CLB should be reachable with default configurations",
			resourceSuffix:   "",
			extraAnnotations: map[string]string{},
		},
		{
			name:             "NLB should be reachable with default configurations",
			resourceSuffix:   "nlb",
			extraAnnotations: map[string]string{annotationLBType: "nlb"},
		},
		{
			name:             "NLB should be reachable with target-node-labels",
			resourceSuffix:   "sg-nd",
			extraAnnotations: map[string]string{annotationLBType: "nlb"},
			hookPostServiceConfig: func(cfg *e2eTestConfig) {
				framework.Logf("running hook post-service-config patching service annotations to test node label selector")
				cfg.discoverClusterWorkerNode()

				if cfg.svc.Annotations == nil {
					cfg.svc.Annotations = map[string]string{}
				}
				cfg.svc.Annotations[annotationLBTargetNodeLabels] = cfg.nodeSelector
			},
			hookPostServiceCreate: func(cfg *e2eTestConfig) {
				framework.Logf("running hook post-service-create to validate the number of targets in the load balancer selected")
				if len(cfg.svc.Status.LoadBalancer.Ingress) == 0 {
					framework.Failf("No ingress found in LoadBalancer status for service %s/%s", cfg.svc.Namespace, cfg.svc.Name)
				}
				lbDNS := cfg.svc.Status.LoadBalancer.Ingress[0].Hostname
				framework.ExpectNoError(getLBTargetCount(cfg.ctx, lbDNS, cfg.nodeCount), "AWS LB target count validation failed")
			},
		},
		// Hairpining traffic test for CLB.
		{
			name:           "CLB internal should be reachable with hairpinning traffic",
			resourceSuffix: "hp-clb-int",
			extraAnnotations: map[string]string{
				annotationLBInternal: "true",
			},
			hookPostServiceConfig: func(cfg *e2eTestConfig) {
				framework.Logf("running hook post-service-config")
				cfg.discoverClusterWorkerNode()
				framework.Logf("patching service annotations to enforce LB pins/selects target to a single node: kubernetes.io/hostname=%s", cfg.nodeSingleSample)

				if cfg.svc.Annotations == nil {
					cfg.svc.Annotations = map[string]string{}
				}
				cfg.svc.Annotations[annotationLBTargetNodeLabels] = fmt.Sprintf("kubernetes.io/hostname=%s", cfg.nodeSingleSample)
			},
			overrideTestRunInClusterReachableHTTP: true,
			requireAffinity:                       true,
		},
		// Hairpining traffic test for NLB.
		// Hairpin connection work with target type as instance only when preserve client IP is disabled.
		// Currently CCM does not provide an interface to create a service with that setup, making an internal
		// Service to fail.
		// FIXME: https://github.com/kubernetes/cloud-provider-aws/issues/1160
		// Once issue 1160 is fixed, the skipTestFailure must be unset/false.
		{
			name:           "NLB internal should be reachable with hairpinning traffic",
			resourceSuffix: "hp-nlb-int",
			extraAnnotations: map[string]string{
				annotationLBType:     "nlb",
				annotationLBInternal: "true",
			},
			listenerCount: 1,
			hookPostServiceConfig: func(cfg *e2eTestConfig) {
				framework.Logf("running hook post-service-config")
				cfg.discoverClusterWorkerNode()
				framework.Logf("patching service annotations to enforce LB pins/selects target to a single node: kubernetes.io/hostname=%s", cfg.nodeSingleSample)

				if cfg.svc.Annotations == nil {
					cfg.svc.Annotations = map[string]string{}
				}
				cfg.svc.Annotations[annotationLBTargetNodeLabels] = fmt.Sprintf("kubernetes.io/hostname=%s", cfg.nodeSingleSample)
			},
			overrideTestRunInClusterReachableHTTP: true,
			requireAffinity:                       true,
			skipTestFailure:                       true,
		},
		// NLB with BYO Security Group annotation should use provided security group
		{
			name:           "NLB with BYO Security Group annotation should use provided security group",
			resourceSuffix: "nlb-byo-sg",
			extraAnnotations: map[string]string{
				annotationLBType: "nlb",
			},
			hookPostServiceConfig: func(cfg *e2eTestConfig) {
				framework.Logf("Creating a test security group for BYO SG scenario")

				securityGroupName := "test-byo-sg-" + cfg.svc.Name
				byoSGID, err := cfg.awsHelper.createSecurityGroup(securityGroupName, "BYO Security Group for e2e test")
				framework.ExpectNoError(err, "Failed to create BYO security group")

				// Store the SG ID for cleanup
				cfg.byoSecurityGroupID = byoSGID

				// Add the BYO security group annotation to the service
				if cfg.svc.Annotations == nil {
					cfg.svc.Annotations = make(map[string]string)
				}
				cfg.svc.Annotations[annotationLBSecurityGroups] = byoSGID

				framework.Logf("Created BYO security group %s for service %s", byoSGID, cfg.svc.Name)
			},
			hookPostServiceCreate: func(cfg *e2eTestConfig) {
				if len(cfg.svc.Status.LoadBalancer.Ingress) == 0 {
					framework.Failf("No ingress found in LoadBalancer status for service %s/%s", cfg.svc.Namespace, cfg.svc.Name)
				}
				lbDNS := cfg.svc.Status.LoadBalancer.Ingress[0].Hostname

				// Verify that the NLB is using the BYO security group
				securityGroups, err := cfg.awsHelper.getLoadBalancerSecurityGroups(lbDNS)
				framework.ExpectNoError(err, "Failed to get load balancer security groups")

				framework.Logf("Load balancer %s has security groups: %v", lbDNS, securityGroups)

				// Verify the BYO security group is attached
				byoSGFound := false
				for _, sgID := range securityGroups {
					if sgID == cfg.byoSecurityGroupID {
						byoSGFound = true
						break
					}
				}

				if !byoSGFound {
					framework.Failf("BYO security group %s not found in load balancer security groups %v", cfg.byoSecurityGroupID, securityGroups)
				}
				framework.Logf("BYO security group %s found in load balancer security groups %v", cfg.byoSecurityGroupID, securityGroups)

				// Verify no managed security groups were created (only BYO should be present)
				for _, sgID := range securityGroups {
					isManaged, err := cfg.awsHelper.isSecurityGroupManaged(sgID)
					framework.ExpectNoError(err, "Failed to check if security group is managed")

					if isManaged && sgID != cfg.byoSecurityGroupID {
						framework.Failf("Found unexpected managed security group %s when BYO was specified", sgID)
					}
				}
				framework.Logf("BYO security group validation passed using SG: %s", cfg.byoSecurityGroupID)

				// Verify that the controller creates the inbound security group rules on BYO SG
				ports := make([]int32, len(cfg.svc.Spec.Ports))
				for i, port := range cfg.svc.Spec.Ports {
					ports[i] = port.Port
				}
				err = cfg.awsHelper.validateSecurityGroupRules(cfg.byoSecurityGroupID, ports)
				framework.ExpectNoError(err, "Failed to validate security group rules")
				framework.Logf("Rules validation for BYO Security group passed using SG: %s", cfg.byoSecurityGroupID)
			},
		},
	}

	serviceNameBase := "lbconfig-test"
	for _, tc := range cases {
		It(tc.name, func() {
			By("setting up test environment")
			framework.Logf("[SETUP] Test case: %s", tc.name)
			e2e := newE2eTestConfig(cs)
			defer e2e.cleanup()

			loadBalancerCreateTimeout := e2eservice.GetServiceLoadBalancerCreationTimeout(cs)
			framework.Logf("[CONFIG] AWS load balancer timeout: %s", loadBalancerCreateTimeout)

			By("building service configuration with annotations")
			serviceName := serviceNameBase
			if len(tc.resourceSuffix) > 0 {
				serviceName = serviceName + "-" + tc.resourceSuffix
			}
			framework.Logf("[CONFIG] Service name: %s, namespace: %s", serviceName, ns.Name)
			e2e.LBJig = e2eservice.NewTestJig(cs, ns.Name, serviceName)

			// Hook annotations to support dynamic config
			e2e.svc = e2e.buildService(tc.listenerCount, tc.extraAnnotations)
			framework.Logf("[CONFIG] Service ports: %d, extra annotations: %v", len(e2e.svc.Spec.Ports), tc.extraAnnotations)

			if tc.hookPostServiceConfig != nil {
				By("executing hook post-service-config: applying service configuration")
				framework.Logf("[HOOK] Executing post-service-config hook")
				tc.hookPostServiceConfig(e2e)
				framework.Logf("[HOOK] Final service annotations: %v", e2e.svc.Annotations)
			}

			By("creating LoadBalancer service in Kubernetes")
			if _, err := e2e.LBJig.Client.CoreV1().Services(e2e.LBJig.Namespace).Create(context.TODO(), e2e.svc, metav1.CreateOptions{}); err != nil {
				framework.ExpectNoError(fmt.Errorf("failed to create LoadBalancer Service %q: %v", e2e.svc.Name, err))
			}
			framework.Logf("[K8S] LoadBalancer service created successfully")

			By("waiting for AWS load balancer provisioning")
			var err error
			e2e.svc, err = e2e.LBJig.WaitForLoadBalancer(loadBalancerCreateTimeout)
			if err != nil {
				framework.Logf("Error waiting for loadbalancer for service %q: %v", e2e.svc.Name, err)
				collectEventsFromResource(e2e.ctx, e2e.kubeClient, e2e.LBJig.Namespace, e2e.LBJig.Name)
			}
			framework.ExpectNoError(err)

			By("creating backend server pods")
			_, err = e2e.LBJig.Run(e2e.buildReplicationController(tc.requireAffinity))
			framework.ExpectNoError(err)
			framework.Logf("[K8S] Backend pods created, affinity required: %t", tc.requireAffinity)

			if tc.hookPostServiceCreate != nil {
				By("executing hook post-service-create: applying service configuration")
				tc.hookPostServiceCreate(e2e)
			}

			By("collecting service and load balancer information")
			if len(e2e.svc.Spec.Ports) == 0 {
				framework.Failf("No ports found in service spec for service %s/%s", e2e.svc.Namespace, e2e.svc.Name)
			}
			if len(e2e.svc.Status.LoadBalancer.Ingress) == 0 {
				framework.Failf("No ingress found in LoadBalancer status for service %s/%s", e2e.svc.Namespace, e2e.svc.Name)
			}
			svcPort := int(e2e.svc.Spec.Ports[0].Port)
			ingressAddress := e2eservice.GetIngressPoint(&e2e.svc.Status.LoadBalancer.Ingress[0])
			framework.Logf("[LB-INFO] Ingress address: %s, port: %d", ingressAddress, svcPort)

			if tc.hookPreTest != nil {
				By("executing pre-test hook")
				tc.hookPreTest(e2e)
			}

			// overrideTestRunInClusterReachableHTTP changes the default test function to run the client in the cluster.
			if tc.overrideTestRunInClusterReachableHTTP {
				By("testing HTTP connectivity from internal network")
				framework.Logf("[TEST] Running internal connectivity test from node: %s", e2e.nodeSingleSample)
				err := inClusterTestReachableHTTP(cs, ns.Name, e2e.nodeSingleSample, ingressAddress, svcPort)
				if err != nil && tc.skipTestFailure {
					Skip(err.Error())
				}
				framework.ExpectNoError(err)
			} else {
				By("testing HTTP connectivity from external client")
				framework.Logf("[TEST] Running external connectivity test to %s:%d", ingressAddress, svcPort)
				e2eservice.TestReachableHTTP(ingressAddress, svcPort, e2eservice.LoadBalancerLagTimeoutAWS)
			}
			framework.Logf("[TEST] HTTP connectivity test completed successfully")

			// Update the service to cluster IP
			By("cleaning up: converting service to ClusterIP")
			_, err = e2e.LBJig.UpdateService(func(s *v1.Service) {
				s.Spec.Type = v1.ServiceTypeClusterIP
			})
			framework.ExpectNoError(err)

			// Wait for the load balancer to be destroyed asynchronously
			By("cleaning up: waiting for load balancer destruction")
			framework.Logf("[CLEANUP] Waiting for load balancer destruction")
			_, err = e2e.LBJig.WaitForLoadBalancerDestroy(ingressAddress, svcPort, loadBalancerCreateTimeout)
			framework.ExpectNoError(err)
			framework.Logf("[CLEANUP] Load balancer destroyed successfully")
		})
	}
})

type e2eTestConfig struct {
	ctx        context.Context
	kubeClient clientset.Interface

	// AWS helper
	awsHelper *awsHelper

	// AWS resources subject to cleanup
	byoSecurityGroupID string

	// service configuration
	cfgPortCount          int
	cfgPodPort            uint16
	cfgPodProtocol        v1.Protocol
	cfgDefaultAnnotations map[string]string
	LBJig                 *e2eservice.TestJig

	// service instance
	svc *v1.Service

	// node discovery
	nodeSelector     string
	nodeCount        int
	nodeSingleSample string
}

func newE2eTestConfig(cs clientset.Interface) *e2eTestConfig {
	// Create a context with a reasonable timeout for e2e tests
	// E2E tests can take several minutes for load balancer provisioning and configuration
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	_ = cancel // We'll let the test framework handle cleanup

	h, err := newAWSHelper(ctx, cs)
	framework.ExpectNoError(err, "Failed to create AWS helper")

	return &e2eTestConfig{
		kubeClient:     cs,
		cfgPortCount:   2,
		ctx:            ctx,
		cfgPodPort:     8080,
		cfgPodProtocol: v1.ProtocolTCP,
		cfgDefaultAnnotations: map[string]string{
			"aws-load-balancer-backend-protocol": "http",
			"aws-load-balancer-ssl-ports":        "https",
		},
		awsHelper: h,
	}
}

func (e *e2eTestConfig) cleanup() {
	framework.Logf("Cleaning up e2e resources")
	// Cleanup security group
	if e.awsHelper != nil && e.byoSecurityGroupID != "" {
		framework.Logf("Cleaning up security group %s", e.byoSecurityGroupID)
		err := e.awsHelper.waitForSecurityGroupDeletion(e.byoSecurityGroupID, 5*time.Minute)
		if err != nil {
			framework.Logf("Failed to delete security group %s during cleanup: %v", e.byoSecurityGroupID, err)
		} else {
			framework.Logf("Successfully cleaned up security group %s", e.byoSecurityGroupID)
		}
	}
}

// buildService creates a service instance with custom annotations.
func (e2e *e2eTestConfig) buildService(portCount int, extraAnnotations map[string]string) *v1.Service {
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   e2e.LBJig.Namespace,
			Name:        e2e.LBJig.Name,
			Labels:      e2e.LBJig.Labels,
			Annotations: make(map[string]string, len(e2e.cfgDefaultAnnotations)+len(extraAnnotations)),
		},
		Spec: v1.ServiceSpec{
			Type:            v1.ServiceTypeLoadBalancer,
			SessionAffinity: v1.ServiceAffinityNone,
			Selector:        e2e.LBJig.Labels,
		},
	}
	if portCount == 0 {
		portCount = e2e.cfgPortCount
	}
	for i := 0; i < portCount; i++ {
		svc.Spec.Ports = append(svc.Spec.Ports, v1.ServicePort{
			Name:       fmt.Sprintf("port-%d", i),
			Protocol:   v1.ProtocolTCP,
			Port:       int32(80 + i),
			TargetPort: intstr.FromInt(int(e2e.cfgPodPort)),
		})
	}

	// add default annotations - can be overriden by extra annotations
	for aK, aV := range e2e.cfgDefaultAnnotations {
		svc.Annotations[aK] = aV
	}

	// append test case annotations to the service
	for aK, aV := range extraAnnotations {
		svc.Annotations[aK] = aV
	}

	// Defensive: ensure Annotations is not nil
	if svc.Annotations == nil {
		svc.Annotations = map[string]string{}
	}

	return svc
}

// buildReplicationController creates a replication controller wrapper for the test framework.
// buildReplicationController is based on newRCTemplate() from the e2e test framework, which not provide
// customization to bind in non-privileged ports.
// TODO(mtulio): v1.33+[2][3] moved from RC to Deployments on tests, we must do the same to use Run()
// when the test framework is updated.
// [1] https://github.com/kubernetes/kubernetes/blob/89d95c9713a8fd189e8ad555120838b3c4f888d1/test/e2e/framework/service/jig.go#L636
// [2] https://github.com/kubernetes/kubernetes/issues/119021
// [3] https://github.com/kubernetes/cloud-provider-aws/blob/master/tests/e2e/go.mod#L14
func (e2e *e2eTestConfig) buildReplicationController(affinity bool) func(rc *v1.ReplicationController) {
	return func(rc *v1.ReplicationController) {
		var replicas int32 = 1
		var grace int64 = 3
		rc.ObjectMeta = metav1.ObjectMeta{
			Namespace: e2e.LBJig.Namespace,
			Name:      e2e.LBJig.Name,
			Labels:    e2e.LBJig.Labels,
		}
		rc.Spec = v1.ReplicationControllerSpec{
			Replicas: &replicas,
			Selector: e2e.LBJig.Labels,
			Template: &v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: e2e.LBJig.Labels,
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "netexec",
							Image: imageutils.GetE2EImage(imageutils.Agnhost),
							Args: []string{
								"netexec",
								fmt.Sprintf("--http-port=%d", e2e.cfgPodPort),
								fmt.Sprintf("--udp-port=%d", e2e.cfgPodPort),
							},
							ReadinessProbe: &v1.Probe{
								PeriodSeconds: 3,
								ProbeHandler: v1.ProbeHandler{
									HTTPGet: &v1.HTTPGetAction{
										Port: intstr.FromInt(int(e2e.cfgPodPort)),
										Path: "/hostName",
									},
								},
							},
						},
					},
					TerminationGracePeriodSeconds: &grace,
				},
			},
		}
		if affinity {
			rc.Spec.Template.Spec.Affinity = &v1.Affinity{
				NodeAffinity: &v1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{
							{
								MatchExpressions: []v1.NodeSelectorRequirement{
									{
										Key:      "kubernetes.io/hostname",
										Operator: v1.NodeSelectorOpIn,
										Values:   []string{e2e.nodeSingleSample},
									},
								},
							},
						},
					},
				},
			}
		}
	}
}

// discoverClusterWorkerNode identifies and selects worker nodes in the cluster based on predefined node label selectors.
// It returns a ClusterNodeDiscovery struct with the discovered information.
func (e2e *e2eTestConfig) discoverClusterWorkerNode() {
	var workerNodeList []string
	framework.Logf("discovering node label used in the kubernetes distributions")
	for _, selector := range lookupNodeSelectors {
		nodeList, err := e2e.kubeClient.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{
			LabelSelector: selector,
		})
		framework.ExpectNoError(err, "failed to list worker nodes")
		if len(nodeList.Items) > 0 {
			for _, node := range nodeList.Items {
				workerNodeList = append(workerNodeList, node.Name)
			}
			// Save the first worker node in the list to be used in cases.
			sort.Strings(workerNodeList)
			e2e.nodeCount = len(nodeList.Items)
			e2e.nodeSingleSample = workerNodeList[0]
			e2e.nodeSelector = selector
			framework.Logf("[SETUP] Worker nodes discovered: %d nodes, selector: %s, sample node: %s", e2e.nodeCount, e2e.nodeSelector, e2e.nodeSingleSample)
			return
		}
	}
	framework.ExpectNoError(fmt.Errorf("unable to find node selector for %v", lookupNodeSelectors))
}

// getLBTargetCount verifies the number of registered targets for a given LBv2 DNS name matches the expected count.
// The steps includes:
// - Get Load Balancer ARN from DNS name extracted from service Status.LoadBalancer.Ingress[0].Hostname
// - List listeners for the load balancer
// - Get target groups attached to listeners
// - Count registered targets in target groups
// - Verify count matches number of worker nodes
func getLBTargetCount(ctx context.Context, lbDNSName string, expectedTargets int) error {
	// Load AWS config
	elbClient, err := getAWSClientLoadBalancer(ctx)
	if err != nil {
		return fmt.Errorf("unable to create AWS client: %v", err)
	}

	// Get Load Balancer ARN from DNS name
	foundLB, err := getAWSLoadBalancerFromDNSName(ctx, elbClient, lbDNSName)
	if err != nil {
		return fmt.Errorf("failed to get load balancer from DNS name: %v", err)
	}
	lbARN := aws.ToString(foundLB.LoadBalancerArn)

	// List listeners for the load balancer
	listenersOut, err := elbClient.DescribeListeners(ctx, &elbv2.DescribeListenersInput{
		LoadBalancerArn: aws.String(lbARN),
	})
	if err != nil {
		return fmt.Errorf("failed to describe listeners: %v", err)
	}

	// Get target groups attached to listeners
	targetGroupARNs := map[string]struct{}{}
	for _, listener := range listenersOut.Listeners {
		if len(targetGroupARNs) > 0 {
			break
		}
		for _, action := range listener.DefaultActions {
			if action.TargetGroupArn != nil {
				targetGroupARNs[aws.ToString(action.TargetGroupArn)] = struct{}{}
				break
			}
		}
	}
	if len(targetGroupARNs) == 0 {
		return fmt.Errorf("no target groups found for LB: %s", lbARN)
	}

	// Count registered targets in target groups
	totalTargets := 0
	for tgARN := range targetGroupARNs {
		tgHealth, err := elbClient.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{
			TargetGroupArn: aws.String(tgARN),
		})
		if err != nil {
			return fmt.Errorf("failed to describe target health for TG %s: %v", tgARN, err)
		}
		totalTargets += len(tgHealth.TargetHealthDescriptions)
	}

	// Verify count matches number of worker nodes
	if totalTargets != expectedTargets {
		return fmt.Errorf("target count mismatch: expected %d, got %d", expectedTargets, totalTargets)
	}
	return nil
}

// AWS helpers
func getAWSClientLoadBalancer(ctx context.Context) (*elbv2.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %v", err)
	}
	return elbv2.NewFromConfig(cfg), nil
}

func getAWSLoadBalancerFromDNSName(ctx context.Context, elbClient *elbv2.Client, lbDNSName string) (*elbv2types.LoadBalancer, error) {
	var foundLB *elbv2types.LoadBalancer
	framework.Logf("describing load balancers with DNS %s", lbDNSName)

	paginator := elbv2.NewDescribeLoadBalancersPaginator(elbClient, &elbv2.DescribeLoadBalancersInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		framework.ExpectNoError(err)

		framework.Logf("found %d load balancers", len(page.LoadBalancers))
		// Search for the load balancer with matching DNS name in this page
		for i := range page.LoadBalancers {
			if aws.ToString(page.LoadBalancers[i].DNSName) == lbDNSName {
				foundLB = &page.LoadBalancers[i]
				framework.Logf("found load balancer with DNS %s", aws.ToString(foundLB.DNSName))
				break
			}
		}
		if foundLB != nil {
			break
		}
	}

	if foundLB == nil {
		framework.Failf("No load balancer found with DNS name: %s", lbDNSName)
	}

	return foundLB, nil
}

// inClusterTestReachableHTTP creates a pod within the cluster to test HTTP connectivity to a target IP and port.
// It schedules the pod on the specified node using node affinity to test the hairpin scenario.
// The pod uses a curl-based container to perform the HTTP request and validates the response.
// The function waits for the pod to complete its execution and inspects its exit code to determine success or failure.
//
// Parameters:
// - cs: Kubernetes clientset interface used to interact with the cluster.
// - namespace: The namespace in which the test pod will be created.
// - nodeName: The name of the node where the test pod should be scheduled.
// - target: The IP address or Hostname of the target HTTP server.
// - targetPort: The port number of the target HTTP server.
//
// Returns:
// - error: Returns an error if the pod creation, execution, or cleanup fails, or if the HTTP test fails unexpectedly.
//
// Behavior:
// - The function creates a pod with a curl-based container to perform the HTTP request.
// - It configures the pod to run as a non-root user with security settings.
// - The pod is scheduled on the specified node using node affinity.
// - Logs are periodically collected during the pod's execution for troubleshooting.
// - Events are inspected if the pod remains in a pending state for too long.
// - The function waits for the pod to complete and inspects its exit code to determine success or failure.
// - If the pod fails, an error is returned.
// - The pod is cleaned up after the test completes.
func inClusterTestReachableHTTP(cs clientset.Interface, namespace, nodeName, target string, targetPort int) error {
	podName := "http-test-pod"

	// client http test (curl) pod spec.
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    "curl",
					Image:   imageutils.GetE2EImage(imageutils.Agnhost),
					Command: []string{"curl"},
					Args: []string{
						"--retry", "15", // Retry up to 15 times in case of transient network issues.
						"--retry-delay", "20", // Wait 20 seconds between retries.
						"--retry-max-time", "480", // Maximum time for retries is 480 seconds.
						"--retry-all-errors",                                                       // Retry on all errors, ensuring robustness against temporary failures.
						"--trace-time",                                                             // Include timestamps in trace output for debugging.
						"-w", "\\\"\\n---> HTTPCode=%{http_code} Time=%{time_total}ms <---\\n\\\"", // Format output to include HTTP code and response time.
						fmt.Sprintf("http://%s:%d/echo?msg=hello", target, targetPort),
					},
				},
			},
			SecurityContext: &v1.PodSecurityContext{
				RunAsNonRoot: aws.Bool(true),  // Ensures the pod runs as a non-root user for enhanced security.
				RunAsUser:    aws.Int64(1000), // Specifies the user ID for the container process.
				RunAsGroup:   aws.Int64(1000), // Specifies the group ID for the container process.
				SeccompProfile: &v1.SeccompProfile{
					Type: v1.SeccompProfileTypeRuntimeDefault, // Enforces runtime default seccomp profile for syscall filtering.
				},
			},
			RestartPolicy: v1.RestartPolicyNever, // Prevents the pod from restarting automatically.
			Affinity: &v1.Affinity{
				NodeAffinity: &v1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{
							{
								MatchExpressions: []v1.NodeSelectorRequirement{
									{
										Key:      "kubernetes.io/hostname",
										Operator: v1.NodeSelectorOpIn,
										Values:   []string{nodeName}, // Ensures the pod is scheduled on the specified node.
									},
								},
							},
						},
					},
				},
			},
		},
	}
	ct := pod.Spec.Containers[0]
	framework.Logf("In-Cluster test PodSpec Image=%v Command=%v Args=%v", ct.Image, ct.Command, ct.Args)

	// Create the pod
	_, err := cs.CoreV1().Pods(namespace).Create(context.TODO(), pod, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create HTTP test pod: %v", err)
	}
	// Clean up the pod
	defer func() {
		err = cs.CoreV1().Pods(namespace).Delete(context.TODO(), podName, metav1.DeleteOptions{})
		if err != nil {
			framework.Logf("Failed to delete pod %s: %v", podName, err)
		}
	}()

	// Pod logs wrapper. Collect recent logs, or all, from a test pod.
	gatherLogs := func(tail int) string {
		opts := &v1.PodLogOptions{}
		if tail == 0 {
			tail = 20
		}
		opts.TailLines = aws.Int64(int64(tail))
		logs, errL := cs.CoreV1().Pods(namespace).GetLogs(podName, opts).DoRaw(context.TODO())
		if errL != nil {
			framework.Logf("Failed to retrieve pod logs: %v", errL)
			return ""
		}
		return string(logs)
	}

	// Wait for the test pod to complete. Limit waiter be higher than curl retries.
	waitCount := 0
	pendingCount := 0
	err = wait.PollImmediate(15*time.Second, 15*time.Minute, func() (bool, error) {
		p, err := cs.CoreV1().Pods(namespace).Get(context.TODO(), podName, metav1.GetOptions{})
		if err != nil {
			framework.Logf("Error getting pod %s: %v", podName, err)
			return false, err
		}
		framework.Logf("Pod %s status: Phase=%s", podName, p.Status.Phase)
		podFinished := p.Status.Phase == v1.PodSucceeded || p.Status.Phase == v1.PodFailed

		// Troubleshoot pending pods
		if p.Status.Phase == v1.PodPending {
			pendingCount++
		}
		if pendingCount%10 == 0 && pendingCount > 0 {
			framework.Logf("Pod %s is pending for too long, checking events...", podName)
			collectEventsFromResource(context.TODO(), cs, namespace, podName)
		}
		// frequently collect logs.
		if waitCount > 0 && waitCount%4 == 0 {
			framework.Logf("Tail logs for HTTP test pod:\n%s", gatherLogs(5))
		}
		if podFinished {
			framework.Logf("Tail logs for HTTP test pod:\n%s", gatherLogs(0))
		}
		waitCount++
		return podFinished, nil
	})
	// Check overall error
	if err != nil {
		return fmt.Errorf("error waiting for pod %s to complete: %v", podName, err)
	}

	// Inspect the pod's container status for exit code
	pod, errS := cs.CoreV1().Pods(namespace).Get(context.TODO(), podName, metav1.GetOptions{})
	if errS != nil {
		return fmt.Errorf("failed to get pod %s: %v", podName, errS)
	}
	if len(pod.Status.ContainerStatuses) == 0 {
		return fmt.Errorf("no container statuses found for pod %s", podName)
	}
	containerStatus := pod.Status.ContainerStatuses[0]

	if containerStatus.State.Terminated != nil {
		exitCode := containerStatus.State.Terminated.ExitCode
		if exitCode != 0 {
			errmsg := fmt.Errorf("pod %s exited with code %d", podName, exitCode)
			framework.Logf("WARNING: %s.", errmsg.Error())
			return errmsg
		}
	}

	// Validate HTTP response format
	// Expected format: HTTPCode=200 Time=<time>ms
	response := gatherLogs(0)
	if !strings.Contains(response, "HTTPCode=200") {
		errmsg := fmt.Errorf("HTTP response validation failed: HTTP response format must be HTTPCode=200")
		framework.Logf("WARNING: %s.", errmsg.Error())
		return errmsg
	}

	return nil
}

// awsHelper provides AWS API operations for e2e tests
type awsHelper struct {
	ctx         context.Context
	ec2Client   *ec2.Client
	elbv2Client *elbv2.Client

	// Cluster information
	clusterName     string
	clusterTag      string
	clusterTagValue string
	vpcID           string
	awsRegion       string
}

// newAWSHelper creates a new AWS helper with configured clients
func newAWSHelper(ctx context.Context, cs clientset.Interface) (*awsHelper, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %v", err)
	}

	h := &awsHelper{
		ctx:         ctx,
		ec2Client:   ec2.NewFromConfig(cfg),
		elbv2Client: elbv2.NewFromConfig(cfg),
	}

	err = h.discoverClusterTag(cs)
	if err != nil {
		return nil, fmt.Errorf("unable to find cluster tag: %v", err)
	}

	return h, nil
}

// getSecurityGroup gets a security group by ID
func (h *awsHelper) getSecurityGroup(sgID string) (*ec2types.SecurityGroup, error) {
	result, err := h.ec2Client.DescribeSecurityGroups(h.ctx, &ec2.DescribeSecurityGroupsInput{
		GroupIds: []string{sgID},
	})
	if err != nil {
		return nil, err
	}
	if len(result.SecurityGroups) == 0 {
		return nil, fmt.Errorf("security group %s not found", sgID)
	}
	return &result.SecurityGroups[0], nil
}

// waitForSecurityGroupDeletion attempts to delete a security group and waits for it to be deleted
// It handles dependency violations when the SG is still attached to resources like load balancers
func (h *awsHelper) waitForSecurityGroupDeletion(sgID string, timeout time.Duration) error {
	return wait.PollImmediate(10*time.Second, timeout, func() (bool, error) {
		_, err := h.getSecurityGroup(sgID)
		if err != nil {
			framework.Logf("Security group %s appears to be deleted: %v", sgID, err)
			return true, nil
		}

		err = h.deleteSecurityGroup(sgID)
		if err != nil {
			// Check for dependency violation errors
			if strings.Contains(err.Error(), "DependencyViolation") ||
				strings.Contains(err.Error(), "InvalidGroup.InUse") ||
				strings.Contains(err.Error(), "resource has a dependent object") {
				framework.Logf("Security group %s still has dependencies, waiting... (%v)", sgID, err)
				return false, nil // Continue waiting
			}

			// Check if it's already deleted
			if strings.Contains(err.Error(), "InvalidGroup.NotFound") ||
				strings.Contains(err.Error(), "InvalidGroupId.NotFound") {
				framework.Logf("Security group %s is already deleted", sgID)
				return true, nil
			}

			// For other errors, return the error
			return false, err
		}

		// Successfully deleted
		framework.Logf("Successfully deleted security group %s", sgID)
		return true, nil
	})
}

// isSecurityGroupManaged checks if a security group is managed by the controller
// It checks for the cluster ownership tag to determine if the controller owns this security group
func (h *awsHelper) isSecurityGroupManaged(sgID string) (bool, error) {
	sg, err := h.getSecurityGroup(sgID)
	if err != nil {
		return false, err
	}

	// Check for cluster ownership tag - security groups owned by the controller
	// have the cluster tag with "owned" value
	clusterTagKey := fmt.Sprintf("kubernetes.io/cluster/%s", h.clusterName)
	for _, tag := range sg.Tags {
		if aws.ToString(tag.Key) == clusterTagKey &&
			aws.ToString(tag.Value) == "owned" {
			return true, nil
		}
	}
	return false, nil
}

// getLoadBalancerSecurityGroups gets security groups attached to a load balancer
func (h *awsHelper) getLoadBalancerSecurityGroups(lbDNSName string) ([]string, error) {
	// Get Load Balancer ARN from DNS name
	describeLBs, err := h.elbv2Client.DescribeLoadBalancers(h.ctx, &elbv2.DescribeLoadBalancersInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to describe load balancers: %v", err)
	}

	for _, lb := range describeLBs.LoadBalancers {
		if strings.EqualFold(aws.ToString(lb.DNSName), lbDNSName) {
			return lb.SecurityGroups, nil
		}
	}
	return nil, fmt.Errorf("load balancer with DNS %s not found", lbDNSName)
}

// validateSecurityGroupRules validates that security group has the expected ingress rules
func (h *awsHelper) validateSecurityGroupRules(sgID string, expectedPorts []int32) error {
	sg, err := h.getSecurityGroup(sgID)
	if err != nil {
		return err
	}

	foundPorts := make(map[int32]bool)
	for _, rule := range sg.IpPermissions {
		if rule.FromPort != nil && rule.ToPort != nil {
			port := aws.ToInt32(rule.FromPort)
			foundPorts[port] = true
		}
	}

	for _, expectedPort := range expectedPorts {
		if !foundPorts[expectedPort] {
			return fmt.Errorf("expected port %d not found in security group %s rules", expectedPort, sgID)
		}
	}

	return nil
}

// createSecurityGroup creates a new security group for testing purposes
func (h *awsHelper) createSecurityGroup(name, description string) (string, error) {
	result, err := h.ec2Client.CreateSecurityGroup(h.ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(name),
		Description: aws.String(description),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeSecurityGroup,
				Tags: []types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(name),
					},
					{
						Key:   aws.String(fmt.Sprintf("kubernetes.io/cluster/%s", h.clusterName)),
						Value: aws.String("shared"),
					},
				},
			},
		},
		VpcId: aws.String(h.vpcID),
	})
	if err != nil {
		return "", fmt.Errorf("failed to create security group: %v", err)
	}

	return aws.ToString(result.GroupId), nil
}

// deleteSecurityGroup deletes a security group
func (h *awsHelper) deleteSecurityGroup(sgID string) error {
	if _, err := h.ec2Client.DeleteSecurityGroup(h.ctx, &ec2.DeleteSecurityGroupInput{
		GroupId: aws.String(sgID),
	}); err != nil {
		return fmt.Errorf("failed to delete security group %s: %v", sgID, err)
	}

	return nil
}

// discoverClusterTag discovers the cluster tag from a cluster.
// The discover is done by looking up the EC2 instance tags with tag:Name prefix kubernetes.io/cluster.
// The EC2 Instance ID is discovered from a cluster node object.
// The cluster ID, VPC ID and cluster tag are discovered from the EC2 instance tags.
// If is any error is found, the function returns an error.
func (h *awsHelper) discoverClusterTag(cs clientset.Interface) error {
	nodes, err := cs.CoreV1().Nodes().List(h.ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %v", err)
	}

	var (
		instanceID string
	)

	for _, node := range nodes.Items {
		providerID := node.Spec.ProviderID
		if providerID == "" {
			framework.Logf("providerID %s is empty for node %s", providerID, node.Name)
			continue
		}
		providerID = strings.Replace(providerID, "aws:///", "", 1)
		if len(strings.Split(providerID, "/")) < 2 {
			framework.Logf("providerID %s is not a valid EC2 instance ID", providerID)
			continue
		}
		h.awsRegion = strings.Split(providerID, "/")[0]
		instanceID = strings.Split(providerID, "/")[1]
		if !strings.HasPrefix(instanceID, "i-") {
			framework.Logf("instanceID %s is not a valid EC2 instance ID", instanceID)
			continue
		}
		break
	}

	instance, err := h.ec2Client.DescribeInstances(h.ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return fmt.Errorf("failed to describe instances: %v", err)
	}

	clusterTagFound := false
	for _, reservation := range instance.Reservations {
		for _, tag := range reservation.Instances[0].Tags {
			if strings.HasPrefix(aws.ToString(tag.Key), "kubernetes.io/cluster") {
				h.clusterTag = aws.ToString(tag.Key)
				h.clusterTagValue = aws.ToString(tag.Value)
				clusterTagFound = true
				break
			}
		}
		if clusterTagFound {
			break
		}
	}

	if !clusterTagFound {
		return fmt.Errorf("cluster tag not found in the instance %s", instanceID)
	}

	h.clusterName = strings.Split(h.clusterTag, "/")[2]
	if h.clusterName == "" {
		return fmt.Errorf("cluster name not found in the cluster tag %s", h.clusterTag)
	}

	// extract VPC ID from the Instance
	for _, networkInterface := range instance.Reservations[0].Instances[0].NetworkInterfaces {
		h.vpcID = aws.ToString(networkInterface.VpcId)
		break
	}

	if h.vpcID == "" {
		return fmt.Errorf("VPC ID not found in the instance %s", instanceID)
	}

	return nil
}

func collectEventsFromResource(ctx context.Context, cs clientset.Interface, resourceNamespace, resourceName string) {
	framework.Logf("Collecting events for failed resource %q", resourceName)
	events, err := cs.CoreV1().Events(resourceNamespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s", resourceName),
	})
	if err != nil {
		framework.Logf("Failed to list events for resource %q: %v", resourceName, err)
	} else {
		framework.Logf("Events for failed resource %q:", resourceNamespace)
		for _, event := range events.Items {
			framework.Logf("Event: %s, %s, %s, %s", event.Type, event.Reason, event.InvolvedObject.Name, event.Message)
		}
	}
}
