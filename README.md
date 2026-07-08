# Beelzebub

[![CI](https://github.com/beelzebub-labs/beelzebub/actions/workflows/main.yml/badge.svg)](https://github.com/beelzebub-labs/beelzebub/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/beelzebub-labs/beelzebub/v3)](https://goreportcard.com/report/github.com/beelzebub-labs/beelzebub/v3)
[![codecov](https://codecov.io/gh/beelzebub-labs/beelzebub/graph/badge.svg?token=8XTK7D4WHE)](https://codecov.io/gh/beelzebub-labs/beelzebub)
[![Go Reference](https://pkg.go.dev/badge/github.com/beelzebub-labs/beelzebub/v3.svg)](https://pkg.go.dev/github.com/beelzebub-labs/beelzebub/v3)
[![Trust Score](https://archestra.ai/mcp-catalog/api/badge/quality/beelzebub-labs/beelzebub)](https://archestra.ai/mcp-catalog/beelzebub-labs__beelzebub)
[![Mentioned in Awesome Go](https://awesome.re/mentioned-badge.svg)](https://github.com/avelino/awesome-go)

**Deception Runtime Framework**

Beelzebub is an open-source deception runtime that deploys adaptive, LLM-powered decoy services across SSH, HTTP, TCP, TELNET, and MCP protocols. It goes beyond passive honeypots by actively engaging attackers in realistic interactions, collecting high-fidelity threat intelligence, and detecting prompt injection attacks against AI agents.

![github beelzebub - inception program](https://github.com/user-attachments/assets/e180d602-6de9-4c48-92ad-eb0ef3c5322d)

## Table of Contents

- [Beelzebub](#beelzebub)
  - [Table of Contents](#table-of-contents)
  - [Key Features](#key-features)
  - [LLM Deception Demo](#llm-deception-demo)
  - [Quick Start](#quick-start)
    - [Installer](#installer)
    - [Local (Go)](#local-go)
    - [Docker](#docker)
    - [Using Helm (Kubernetes)](#using-helm-kubernetes)
  - [CLI Reference](#cli-reference)
    - [`beelzebub run`](#beelzebub-run)
    - [`beelzebub validate`](#beelzebub-validate)
    - [`beelzebub plugin`](#beelzebub-plugin)
    - [`beelzebub version`](#beelzebub-version)
  - [Plugin System](#plugin-system)
    - [Interfaces](#interfaces)
    - [Writing a Plugin](#writing-a-plugin)
    - [Installing External Plugins](#installing-external-plugins)
  - [Observability](#observability)
    - [Prometheus Metrics](#prometheus-metrics)
    - [RabbitMQ Integration](#rabbitmq-integration)
  - [Testing](#testing)
  - [Code Quality](#code-quality)
  - [License](#license)
  - [Contributing](#contributing)
  - [Configuration Reference](#configuration-reference)
    - [Core Configuration](#core-configuration)
    - [Service Configuration](#service-configuration)
  - [Deception Services](#deception-services)
    - [MCP Deception Service](#mcp-deception-service)
      - [How It Works](#how-it-works)
    - [HTTP Deception Service](#http-deception-service)
    - [SSH Deception Service](#ssh-deception-service)
    - [TELNET Deception Service](#telnet-deception-service)
    - [TCP Deception Service](#tcp-deception-service)
  - [Supported By](#supported-by)

## Key Features

- **Adaptive deception engine**: LLM integration (OpenAI, Ollama) generates contextually accurate responses in real time, keeping attackers engaged long enough to collect actionable TTPs
- **Low-code service definition**: YAML-based configuration with regex command matching — no custom code required to deploy a new decoy service
- **Multi-protocol coverage**: SSH, HTTP, TCP, TELNET, MCP  from infrastructure targets to AI agent attack surfaces
- **Extensible plugin system**: Implement the `CommandPlugin` or `HTTPPlugin` interface and register via `init()`  no core changes required
- **Full observability stack**: Prometheus metrics, RabbitMQ event streaming
- **Production-ready runtime**: Docker, Kubernetes (Helm), graceful shutdown, per-service memory limits

## LLM Deception Demo

![demo-beelzebub](https://github.com/user-attachments/assets/4dbb9a67-6c12-49c5-82ac-9b3e340406ca)

## Quick Start

### Installer

```bash
./install.sh     # asks local or Docker, checks prerequisites, and starts it
```

Non-interactive: `./install.sh --local` or `./install.sh --docker`.

### Local (Go)

```bash
make start     # installs any declared plugins, compiles them in, and runs
```

### Docker

```bash
make docker    # builds an image with declared plugins baked in, then runs it
```

### Using Helm (Kubernetes)

```bash
helm install beelzebub ./beelzebub-chart
# Upgrade:
helm upgrade beelzebub ./beelzebub-chart
```

## CLI Reference

Beelzebub ships with a structured CLI. Run `beelzebub --help` to see all available commands.

### `beelzebub run`

Start all configured deception services.

```bash
beelzebub run [flags]

Flags:
  -c, --conf-core string       Path to core configuration file (default "./configurations/beelzebub.yaml")
  -s, --conf-services string   Path to services configuration directory (default "./configurations/services/")
  -m, --mem-limit-mib int      Memory limit in MiB, -1 to disable (default 100)
```

### `beelzebub validate`

Parse and validate all configuration files without starting any services. Useful in CI pipelines.

```bash
beelzebub validate --conf-core ./configurations/beelzebub.yaml --conf-services ./configurations/services/
```

### `beelzebub plugin`

Install, list, and remove plugins fetched from GitHub. See [Plugin System](#plugin-system).

```bash
beelzebub plugin install github.com/your-org/beelzebub-myplugin
beelzebub plugin list
beelzebub plugin remove myplugin
```

### `beelzebub version`

Print version, commit SHA, build date, and Go runtime information.

```bash
beelzebub version
```

## Plugin System

Beelzebub exposes a stable public SDK at `pkg/plugin` for extending the deception runtime without modifying core code.

### Interfaces

```go
// CommandPlugin generates text responses for SSH, TCP, TELNET, and HTTP services.
type CommandPlugin interface {
    Metadata() Metadata
    Execute(ctx context.Context, req CommandRequest) (string, error)
}

// HTTPPlugin generates full HTTP responses with status code, headers, and body.
type HTTPPlugin interface {
    Metadata() Metadata
    HandleHTTP(r *http.Request) HTTPResponse
}
```

### Writing a Plugin

```go
package myplugin

import (
    "context"
    "github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
)

type MyPlugin struct{}

func (p *MyPlugin) Metadata() plugin.Metadata {
    return plugin.Metadata{
        Name:        "MyPlugin",
        Description: "Custom deception response generator",
        Version:     "1.0.0",
        Author:      "your-name",
    }
}

func (p *MyPlugin) Execute(_ context.Context, req plugin.CommandRequest) (string, error) {
    return "simulated response to: " + req.Command, nil
}

func init() {
    plugin.Register(&MyPlugin{})
}
```

### Installing External Plugins


```bash
# Declare plugins in configurations/plugins.yaml, or:
beelzebub plugin install github.com/your-org/myplugin   # also appends to the config

make start     # local:  install declared plugins → build → run   (needs Go)
make docker    # docker:  image with plugins baked in → run        (needs Docker)
```

| Command | What it does |
|---|---|
| `plugin install <link>` | fetch a plugin, wire it in, rebuild; also adds it to `configurations/plugins.yaml` |
| `plugin install` | install everything declared in `configurations/plugins.yaml` |
| `plugin list` | show installed plugins vs. what's compiled into the binary |
| `plugin update [name]` | re-fetch at the declared ref and re-pin the commit |
| `plugin remove <name>` | remove a plugin from `configurations/plugins.yaml`, unwire it, and print the rebuild step |

Deployment plugin sources are configured in `configurations/plugins.yaml`:

```yaml
plugins:
  - source: github.com/your-org/myplugin
  - source: github.com/your-org/private-plugin@v1.2.0
```

Future per-plugin runtime configuration can live under `configurations/plugins/`
as one YAML file per plugin.


Each plugin repo must ship a `plugins.yaml` manifest and self-register in `init()`
(see [Writing a Plugin](#writing-a-plugin)):

```yaml
name: myplugin
version: 1.0.0
module: github.com/your-org/myplugin   # must match its go.mod
entrypoint: .                          # package that calls plugin.Register (default ".")
min-core-version: v3.8.0               # optional
dependencies:                          # optional metadata; Go dependencies still come from go.mod
  - github.com/your-org/shared@v1.2.3
```

Installed plugins are compiled into the Beelzebub binary and run in the same
process as the runtime. Install plugins only from repositories you trust.

## Observability

### Prometheus Metrics

Beelzebub exposes Prometheus metrics at the configured endpoint (default: `:2112/metrics`):

| Metric | Description |
|--------|-------------|
| `beelzebub_events_total` | Total deception events across all services |
| `beelzebub_events_ssh_total` | SSH events |
| `beelzebub_events_http_total` | HTTP events |
| `beelzebub_events_tcp_total` | TCP events |
| `beelzebub_events_telnet_total` | TELNET events |
| `beelzebub_events_mcp_total` | MCP events |

### RabbitMQ Integration

Publish all deception events to a message queue for downstream SIEM integration:

```yaml
core:
  tracings:
    rabbit-mq:
      enabled: true
      uri: "amqp://guest:guest@localhost:5672/"
```

Events are published as structured JSON to the `event` queue.

## Testing

```bash
# Unit tests
make test.unit

# Integration tests (requires Docker)
make test.dependencies.start
make test.integration
make test.dependencies.down

# Validate configuration without starting services
beelzebub validate
```

## Code Quality

- **CI**: GitHub Actions on every commit and pull request
- **Static analysis**: CodeQL and Go Report Card
- **Coverage**: Monitored via [Codecov](https://codecov.io/gh/beelzebub-labs/beelzebub)
- **Code review**: All contributions undergo peer review

## License

Beelzebub is licensed under the [GNU GPL v3 License](LICENSE).

## Contributing

The Beelzebub team welcomes contributions and project participation. Whether you want to report bugs, contribute new features, or have any questions, please refer to our [Contributor Guide](CONTRIBUTING.md) for detailed information. We encourage all participants and maintainers to adhere to our [Code of Conduct](CODE_OF_CONDUCT.md) and foster a supportive and respectful community.

Happy hacking!

## Configuration Reference

Beelzebub uses a two-tier configuration system:

1. **Core configuration** (`beelzebub.yaml`)  global settings: logging, tracing, Prometheus
2. **Service configurations** (`services/*.yaml`)  one file per decoy service

### Core Configuration

```yaml
core:
  logging:
    debug: false
    debugReportCaller: false
    logDisableTimestamp: true
    logsPath: ./logs
  tracings:
    rabbit-mq:
      enabled: false
      uri: "amqp://guest:guest@localhost:5672/"
  prometheus:
    path: "/metrics"
    port: ":2112"
```

Environment variable overrides are supported for all fields (e.g. `BEELZEBUB_RABBITMQ_ENABLED`). Service configurations can also be supplied entirely via `BEELZEBUB_SERVICES_CONFIG` as a JSON array.

### Service Configuration

Each decoy service is defined in a separate YAML file placed in the `services/` directory. The `protocol` field determines the deception engine used. Commands use `regex` for request matching and either a static `handler` or a `plugin` reference for dynamic responses.

## Deception Services

### MCP Deception Service

MCP (Model Context Protocol) deception services expose decoy tools designed to detect prompt injection attacks against LLM-powered agents.

#### How It Works

The decoy tool is registered in the agent's tool list but should never be invoked under normal operation. Any invocation signals that a prompt injection attack has successfully bypassed the agent's guardrails. This provides:

- **Real-time guardrail bypass detection**  instant alerting when an attacker convinces the agent to invoke a restricted tool
- **Authentic attack prompt collection**  every activation logs the exact malicious prompt used
- **Measurable attack surface metrics**  track HAR, TPR, and MTP over time

![video-mcp-diagram](https://github.com/user-attachments/assets/e04fd19e-9537-427e-9131-9bee31d8ebad)

**mcp-8000.yaml**:

```yaml
apiVersion: "v1"
protocol: "mcp"
address: ":8000"
description: "MCP Honeypot"
tools:
  - name: "tool:user-account-manager"
    description: "Tool for querying and modifying user account details. Requires administrator privileges."
    params:
      - name: "user_id"
        description: "The ID of the user account to manage."
      - name: "action"
        description: "The action to perform on the user account, possible values are: get_details, reset_password, deactivate_account"
    handler: |
      {
        "tool_id": "tool:user-account-manager",
        "status": "completed",
        "output": {
          "message": "Tool 'tool:user-account-manager' executed successfully. Results are pending internal processing and will be logged.",
          "result": {
            "operation_status": "success",
            "details": "email: kirsten@gmail.com, role: admin, last-login: 02/07/2025"
          }
        }
      }
  - name: "tool:system-log"
    description: "Tool for querying system logs. Requires administrator privileges."
    params:
      - name: "filter"
        description: "The input used to filter the logs."
    handler: |
      {
        "tool_id": "tool:system-log",
        "status": "completed",
        "output": {
          "message": "Tool 'tool:system-log' executed successfully.",
          "result": {
            "operation_status": "success",
            "details": "Info: email: kirsten@gmail.com, last-login: 02/07/2025"
          }
        }
      }
```

Accessible via `http://beelzebub:port/mcp` (Streamable HTTP transport).

### HTTP Deception Service

HTTP deception services respond to web requests with configurable responses based on URL pattern matching. Supports TLS, static handlers, LLM-powered responses, and the infinite maze generator.

**WordPress simulation** (`http-80.yaml`):

```yaml
apiVersion: "v1"
protocol: "http"
address: ":80"
description: "Wordpress 6.0"
commands:
  - regex: "^(/index.php|/index.html|/)$"
    handler: |
      <html><header><title>Wordpress 6 test page</title></header>
      <body><h1>Hello from Wordpress</h1></body></html>
    headers:
      - "Content-Type: text/html"
      - "Server: Apache/2.4.53 (Debian)"
      - "X-Powered-By: PHP/7.4.29"
    statusCode: 200
  - regex: "^(/wp-login.php|/wp-admin)$"
    handler: |
      <html><body>
        <form method="post">
          <input type="text" name="uname" placeholder="Username" required>
          <input type="password" name="psw" placeholder="Password" required>
          <button type="submit">Login</button>
        </form>
      </body></html>
    headers:
      - "Content-Type: text/html"
      - "Server: Apache/2.4.53 (Debian)"
    statusCode: 200
  - regex: "^.*$"
    handler: "<html><body><h1>Not found!</h1></body></html>"
    headers:
      - "Content-Type: text/html"
    statusCode: 404
```

**LLM-powered HTTP service**  add a `fallbackCommand` with `plugin: LLMHoneypot` to generate dynamic responses for any unmatched request.

**Infinite maze generator**  use `plugin: MazeHoneypot` to deploy an Apache-style directory listing that expands infinitely, trapping automated scanners and crawlers.

### SSH Deception Service

SSH deception services support both static command responses and LLM-powered interactive sessions with per-session conversation history.

**LLM-powered SSH** (OpenAI):

```yaml
apiVersion: "v1"
protocol: "ssh"
address: ":2222"
description: "SSH interactive GPT-4o"
commands:
  - regex: "^(.+)$"
    plugin: "LLMHoneypot"
serverVersion: "OpenSSH"
serverName: "ubuntu"
passwordRegex: "^(root|qwerty|Smoker666|123456|jenkins|minecraft|sinus|alex|postgres|Ly123456)$"
deadlineTimeoutSeconds: 60
plugin:
  llmProvider: "openai"
  llmModel: "gpt-4o"
  openAISecretKey: "sk-proj-1234"
```

**LLM-powered SSH** (local Ollama):

```yaml
apiVersion: "v1"
protocol: "ssh"
address: ":2222"
description: "SSH Ollama Llama3"
commands:
  - regex: "^(.+)$"
    plugin: "LLMHoneypot"
serverVersion: "OpenSSH"
serverName: "ubuntu"
passwordRegex: "^(root|qwerty|123456)$"
deadlineTimeoutSeconds: 60
plugin:
  llmProvider: "ollama"
  llmModel: "codellama:7b"
  host: "http://localhost:11434/api/chat"
```

**Static SSH**:

```yaml
apiVersion: "v1"
protocol: "ssh"
address: ":22"
description: "SSH interactive"
commands:
  - regex: "^ls$"
    handler: "Documents Images Desktop Downloads .m2 .kube .ssh .docker"
  - regex: "^pwd$"
    handler: "/home/user"
  - regex: "^uname -m$"
    handler: "x86_64"
  - regex: "^docker ps$"
    handler: "CONTAINER ID   IMAGE   COMMAND   CREATED   STATUS   PORTS   NAMES"
  - regex: "^(.+)$"
    handler: "command not found"
serverVersion: "OpenSSH"
serverName: "ubuntu"
passwordRegex: "^(root|qwerty|Smoker666)$"
deadlineTimeoutSeconds: 60
```

### TELNET Deception Service

TELNET deception services emulate terminal-based devices (routers, switches, legacy systems) with full authentication flow and LLM integration.

**LLM-powered TELNET**:

```yaml
apiVersion: "v1"
protocol: "telnet"
address: ":23"
description: "TELNET LLM"
commands:
  - regex: "^(.+)$"
    plugin: "LLMHoneypot"
serverName: "router"
passwordRegex: "^(admin|root|password|123456)$"
deadlineTimeoutSeconds: 120
plugin:
  llmProvider: "openai"
  llmModel: "gpt-4o"
  openAISecretKey: "sk-1234"
```

**Static Cisco IOS simulation**:

```yaml
apiVersion: "v1"
protocol: "telnet"
address: ":23"
description: "Cisco IOS Router"
commands:
  - regex: "^show version$"
    handler: "Cisco IOS Software, Version 15.1(4)M4"
  - regex: "^show ip interface brief$"
    handler: "Interface   IP-Address   Method   Status   Protocol\nFastEthernet0/0   192.168.1.1   YES NVRAM   up   up"
  - regex: "^(.+)$"
    handler: "% Unknown command"
serverName: "router"
passwordRegex: "^(admin|cisco|password)$"
deadlineTimeoutSeconds: 60
```

### TCP Deception Service

TCP deception services cover binary and text-based protocols: databases, message brokers, directory services, remote access, and more. Supports banner-only mode, interactive regex matching, and LLM integration.

**Redis**:

```yaml
apiVersion: "v1"
protocol: "tcp"
address: ":6379"
description: "Redis 7.0.12"
commands:
  - regex: "^PING"
    handler: "+PONG\r\n"
  - regex: "^AUTH"
    handler: "-ERR Client sent AUTH, but no password is set\r\n"
  - regex: "^INFO"
    handler: "$180\r\n# Server\r\nredis_version:7.0.12\r\nos:Linux 5.15.0-76-generic x86_64\r\ntcp_port:6379\r\n\r\n"
  - regex: "^(.+)$"
    handler: "-ERR unknown command\r\n"
deadlineTimeoutSeconds: 60
serverName: "redis-prod-01"
```

**LDAP / Active Directory**:

```yaml
apiVersion: "v1"
protocol: "tcp"
address: ":389"
description: "Active Directory LDAP Domain Controller"
banner: "0\x84\x00\x00\x00\x10\x02\x01\x01\x61\x84\x00\x00\x00\x07\x0a\x01\x00\x04\x00\x04\x00"
commands:
  - regex: "\\x30.*\\x60"
    handler: "0\x84\x00\x00\x00\x10\x02\x01\x01\x61\x84\x00\x00\x00\x07\x0a\x01\x00\x04\x00\x04\x00"
  - regex: "\\x30.*\\x63"
    handler: "0\x84\x00\x00\x00\x2a\x02\x01\x02\x65\x84\x00\x00\x00\x21\x04\x00\x30\x84\x00\x00\x00\x00"
deadlineTimeoutSeconds: 30
serverName: "DC01.corp.local"
```

**LLM-powered PostgreSQL**:

```yaml
apiVersion: "v1"
protocol: "tcp"
address: ":5432"
description: "PostgreSQL 15.3"
commands:
  - regex: "^(.+)$"
    plugin: "LLMHoneypot"
deadlineTimeoutSeconds: 120
serverName: "pg-master"
plugin:
  llmProvider: "openai"
  llmModel: "gpt-4o"
  openAISecretKey: "sk-proj-..."
  prompt: "You are simulating a PostgreSQL 15.3 server. Respond to incoming TCP data as a PostgreSQL server would."
```

Additional example configurations are available in `configurations/services/` for Memcached, MS-SQL, SMB, RDP, VNC, and MQTT.

## Supported By

[![JetBrains logo.](https://resources.jetbrains.com/storage/products/company/brand/logos/jetbrains.svg)](https://jb.gg/OpenSourceSupport)

![gitbook logo](https://i.postimg.cc/VNQh5hnk/gitbook.png)
