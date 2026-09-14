# NeMo Guardrails Integration

This guide covers configuring [NeMo Guardrails](https://docs.nvidia.com/nemo/guardrails/) so the gateway inspects `tools/call` requests and responses before they reach backends or clients.

## Overview

The router sends each checked call to a NeMo Guardrails server at `POST {url}/v1/checks`. NeMo returns `passed`, `modified`, or `blocked`. The gateway does not deploy NeMo. A running server must already be reachable from the broker-router pods.

Key concepts:

- **Global guardrails** — one guardrails server per gateway, applied to every registered MCP server
- **Per-server config IDs** — extra NeMo config IDs merged with the global list (global IDs first). They cannot remove a global policy
- **Fail closed by default** — when the guardrails server is unreachable, tool calls are rejected
- **Request and response checks** — arguments are checked before the backend runs; text in the tool result is checked before the client sees it

Checks run only when at least one config ID is in effect. A Secret with an empty `configIDs` list does not inspect traffic until a server adds its own IDs.

Client `Authorization` headers are not forwarded to NeMo. NeMo authenticates to its own model backend.

## Prerequisites

- [MCP Gateway installed](./how-to-install-and-configure.md)
- A NeMo Guardrails server reachable from the broker-router pods, exposing `POST /v1/checks`
- The Secret and `MCPGatewayExtension` in the same namespace

Examples below use the `mcp-system` namespace. Use the namespace of your `MCPGatewayExtension`.

## Step 1: Create the guardrails Secret

The Secret type must be `guardrails/external/nemo` and it must have the label `mcp.kuadrant.io/secret: "true"`. The `config.yaml` key holds the server settings.

`url` must be an absolute `http` or `https` URL. Cluster DNS names are accepted. Literal loopback and private addresses, including `localhost`, are rejected.

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: guardrails-config
  namespace: mcp-system
  labels:
    mcp.kuadrant.io/secret: "true"
type: guardrails/external/nemo
stringData:
  config.yaml: |
    url: http://nemo-guardrails.mcp-system.svc.cluster.local:8000
    configIDs:
      - "tool-safety-v1"
    model: "meta/llama-3.1-8b-instruct"
    failMode: deny
EOF
```

| Field | Required | Description |
| --- | --- | --- |
| `url` | yes | NeMo Guardrails server endpoint. Scheme must be `http` or `https` |
| `configIDs` | no | Default config IDs applied to every server. Empty means no global check |
| `model` | yes | Model identifier sent on every check |
| `failMode` | no | `deny` (default) or `allow` when the guardrails server cannot be reached or returns an unusable response |

Verify the Secret:

```bash
kubectl get secret guardrails-config -n mcp-system
```

## Step 2: Annotate the MCPGatewayExtension

```bash
kubectl annotate mcpgatewayextension mcp-gateway -n mcp-system \
  mcp.kuadrant.io/guardrails-ref=guardrails-config
```

The controller validates the Secret and writes the resolved config into the gateway config Secret. The broker-router then starts checking tool calls.

Verify the `GuardrailsResolved` condition:

```bash
kubectl get mcpgatewayextension mcp-gateway -n mcp-system \
  -o jsonpath='{.status.conditions[?(@.type=="GuardrailsResolved")]}{"\n"}'
```

`status` should be `True` and `reason` should be `GuardrailsSecretResolved`. `Ready` stays `True` when the rest of the extension is valid.

If the Secret is missing, both `Ready` and `GuardrailsResolved` become `False` with reason `GuardrailsSecretNotFound`. A Secret that exists but is invalid (wrong type, missing label, bad `config.yaml`) uses reason `GuardrailsSecretInvalid`. While that condition is `False`, every `MCPServerRegistration` on the gateway is set to `NotReady` with reason `GatewayGuardrailsNotConfigured` and removed from the gateway config.

## Step 3: Test a tool call

The router sends the tool name and arguments to NeMo before routing to the backend. A `blocked` verdict returns HTTP 403 and a JSON-RPC error. The message is `blocked by guardrails`.

```bash
curl -X POST https://<your-gateway-host>/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
      "name": "execute_sql",
      "arguments": {"query": "DROP TABLE users"}
    }
  }'
```

A blocked call returns:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32000,
    "message": "blocked by guardrails"
  }
}
```

If NeMo returns `modified`, the gateway forwards the rewritten arguments to the backend.

Accepted elicitation responses are checked the same way. Decline and cancel are not checked.

## Step 4: (Optional) Add per-server config IDs

