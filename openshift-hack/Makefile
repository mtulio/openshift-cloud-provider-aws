GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
GOPROXY ?= $(shell go env GOPROXY)
GIT_VERSION ?= $(shell git describe --dirty --tags --match='v*')
VERSION ?= $(GIT_VERSION)
LDFLAGS ?= -w -s -X k8s.io/component-base/version.gitVersion=$(VERSION) -X main.gitVersion=$(VERSION)

.PHONY: openshift-tests-ext-ccm-aws
openshift-tests-ext-ccm-aws:
	 pushd tests/openshift-tests-ext-ccm-aws && \
	 GO111MODULE=on CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) GOPROXY=$(GOPROXY) go build \
		-trimpath \
		-ldflags="$(LDFLAGS)" \
		-o=../../openshift-tests-ext-ccm-aws .

.PHONY: image
image:
	podman build \
		--authfile $(PULL_SECRET) \
		-f Dockerfile.openshift \
		-t aws-cloud-controller-manager:$(VERSION) .