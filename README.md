# Beelzebub

[![CI](https://github.com/beelzebub-labs/beelzebub/actions/workflows/main.yml/badge.svg)](https://github.com/beelzebub-labs/beelzebub/actions/workflows/main.yml)
[![codecov](https://codecov.io/gh/beelzebub-labs/beelzebub/graph/badge.svg?token=8XTK7D4WHE)](https://codecov.io/gh/beelzebub-labs/beelzebub)
[![Go Reference](https://pkg.go.dev/badge/github.com/beelzebub-labs/beelzebub/v3.svg)](https://pkg.go.dev/github.com/beelzebub-labs/beelzebub/v3)
[![Trust Score](https://archestra.ai/mcp-catalog/api/badge/quality/beelzebub-labs/beelzebub)](https://archestra.ai/mcp-catalog/beelzebub-labs__beelzebub)
[![Mentioned in Awesome Go](https://awesome.re/mentioned-badge.svg)](https://github.com/avelino/awesome-go)

**Open-source deception runtime for SSH, HTTP, TCP, TELNET, and MCP.** Beelzebub deploys realistic decoys, collects high-fidelity threat intelligence, detects prompt-injection attempts against AI agents, and can be extended with trusted Go plugins.

![GitHub Beelzebub - Inception Program](https://github.com/user-attachments/assets/e180d602-6de9-4c48-92ad-eb0ef3c5322d)

## Table Of Contents

- [Key Features](#key-features)
- [Quick Start](#quick-start)
- [Documentation](#documentation)
- [Demo](#demo)
- [Development](#development)
- [Contributing](#contributing)
- [Supported By](#supported-by)
- [License](#license)

## Key Features

- **Adaptive deception:** engage attackers with static or LLM-powered responses while collecting actionable behavior.
- **Low-code configuration:** define realistic services, routes, and response rules in YAML.
- **Multi-protocol coverage:** protect infrastructure and AI-agent surfaces with SSH, HTTP, TCP, TELNET, and MCP decoys.
- **Extensible runtime:** add trusted response generators and services through the public Go plugin SDK.
- **Operational visibility:** expose Prometheus metrics and forward structured events to RabbitMQ or Beelzebub Cloud.
- **Flexible deployment:** run locally, with Docker Compose, or on Kubernetes with Helm.

## Quick Start

Use the Docker installer for an isolated lab deployment:

```bash
git clone https://github.com/beelzebub-labs/beelzebub.git
cd beelzebub
./install.sh --docker
```

Run `beelzebub validate` before exposing any service. Use synthetic credentials and read the [production safety guide](https://docs.beelzebub.ai/operations/production-safety) before internet exposure.

## Documentation

Read the complete, current documentation at **[docs.beelzebub.ai](https://docs.beelzebub.ai)**. It includes installation, configuration, protocol behavior, Docker and Helm operations, observability, security guidance, recipes, plugin authoring, and contribution workflows.

## Demo

See an LLM-powered deception service respond dynamically to attacker input and sustain a realistic interaction beyond fixed command handlers.

![Beelzebub LLM Deception Demo](https://github.com/user-attachments/assets/4dbb9a67-6c12-49c5-82ac-9b3e340406ca)

## Development

Runtime development requires Go 1.25.9, Git, and Make. Build the binary from a local checkout:

```bash
git clone https://github.com/beelzebub-labs/beelzebub.git
cd beelzebub
make build
```

Run unit tests, static analysis, schema validation, and full configuration validation before opening a pull request:

```bash
make test.unit
go vet ./...
make validate-all
```

Integration tests additionally require Docker. Start their dependencies, run the suite, and tear the environment down afterward:

```bash
make test.dependencies.start
make test.integration
make test.dependencies.down
```

See the [development workflow](https://docs.beelzebub.ai/contributing/development) for runtime changes and the [documentation workflow](https://docs.beelzebub.ai/contributing/documentation) for the pnpm-based Fumadocs site.

## Contributing

Contributions are welcome. Follow [CONTRIBUTING.md](CONTRIBUTING.md), participate according to the [Code Of Conduct](CODE_OF_CONDUCT.md), and report vulnerabilities privately through [SECURITY.md](SECURITY.md).

## Supported By

Beelzebub is developed with support from organizations that invest in open-source software. We thank JetBrains for providing the tools that help us build and maintain the project.

[![JetBrains Logo](https://resources.jetbrains.com/storage/products/company/brand/logos/jetbrains.svg)](https://jb.gg/OpenSourceSupport)

## License

Beelzebub is licensed under the [GNU General Public License v3.0](LICENSE).
