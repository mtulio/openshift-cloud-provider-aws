# The Service Controller

The service controller is responsible for watch for service and node object changes, so that it can create, update, or delete cloud load balancers corresponding to load balanced services.  Like the other controllers, we import the cloud-provider provided utility functions for managing the controller itself, which calls into cloud provider defined methods `GetLoadBalancer`, `GetLoadBalancerName`, `EnsureLoadBalancer`, `UpdateLoadBalancer`, and `EnsureLoadBalancerDeleted`.


| Annotation | Valid Values | Default | Valid for | Description |
| --- | --- | --- | --- | --- |
| service.beta.kubernetes.io/aws-load-balancer-access-log-emit-interval          | [5\|60]                             | -   | ELB | How frequently the load balancer emits [access logs](https://docs.aws.amazon.com/elasticloadbalancing/latest/classic/access-log-collection.html), in minutes.  |
| service.beta.kubernetes.io/aws-load-balancer-access-log-enabled                | [true\|false]                       | -   | ELB | If true, access logs is enabled.  |
| service.beta.kubernetes.io/aws-load-balancer-access-log-s3-bucket-name         | -                                   | -   | ELB | Access log S3 bucket name.  |
| service.beta.kubernetes.io/aws-load-balancer-access-log-s3-bucket-prefix       | -                                   | -   | ELB | Access log S3 bucket prefix.  |
| service.beta.kubernetes.io/aws-load-balancer-additional-resource-tags          | Comma-separated list of key=value   | -   | ELB,NLB | A comma-separated list of key-value pairs which will be recorded as additional tags in the ELB. For example: "Key1=Val1,Key2=Val2,KeyNoVal1=,KeyNoVal2" |
| service.beta.kubernetes.io/aws-load-balancer-backend-protocol                  | [http\|https\|ssl\|tcp]             | -   | ELB,NLB | Specifies the protocol spoken by the backend (pod) behind a listener. If `http` (default) or `https`, an HTTPS listener that terminates the connection and parses headers is created. If set to `ssl` or `tcp`, a "raw" SSL listener is used. If set to `http` and `aws-load-balancer-ssl-cert` is not used then a HTTP listener is used. |
| service.beta.kubernetes.io/aws-load-balancer-connection-draining-enabled       | [true\|false]                       | -   | ELB | Enable [connection draining](https://docs.aws.amazon.com/elasticloadbalancing/latest/classic/config-conn-drain.html). |
| service.beta.kubernetes.io/aws-load-balancer-connection-draining-timeout       | [1-3600]                            | 300 | ELB | The maximum time (in seconds) for the load balancer to keep connections alive before reporting the instance as de-registered. The maximum timeout value can be set between 1 and 3,600 seconds (the default is 300 seconds). When the maximum time limit is reached, the load balancer forcibly closes connections to the de-registering instance. |
| service.beta.kubernetes.io/aws-load-balancer-connection-idle-timeout           | [1-4000]                            | 60  | ELB | The load balancer has a configured idle timeout period (in seconds) that applies to its connections. If no data has been sent or received by the time that the idle timeout period elapses, the load balancer closes the connection. |
| service.beta.kubernetes.io/aws-load-balancer-cross-zone-load-balancing-enabled | [true\|false]                       | -   | ELB | With cross-zone load balancing, each load balancer node for your Classic Load Balancer distributes requests evenly across the registered instances in all enabled Availability Zones. If cross-zone load balancing is disabled, each load balancer node distributes requests evenly across the registered instances in its Availability Zone only. |
| service.beta.kubernetes.io/aws-load-balancer-extra-security-groups             | Comma-separated list                | -   | ELB | Specifies additional security groups to be added to ELB.    |
| service.beta.kubernetes.io/aws-load-balancer-security-groups                   | Comma-separated list                | -   | ELB | Specifies the security groups to be added to ELB. Differently from the annotation "service.beta.kubernetes.io/aws-load-balancer-extra-security-groups", this replaces all other security groups previously assigned to the ELB. |
| service.beta.kubernetes.io/aws-load-balancer-healthcheck-healthy-threshold     | [2-10]                              | -   | NLB | Specifies the number of successive successful health checks required for a backend to be considered healthy for traffic. For NLB, healthy-threshold and unhealthy-threshold must be equal. |
| service.beta.kubernetes.io/aws-load-balancer-healthcheck-interval              | [5-300]                             | 30  | NLB | Specifies, in seconds, the interval between health checks. |
| service.beta.kubernetes.io/aws-load-balancer-healthcheck-timeout               | [2-60]                              | 5   | NLB | The amount of time to wait when receiving a response from the health check, in seconds. |
| service.beta.kubernetes.io/aws-load-balancer-healthcheck-unhealthy-threshold   | [2-10]                              | 2   | NLB | The number of consecutive failed health checks that must occur before declaring an EC2 instance unhealthy. |
| service.beta.kubernetes.io/aws-load-balancer-internal                          | [true\|false]                       | -   | ELB,NLB | Indicates that the load balancer should be internal. |
| service.beta.kubernetes.io/aws-load-balancer-proxy-protocol                    | [*]                                 | -   | ELB | Enables the proxy protocol on an ELB. Right now we only accept the value "*" which means enable the proxy protocol on all ELB backends. In the future we could adjust this to allow setting the proxy protocol only on certain backends. |
| service.beta.kubernetes.io/aws-load-balancer-ssl-cert                          | IAM or ACM ARN                      | -   | ELB,NLB | Requests a secure listener. Value is a valid certificate ARN. For more, see the [elb listener config guide](http://docs.aws.amazon.com/ElasticLoadBalancing/latest/DeveloperGuide/elb-listener-config.html).  CertARN is an IAM or CM certificate ARN. |
| service.beta.kubernetes.io/aws-load-balancer-ssl-negotiation-policy            | -                                   | ELBSecurityPolicy-2016-08 | ELB,NLB | Specifies SSL negotiation settings for the HTTPS/SSL listeners of your load balancer. Defaults to the default ELB policy. |
| service.beta.kubernetes.io/aws-load-balancer-ssl-ports                         | Comma-separated list                | *   | ELB,NLB | Specifies a comma-separated list of ports that will use SSL/HTTPS listeners. Defaults to all. |
| service.beta.kubernetes.io/aws-load-balancer-type                              | [nlb]                               | -   | ELB,NLB | Indicates the type of Load Balancer. The only valid value is nlb.  Leaving this field blank is equivalent to selecting ELB. |
| service.beta.kubernetes.io/aws-load-balancer-eip-allocations                   | Comma-separated list                | -   | NLB | List of EIP allocations to associate with a internet-facing load balancer. Only valid for NLB. |
| service.beta.kubernetes.io/aws-load-balancer-healthcheck-path                  | -                                   | /   | NLB | Specifies the http path for the health check in case of http/https protocol. |
| service.beta.kubernetes.io/aws-load-balancer-healthcheck-port                  | [traffic-port\|1-65535]             | traffic-port | NLB | Specifies the TCP target port for the target group health check. |
| service.beta.kubernetes.io/aws-load-balancer-healthcheck-protocol              | [tcp\|http\|https]                  | tcp | NLB | Specifies the protocol to use for the target group health check. |
| service.beta.kubernetes.io/aws-load-balancer-subnets                           | Comma-separated list                | -   | ELB,NLB | Specifies the Availability Zone configuration for the load balancer. The values are comma separated list of subnetID or subnetName from different AZs. |
| service.beta.kubernetes.io/aws-load-balancer-target-node-labels                | Comma-separated list of key=value   | -   | ELB,NLB | Specifies a comma-separated list of key-value pairs which will be used to select the target nodes for the load balancer. |
| service.beta.kubernetes.io/aws-load-balancer-ip-address-type | [ipv4\|dualstack] | ipv4 | NLB | IP Address Type used to create the Network Load Balancer (NLB). The subnet must have assigned valid IPv6 CIDR block. |
| service.beta.kubernetes.io/target-group-ip-address-type | [ipv4\|ipv6] | ipv4 | NLB | IP Address Type used to create the Target Group (TG). Default is `ipv4`  |


## Annotation Configuration

### NLB dual-stack

Proposed changes:
- introduce annotations:
    - `service.beta.kubernetes.io/aws-load-balancer-ip-address-type`: allowing users to provision Service NLB with frontend dual-stack support. Valid values: `ipv4` and `dualstack`. Default: `ipv4`
    - `service.beta.kubernetes.io/target-group-ip-address-type`: allowing users to change the default target ip address type to `ipv6`. Valid values: `ipv4` and `ipv6`. Default: `ipv4`. Requires `aws-load-balancer-ip-address-type`

Prerequisites:
- VPC with a IPv6 CIDR block
- Dual-stack subnets
- Egress-only Internet Gateway (when using IPv6 in private subnets)
- Routes assigned to the subnets used/discovered by controller

Scenarios:
- Public NLB dual-stack subnet with target IPv4 type instance
- Public NLB dual-stack subnet with target IPv4 type ip
- Public NLB dual-stack subnet with target IPv6 type instance (the targets must have an assigned primary IPv6 address)
- Public NLB dual-stack subnet with target IPv6 type ip
- Private NLB dual-stack subnet with target IPv4 type instance
- Private NLB dual-stack subnet with target IPv4 type ip
- Private NLB dual-stack subnet with target IPv6 type instance (the targets must have an assigned primary IPv6 address)
- Private NLB dual-stack subnet with target IPv6 type ip

Violations:
- Single-stack IPv6 subnets must be rejected for NLBs
- Service type-CLB must reject annotation ip-address-type
- When target IPv6, the node must have the IPv6 interface as primary (?)
- BYO Subnets must be considered (?) (is it currently supported on NLB?)
- When target ipv6, the instances must be validated if there is ipv6 address available

Open questions for CCM:
- What is the default IP address type for the target (ipv4 or ipv6)? Should it be ipv4 and explicitly set ipv6 when user knows their environment have machines with primary ipv6? Is there issue when set ipv6 and machines are primary ipv4?
- How to teach the controller to use target IPv6? How ALBC handles it?

Red Hat OpenShift work:
- Need to validate how Installer will set the cluster to validate if the CCM proposal meets the expectation of the product
- What is the default IP address type for the target (ipv4 or ipv6)? Should it follow the same strategy of Installer's API flow?
- Need to validate wether there are CIO changes to make sure controller sets the annotation to instruct the default router Service to provision dual-stack NLB.

Red Hat Discussions:
- https://redhat-internal.slack.com/archives/C05KZA3NVU6/p1754300967882369
- https://github.com/openshift/enhancements/pull/1806

Related AWS Documentation:
- https://docs.aws.amazon.com/elasticloadbalancing/latest/network/network-load-balancers.html#ip-address-type
- https://docs.aws.amazon.com/elasticloadbalancing/latest/network/load-balancer-ip-address-type.html
- https://docs.aws.amazon.com/elasticloadbalancing/latest/network/load-balancer-target-groups.html#target-group-ip-address-type
- https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/using-instance-addressing.html#ipv6-addressing

