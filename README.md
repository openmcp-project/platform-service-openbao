# platform-service-openbao

> [!WARNING]
> This project is experimental and was created as a proof of concept during a hackathon. It is not suitable for use in production environments.

A Kubernetes operator that configures OIDC/JWT trust between Open Control Plane ControlPlane ServiceAccounts and OpenBao instances, enabling tools like External Secrets Operator to authenticate using short-lived Kubernetes JWTs without ever storing or propagating OpenBao tokens.

## Description

This project is a PlatformService for the [Open Control Plane](https://github.com/openmcp-project) ecosystem. It provides a set of CRDs (`OpenBaoInstance`, `ProjectEntity`, `ControlPlaneTrust`, `ControlPlaneEntity`, `PolicyBinding`) that allow platform operators to register approved OpenBao backends and enable end-users to declaratively establish JWT auth trust for their ControlPlanes. The controller manages only the trust plumbing (JWT auth mounts, JWT roles, and identity entities in OpenBao) while policies, secret data, and downstream consumers like External Secrets Operator remain entirely user-managed. Its trust-only, multi-tenant design ensures strict isolation between projects: each ControlPlane receives its own JWT auth mount, each ServiceAccount identity is bound to exactly the policies specified by the tenant, and cross-tenant access is prevented by construction. Once trust is configured, ControlPlane tools authenticate directly with OpenBao using standard Kubernetes ServiceAccount tokens, and the controller never observes or persists the resulting OpenBao client tokens.

## Manual acceptance testing

The constrained Vault Enterprise namespace test topology, security boundaries, manual CLI cases, evidence rules, and exit criteria are documented in [docs/manual-vault-namespace-acceptance-tests.md](docs/manual-vault-namespace-acceptance-tests.md).

The current environment uses Vault rather than OpenBao and does not grant root-namespace administration. The plan records this limitation explicitly and tests within an assigned parent namespace plus isolated customer child namespaces.

## Getting Started

### Prerequisites
- go version v1.24.6+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/platform-service-openbao:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/platform-service-openbao:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/platform-service-openbao:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/platform-service-openbao/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v2-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## Support, Feedback, Contributing

This project is open to feature requests/suggestions, bug reports etc. via [GitHub issues](https://github.com/openmcp-project/platform-service-openbao/issues). Contribution and feedback are encouraged and always welcome. For more information about how to contribute, the project structure, as well as additional contribution information, see our [Contribution Guidelines](https://github.com/openmcp-project/.github/blob/main/CONTRIBUTING.md).

## Security / Disclosure

If you find any bug that may be a security problem, please follow our instructions at [in our security policy](https://github.com/openmcp-project/platform-service-openbao/security/policy) on how to report it. Please do not create GitHub issues for security-related doubts or problems.

## Code of Conduct

We as members, contributors, and leaders pledge to make participation in our community a harassment-free experience for everyone. By participating in this project, you agree to abide by its [Code of Conduct](https://github.com/openmcp-project/.github/blob/main/CODE_OF_CONDUCT.md) at all times.

## Licensing

Copyright OpenControlPlane contributors. Please see our [LICENSE](LICENSE) for copyright and license information. Detailed information including third-party components and their licensing/copyright information is available [via the REUSE tool](https://api.reuse.software/info/github.com/openmcp-project/platform-service-openbao).

---

<p align="center">
  <a href="https://apeirora.eu/content/projects/">
    <img alt="BMWK-EU funding logo" src="https://apeirora.eu/assets/img/BMWK-EU.png" width="300"/>
  </a>
</p>

<p align="center">
  OpenControlPlane is part of <a href="https://apeirora.eu/content/projects/">ApeiroRA</a>, an EU Important Project of Common European Interest (IPCEI-CIS).
</p>

<p align="center">
  Copyright Linux Foundation Europe. For web site terms of use, trademark policy and other project policies please see <a href="https://linuxfoundation.eu/en/policies">https://linuxfoundation.eu/en/policies</a>.
</p>

