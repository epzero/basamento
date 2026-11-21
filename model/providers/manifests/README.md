# Provider manifests

A provider is usually data, not code: one YAML manifest per vendor says which
models it serves and how to reach it (docs/architecture/model-layer.md §3). A
vendor with an HTTP API uses the `http` driver; a tool on the machine uses the
`cli` driver. Only a protocol a manifest cannot express gets an adapter package
of its own, and every provider ships a simulator and the same contract tests.

Each vendor is one task: add its manifest here, a simulator fixture and a
contract test that drives the simulator. Never point a gate test at a live
endpoint.

## Shape

```yaml
name: vendor
locality: cloud            # cloud | machine_local | worker
protocol: poll             # single | poll | webhook for http; command for cli
models:
  - id: vendor/model
    caps: [image.generate]
    params_schema: {type: object}
    inputs: [{name: prompt, media: text, required: true}]
http:
  base_url: https://api.vendor.test/v1
  credential: cred://vendor # resolved per request; never written here
  auth: {header: Authorization, scheme: Bearer}
  start:
    method: POST
    path: /jobs
    body: '{"prompt": (.text.prompt // .params.prompt)}'
    response: {id: .id}
  status:
    path: /jobs/{id}
    response:
      state: .state
      state_map: {queued: running, running: running, done: succeeded, error: failed}
      progress: .progress
      outputs: '.files | map({url: .url, media: "image"})'
      usage: .usage
      cost: .cost_mpts
      error: .message
      code: .code
  cancel: {method: POST, path: /jobs/{id}/cancel}
  lookup:
    path: /jobs
    query: {client_ref: .client_ref}
    response: {id: .id}
```

Every mapping is gojq over one input document: `model`, `op`, `client_ref`,
`id`, `base_url`, `workdir`, `params`, `messages`, `tools`,
`output_schema`, `text`, `texts`, `assets` and `seed`. `path` may contain
`{id}`. A status call must map `state` for the poll protocol; the first
mapped state that is not `running` ends the invocation.

A `cli` manifest adds `command` (argv prefix), `args` (a gojq expression
producing the rest of argv), `dir`, `env`, `outputs` (file globs, relative to
the invocation directory `dir/runs/<client-ref>-<hash>`, which is also the
command's working directory) and `progress` (a regular expression with one
number group, matched against stdout lines and reported as the number over
100).

The HTTP driver carries `single`, `poll` and `webhook` today. A WebSocket
vendor needs an adapter until the driver carries `socket`; the protocol is
still one invocation through the same contract.

## Shipped manifests

| Manifest | Driver | Status |
| --- | --- | --- |
| `openai-images.yaml` | http, single | Image generation; it needs a credential `cred://openai`. |
| `runway-video.yaml` | http, poll | Image-to-video submit and poll; it needs `cred://runway` and an `AssetResolver` that signs input images. |

With an `AssetResolver`, every asset object in the mapping input also carries a
`url` the vendor can fetch; without one, only `blob_key` and `sha256` are
visible.
