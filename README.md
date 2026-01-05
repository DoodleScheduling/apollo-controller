# Apollo GraphQL kubernetes controller

[![release](https://img.shields.io/github/release/DoodleScheduling/apollo-controller/all.svg)](https://github.com/DoodleScheduling/apollo-controller/releases)
[![release](https://github.com/doodlescheduling/apollo-controller/actions/workflows/release.yaml/badge.svg)](https://github.com/doodlescheduling/apollo-controller/actions/workflows/release.yaml)
[![report](https://goreportcard.com/badge/github.com/DoodleScheduling/apollo-controller)](https://goreportcard.com/report/github.com/DoodleScheduling/apollo-controller)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/DoodleScheduling/apollo-controller/badge)](https://api.securityscorecards.dev/projects/github.com/DoodleScheduling/apollo-controller)
[![Coverage Status](https://coveralls.io/repos/github/DoodleScheduling/apollo-controller/badge.svg?branch=master)](https://coveralls.io/github/DoodleScheduling/apollo-controller?branch=master)
[![license](https://img.shields.io/github/license/DoodleScheduling/apollo-controller.svg)](https://github.com/DoodleScheduling/apollo-controller/blob/master/LICENSE)

This controller manages the [Apollo GraphQL](https://apollo.io/tools/apollo/) router.
The controller can lookup `SubGraph` resources and compose a `SuperGraphSchema` using the official supergraph composer plugin.
A router can be deployed using a `SuperGraph` which uses a composed `SuperGraphSchema`.

### Beta API notice
For v0.x releases and beta api we try not to break the API specs. However
in rare cases backports happen to fix major issues.

## Example

Define some sub graphs:

```yaml
apiVersion: apollo.infra.doodle.com/v1beta1
kind: SubGraph
metadata:
  name: products
spec:
  endpoint: http://product-service/graphql
  schema:
    sdl: |
      extend schema
        @link(url: "https://specs.apollo.dev/federation/v2.0", import: ["@key", "@tag"])

      type Query {
        products: [Product!]!
        product(id: ID!): Product
      }

      type Product @key(fields: "id") {
        id: ID!
        name: String!
        price: Float!
      }
---
apiVersion: apollo.infra.doodle.com/v1beta1
kind: SubGraph
metadata:
  name: users
spec:
  endpoint: http://user-server/graphql
  schema:
    sdl: |
      extend schema
        @link(url: "https://specs.apollo.dev/federation/v2.0", import: ["@key", "@tag"])

      type Query {
        users: [User!]!
        user(id: ID!): User
      }

      type User @key(fields: "id") {
        id: ID!
        username: String!
      }
```

By defining a SuperGraphSchema the controller will compose a schema which again can be used by the router:
```yaml
apiVersion: apollo.infra.doodle.com/v1beta1
kind: SuperGraphSchema
metadata:
  name: root-schema
spec:
  subGraphSelector:
    matchLabels: {}
```

If no subGraph selector on the schema is configured no sub graphs will be included.
`matchLabels: {}` will include all of them in the same namespace as the schema.
By using match labels or expressions it can be configured what sub graphs should be included.

Similar to the `subGraphSelector` it is possible to match sub graphs cross namespace by using `spec.namespaceSelector`. 
By default a `SuperGraphSchema` only looks up sub graphs from the same namespace but with a namespace selector this behaviour can be changed.
Using `namespaceSelector.matchLabels: {}` will lookup sub graphs across all namespaces.

```yaml
apiVersion: apollo.infra.doodle.com/v1beta1
kind: SuperGraphSchema
metadata:
  name: root-schema
spec:
  subGraphSelector:
    matchLabels: {}
```

Deploy a router:
```yaml
apiVersion: apollo.infra.doodle.com/v1beta1
kind: SuperGraph
metadata:
  name: entrypoint
spec:
  schema:
    name: root-schema
  routerConfig: {}
```

## Resource Dependencies
```mermaid
flowchart LR
    SuperGraph["SuperGraph"]
    SuperGraphSchema["SuperGraphSchema"]
    Subgraph1["SubGraph A"]
    Subgraph2["SubGraph B"]
    Subgraph3["SubGraph C"]
    Deployment["Apollo Router"]
    SchemaReconciler["SuperGraph Composer Pod"]

    SuperGraphSchema --> SchemaReconciler
    SuperGraph --> SuperGraphSchema
    Subgraph1 --> SuperGraphSchema
    Subgraph2 --> SuperGraphSchema
    Subgraph3 --> SuperGraphSchema

    SuperGraph --> Deployment
```

## Router deployment template
It is possible to define a custom apollo deployment template which the controller will use to spin up the managed deployment.
In the following example the deployment receives an additional container called mysidecar. Also resources
are declared for the `router` container.

**Note**: The apollo router container is always called router. It is possible to patch that container by using said name as in the example bellow.

```yaml
apiVersion: apollo.infra.doodle.com/v1beta1
kind: SuperGraph
metadata:
  name: entrypoint
spec:
  schema:
    name: root-schema
  deploymentTemplate:
    spec:
      template:
        replicas: 3
        spec:
          containers:
          - name: router
            resources:
              requests:
                memory: 256Mi
                cpu: 50m
              limits:
                memory: 512Mi
          - name: random-sidecar
            image: mysidecar
```

## SuperGraphSchema reconciler template

The super graph schema composer emits a custom supergraph reconciler pod which composes the
super graph config. The pod also comes with an httpd sidecar. The reconciler pod can be customized:

```yaml
apiVersion: apollo.infra.doodle.com/v1beta1
kind: SuperGraphSchema
metadata:
  name: root
spec:
  subGraphSelector:
    matchLabels: {}
  reconcilerTemplate:
    spec:
      containers:
      - image: ghcr.io/doodlescheduling/supergraph:v2.12.1@sha256:ec33ca19c8180122393b809d409dcbabaa30f8e6f6bb67655149859eb2afcfcd
        name: supergraph-composer
        resources:
          limits:
            memory: 256Mi
          requests:
            cpu: 15m
            memory: 128Mi
        securityContext:
          allowPrivilegeEscalation: false
          readOnlyRootFilesystem: false
          runAsGroup: 65532
          runAsNonRoot: true
          runAsUser: 65532
      - image: busybox:1@sha256:d80cd694d3e9467884fcb94b8ca1e20437d8a501096cdf367a5a1918a34fc2fd
        name: httpd
        resources:
          limits:
            memory: 50Mi
          requests:
            cpu: 15m
            memory: 30Mi
        securityContext:
          allowPrivilegeEscalation: false
          readOnlyRootFilesystem: false
          runAsGroup: 65532
          runAsNonRoot: true
          runAsUser: 65532
```

## Subgraph schema from other sources
Besides inline schemas a subgraph schema can also be fetched via http:

```yaml
apiVersion: apollo.infra.doodle.com/v1beta1
kind: SubGraph
metadata:
  name: users
spec:
  endpoint: http://user-server/graphql
  schema:
    http:
      endpoint: http://user-server/graphql-schema
```

## Suspend/Resume reconciliation

All resources support suspending reconciliation.
The reconciliation can be paused by setting `spec.suspend` to `true`:
 
Example:
```
kubectl patch supergraph default-p '{"spec":{"suspend": true}}' --type=merge
```

## Installation

### Helm

Please see [chart/apollo-controller](https://github.com/DoodleScheduling/apollo-controller/tree/master/chart/apollo-controller) for the helm chart docs.

### Manifests/kustomize

Alternatively you may get the bundled manifests in each release to deploy it using kustomize or use them directly.

## Configuration
The controller can be configured using cmd args:
```
--concurrent int                            The number of concurrent SuperGraph reconciles. (default 4)
--default-httpd-image string                The default image which provides an http server to serve the directory /output. By default httpd (busybox) is used. (default "busybox:1")
--default-supergraph-image string           The default rover cli image. (default "ghcr.io/doodlescheduling/supergraph:v0")
--enable-leader-election                    Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.
--graceful-shutdown-timeout duration        The duration given to the reconciler to finish before forcibly stopping. (default 10m0s)
--health-addr string                        The address the health endpoint binds to. (default ":9557")
--insecure-kubeconfig-exec                  Allow use of the user.exec section in kubeconfigs provided for remote apply.
--insecure-kubeconfig-tls                   Allow that kubeconfigs provided for remote apply can disable TLS verification.
--kube-api-burst int                        The maximum burst queries-per-second of requests sent to the Kubernetes API. (default 300)
--kube-api-qps float32                      The maximum queries-per-second of requests sent to the Kubernetes API. (default 50)
--leader-election-lease-duration duration   Interval at which non-leader candidates will wait to force acquire leadership (duration string). (default 35s)
--leader-election-release-on-cancel         Defines if the leader should step down voluntarily on controller manager shutdown. (default true)
--leader-election-renew-deadline duration   Duration that the leading controller manager will retry refreshing leadership before giving up (duration string). (default 30s)
--leader-election-retry-period duration     Duration the LeaderElector clients should wait between tries of actions (duration string). (default 5s)
--log-encoding string                       Log encoding format. Can be 'json' or 'console'. (default "json")
--log-level string                          Log verbosity level. Can be one of 'trace', 'debug', 'info', 'error'. (default "info")
--max-retry-delay duration                  The maximum amount of time for which an object being reconciled will have to wait before a retry. (default 15m0s)
--metrics-addr string                       The address the metric endpoint binds to. (default ":9556")
--min-retry-delay duration                  The minimum amount of time for which an object being reconciled will have to wait before a retry. (default 750ms)
--watch-all-namespaces                      Watch for resources in all namespaces, if set to false it will only watch the runtime namespace. (default true)
--watch-label-selector string               Watch for resources with matching labels e.g. 'sharding.fluxcd.io/shard=shard1'.
```