Add `mcp.kuadrant.io/guardrails-config-ids` on an `MCPServerRegistration` to extend the global policy. IDs are comma-separated. The gateway lists global IDs first, then per-server IDs, and drops duplicates.

```bash
kubectl annotate mcpserverregistration dangerous-server -n mcp-system \
  mcp.kuadrant.io/guardrails-config-ids=strict-input-checking,pii-detection
```

> **Note:** Per-server IDs require `mcp.kuadrant.io/guardrails-ref` on the MCPGatewayExtension. Without it, the server is `NotReady` with reason `GatewayGuardrailsNotConfigured`. An annotation that is set but contains no IDs is also rejected.

If the Secret's `configIDs` list is empty, only servers that set this annotation are checked.

Verify the server:

```bash
kubectl get mcpserverregistration dangerous-server -n mcp-system
```

## Response checks

After the backend returns a `tools/call` result, the router sends the joined `content[].text` values to NeMo. Image and resource content is not sent. A backend JSON-RPC error has no tool text, so it is not checked. A body that cannot be parsed is rejected.

| NeMo status | What the client receives |
| --- | --- |
| `passed` | The original result |
| `modified` | A tool result whose text is the rewritten content. An upstream `isError: true` result stays an error |
| `blocked` | A tool result with `isError: true` and text `blocked by guardrails` |

A blocked response looks like:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "content": [{"type": "text", "text": "blocked by guardrails"}],
    "isError": true
  }
}
```

For `text/event-stream` responses, earlier SSE events (progress, elicitation) are forwarded as they arrive. Only the final tool result is held for the check. For `application/json`, the whole body is held until the check finishes.

## Fail modes

`failMode` applies when the guardrails server is unreachable, times out, returns a non-2xx status, or returns a body the gateway cannot parse. Each check waits up to 8 seconds.

| `failMode` | Guardrails server unreachable or unusable | Request the gateway cannot translate |
| --- | --- | --- |
| `deny` (default) | Rejected. Requests return HTTP 503 with message `guardrails check unavailable`. Responses return an `isError` tool result with the same message | Always rejected. Requests return HTTP 400 with message `guardrails check failed`. Responses return an `isError` tool result with the same message |
| `allow` | The call proceeds without a verdict | Still rejected |

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: guardrails-config
  namespace: mcp-system
  labels:
    mcp.kuadrant.io/secret: "true"
type: guardrails/external/nemo
stringData:
  config.yaml: |
    url: http://nemo-guardrails.mcp-system.svc.cluster.local:8000
    configIDs:
      - "tool-safety-v1"
    model: "meta/llama-3.1-8b-instruct"
    failMode: allow
EOF
```

> **Note:** `failMode` only covers the guardrails HTTP call. If the router itself is down, Envoy still rejects the request.

## Body size limits

`spec.maxBodyBytes` on the MCPGatewayExtension caps how much of a request or response the router buffers. The default is 5242880 (5 MiB). The controller sets the gateway listener's `per_connection_buffer_limit_bytes` to the same value.

Bodies over the limit are rejected regardless of `failMode`:

- Requests return HTTP 413
- Responses return an `isError` tool result with text `response body exceeds configured size limit`

For SSE responses the limit applies per event, not to the whole stream. For JSON responses it applies to the whole body.

```bash
kubectl patch mcpgatewayextension mcp-gateway -n mcp-system --type merge -p '
spec:
  maxBodyBytes: 10485760
'
```

## TLS

HTTPS checks use the system trust store plus the gateway CA bundle from `caCertBundleRef`, when that field is set. Add the guardrails server CA to that bundle. There is no separate guardrails CA field. `http` URLs need no TLS configuration.

See [Custom CA Certificates](./custom-ca-certificates.md).

## Removing guardrails

Remove the annotation to disable guardrails for the gateway:

```bash
kubectl annotate mcpgatewayextension mcp-gateway -n mcp-system \
  mcp.kuadrant.io/guardrails-ref-
```

The controller clears the guardrails config. `GuardrailsResolved` is removed, and servers return to normal operation.

If the Secret is deleted or becomes invalid while the annotation is still set, `GuardrailsResolved` stays `False` and every server on that gateway stays `NotReady`. Recreate the Secret or remove the annotation.

## Next steps

- **[Custom CA Certificates](./custom-ca-certificates.md)** — trust a private CA for the guardrails server
- **[Authorization](./authorization.md)** — control which callers can invoke a tool
- **[Troubleshooting](./troubleshooting.md)** — debug gateway configuration
