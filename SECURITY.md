# Security Policy: NVIDIA Warp Fork

This repository is an NVIDIA-maintained fork of the upstream
[MinIO Warp project](https://github.com/minio/warp).

The fork includes limited changes for NVIDIA use cases. It is not presented as
a security-hardened distribution of Warp, and NVIDIA does not represent that
using this fork reduces the security risks associated with running upstream
Warp. Operators remain responsible for evaluating whether Warp's behavior and
trust model are appropriate for their environment.

## Reporting a Vulnerability

If you discover a potential security vulnerability, please **do not open a
public issue, pull request, or discussion**.

Use one of these private reporting channels:

- **Web (preferred):**
  [NVIDIA Vulnerability Disclosure Program](https://www.nvidia.com/en-us/security/)
- **Email:** [psirt@nvidia.com](mailto:psirt@nvidia.com)
  - For encrypted email, use the
    [NVIDIA public PGP key](https://www.nvidia.com/en-us/security/pgp-key).
- **GitHub:** Use this repository's **Security** tab and select
  **Report a vulnerability**.

Please include:

- The affected version, tag, branch, or commit
- The affected component and vulnerability type
- Reproduction instructions
- Proof-of-concept material, if available
- The expected and observed behavior
- A description of the security impact
- Whether the behavior also reproduces in upstream MinIO Warp

NVIDIA's Product Security Incident Response Team will acknowledge the report,
validate its impact, determine whether it is specific to this fork or inherited
from upstream, coordinate with upstream when appropriate, and develop or publish
fork-specific fixes or advisories when warranted.

## Fork and Upstream Security Scope

The threat model and operational exposure of this fork are substantially the
same as those of upstream MinIO Warp. NVIDIA-specific changes are not intended
to provide additional authentication, transport security, isolation, or
hardening for Warp's distributed control and monitoring interfaces.

Security reports are in scope when they identify:

- A vulnerability introduced by an NVIDIA-specific change
- A fork-specific regression or material increase in impact
- A security-relevant divergence from current upstream behavior
- A new security boundary violation not already covered by the documented
  inherited limitations below

Reports that solely restate a documented, unchanged upstream behavior may be
closed as a known inherited limitation or redirected to the upstream project.
NVIDIA may incorporate upstream fixes, but does not independently commit to
redesigning inherited Warp security boundaries in this fork.

## Security Architecture and Context

Warp is a high-performance Go command-line application for benchmarking S3
compatible object storage and Iceberg REST catalogs. It can run benchmarks
locally or coordinate distributed benchmark clients.

Its principal interfaces and data flows include:

- CLI flags, environment variables, and YAML benchmark configuration
- S3 and catalog credentials supplied by the operator
- S3, Iceberg REST catalog, and optional metrics endpoints
- A distributed WebSocket interface used to control `warp client` processes
- An optional HTTP monitoring API enabled with `--serve`
- A loopback-bound web UI for displaying benchmark results
- Compressed benchmark operation files and analysis output
- CI and release workflows used to build and test artifacts

Warp operates at the **CLI tool and application** level. Its primary security
responsibilities are protecting benchmark credentials and results, restricting
control of distributed clients, and ensuring destructive benchmark operations
are confined to operator-designated test resources. Warp is not an
authentication gateway, multi-tenant service, or general-purpose security
boundary.

**Repository Exposure Classification:** Public.
Basis: this is an open-source repository hosted publicly on GitHub; this
document is written for public consumption.

**Service Exposure Classification:** External / Regulated (high confidence).
Basis: Warp is externally distributed software, handles storage credentials,
performs privileged data operations, and exposes optional network control and
monitoring interfaces. This contextual classification reflects external
distribution; it is not a vulnerability severity rating or a statement that
every deployment is regulated.

The security model trusts the operator, the host operating system, local
configuration files, and the network controls surrounding distributed clients.
S3 services, catalog services, monitoring callers, and network peers cross
explicit trust boundaries and must be evaluated by the operator.

### Threat Model

1. **Unauthenticated distributed client control:** `cli/clientmode.go` exposes
   the `/ws` WebSocket endpoint, and `cli/benchclient.go` accepts benchmark
   requests without cryptographically authenticating the coordinator. A party
   that can reach this interface may direct a client to execute supported
   benchmark commands with supplied flags, including commands that generate
   substantial traffic or perform destructive storage operations.

2. **Distributed control-channel credential disclosure or modification:**
   `cli/benchserver.go` serializes benchmark configuration, including S3
   credentials, and connects to clients using unencrypted `ws://` transport.
   A party able to observe or modify traffic on that network path may obtain
   credentials or alter benchmark configuration.

3. **Unauthenticated monitoring API access:** When an operator enables
   `--serve`, `api/api.go` exposes benchmark status and operation data without
   authentication. Its `/v1/stop` operation can also close the monitoring
   service. Any caller able to reach the configured listen address is therefore
   treated as trusted.

4. **Destructive or misdirected benchmark operations:** Warp credentials
   commonly require permission to create, list, upload, and delete objects.
   Benchmark preparation and cleanup can remove all content from the selected
   bucket. Incorrect endpoints, bucket names, configuration files, or
   credentials can cause data loss outside the intended test environment.

5. **Unverified external tooling in CI:** The
   `.github/workflows/qreleaser-test.yml` workflow downloads, extracts, and
   executes release tooling from an external URL without verifying a pinned
   checksum or signature. If that workflow is run, compromise of the retrieved
   artifact or its distribution path could affect the CI environment and
   generated artifacts.

### Documented Inherited Limitations

The following behaviors are inherited from upstream Warp and are not scheduled
for independent remediation by NVIDIA in this fork:

- The distributed WebSocket endpoint does not authenticate benchmark
  coordinators.
- Distributed benchmark configuration and S3 credentials are carried over an
  unencrypted WebSocket connection.
- The optional monitoring API does not authenticate callers and permits the
  monitoring service to be stopped remotely.
- The release-test workflow executes externally downloaded tooling without
  independent artifact integrity verification.

Operators must mitigate these limitations through deployment controls. Reports
that only restate these unchanged behaviors, without demonstrating a new
fork-specific impact or boundary violation, may be closed as documented
limitations.

### Critical Security Assumptions

- Distributed Warp clients and the `--serve` monitoring API are reachable only
  from explicitly trusted hosts and networks.
- The network between benchmark coordinators and clients provides the required
  confidentiality, integrity, and peer authentication because Warp's
  WebSocket protocol does not provide them.
- Operators use dedicated, least-privileged credentials and isolated benchmark
  buckets that contain no production or otherwise valuable data.
- Operators enable TLS and certificate verification for S3 and catalog
  connections whenever the target supports them. Use of `--insecure` transfers
  certificate-validation responsibility to the operator.
- The host operating system protects process arguments, environment variables,
  configuration files, benchmark output, and logs from unauthorized users.
- YAML configuration files, template variables, endpoint lists, and benchmark
  input files are obtained from trusted sources and reviewed before execution.
- The build and release environment independently establishes trust in
  dependencies, downloaded tooling, and generated binaries.
- Users evaluate relevant upstream MinIO Warp behavior and security information;
  the NVIDIA fork does not imply an improved upstream security posture.

## Deployment and Operational Guidance

- Always bind `warp client` to an explicit loopback or private interface and
  restrict its listening port with host and network firewalls.
- Run distributed benchmarks only on an isolated trusted network or through a
  separate authenticated and encrypted tunnel.
- Bind `--serve` to loopback unless remote monitoring is required. If remote
  access is necessary, place it behind an authenticated proxy or equivalent
  network control.
- Use short-lived, least-privileged credentials dedicated to the benchmark.
- Use a dedicated empty bucket and verify the target endpoint and bucket name
  before every run. Never point Warp at a bucket containing retained data.
- Enable TLS for S3 and catalog traffic and avoid `--insecure`. Protect debug
  output and do not enable debug logging around sensitive traffic unless its
  output is handled securely.
- Treat YAML files, environment files, shell history, benchmark operation files,
  reports, and metrics destinations as potentially sensitive.
- Verify the provenance and integrity of binaries and build inputs before use.
