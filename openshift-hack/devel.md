# OpenShift hacking

## OpenShift Tests Extension (OTE) binary

### Building

Chose one of the steps below to build the OTE binary from root of the project.

To build the OTE binary you can run:

```sh
make -f openshift-hack/Makefile openshift-tests-ext-ccm-aws
```

To build the container image you can run:

```sh
podman build --authfile $PULL_SECRET -f Dockerfile.openshift -t ccm-local:devel .
```

where:

- `$PULL_SECRET` is the path to the registry credentials ('pull-secret')

### Using OTE

List the existing tests:

```sh
./openshift-tests-ext-ccm-aws list tests | jq .[].name
```

List suites:

```sh
./openshift-tests-ext-ccm-aws list tests | jq .[].name
```


Run suite:

```sh
TBD
```

Run a specific test:

```sh
TBD
```
