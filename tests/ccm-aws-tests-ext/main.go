package main

import (
	"os"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"
	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	"github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	utilflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"

	// Importing ginkgo tests from the e2e package
	_ "k8s.io/cloud-provider-aws/tests/e2e"
)

func main() {
	logs.InitLogs()
	defer logs.FlushLogs()
	pflag.CommandLine.SetNormalizeFunc(utilflag.WordSepNormalizeFunc)

	// Create our registry of openshift-tests extensions
	extensionRegistry := e.NewRegistry()
	kubeTestsExtension := e.NewExtension("openshift", "payload", "aws-cloud-controller-manager")
	extensionRegistry.Register(kubeTestsExtension)

	// Carve up the kube tests into the openshift suites
	kubeTestsExtension.AddSuite(e.Suite{
		Name: "ccm/aws/conformance/parallel",
		Parents: []string{
			"openshift/conformance/parallel",
		},
		Qualifiers: []string{`!labels.exists(l, l == "Serial") && labels.exists(l, l == "Conformance")`},
	})

	kubeTestsExtension.AddSuite(e.Suite{
		Name: "ccm/aws/conformance/serial",
		Parents: []string{
			"openshift/conformance/serial",
		},
		Qualifiers: []string{`labels.exists(l, l == "Serial") && labels.exists(l, l == "Conformance")`},
	})

	// Build our specs from ginkgo
	specs, err := g.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite()
	if err != nil {
		panic(err)
	}

	// Initialization for kube ginkgo test framework needs to run before all tests execute
	// specs.AddBeforeAll(func() {
	// 	if err := initializeTestFramework(os.Getenv("TEST_PROVIDER")); err != nil {
	// 		panic(err)
	// 	}
	// })

	// filter only loadbalancer and nodes tests
	// We must skip unsupported tests on OpenShift, such as ECR.
	loadbalancerSpecs := specs.Select(extensiontests.NameContains("[cloud-provider-aws-e2e] loadbalancer"))
	nodesSpecs := specs.Select(extensiontests.NameContains("[cloud-provider-aws-e2e] nodes"))
	kubeTestsExtension.AddSpecs(loadbalancerSpecs)
	kubeTestsExtension.AddSpecs(nodesSpecs)

	// Cobra stuff
	root := &cobra.Command{
		Long: "Machine API Operator tests extension for OpenShift",
	}

	root.AddCommand(cmd.DefaultExtensionCommands(extensionRegistry)...)

	if err := func() error {
		return root.Execute()
	}(); err != nil {
		os.Exit(1)
	}
}
