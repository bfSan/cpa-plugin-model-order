# model-order

A thin [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (CPA) plugin that gives the
model listing endpoints a stable, configured order.

## Why this exists

CPA builds `/v1/models` by ranging over a map keyed by model ID and never sorts the
result, so the served order is Go's randomised map order. It is frozen into the
registry cache and then reshuffled whenever that cache is invalidated, which model
level cooldowns do routinely. There is no native sort knob, and no field anywhere in
CPA that stores an order.

This plugin takes the one seam CPA does provide: the response interceptor, which runs
on model list bodies **after** `oauth-model-alias` substitution. That makes it the
single place where the final, cross provider, aliased list can be ordered once,
instead of every provider plugin ordering its own slice of it.

## Install

Build the plugin on the CPA host (it is `-buildmode=c-shared`, so build it where it
runs):

```bash
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared -ldflags "-s -w" -o model-order.so .
install -m 755 model-order.so /opt/cpa/plugins/model-order.so
```

Then register it and, importantly, give it the **lowest** priority so it runs last in
the interceptor chain. CPA orders interceptors by `priority` descending and then by
plugin ID ascending, and the last plugin to return a body decides the order:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    model-order:
      enabled: true
      priority: -100
```

Restart CPA afterwards. With no `order` configured the built-in rule applies.

## Configuration

```yaml
model-order:
  enabled: true
  priority: -100
  strategy: grouped        # grouped (default) | name
  case_sensitive: false
  order:                   # first match wins, one bucket per pattern
    - "auto"
    - "*-auto"
    - "gpt-*"
    - "codex-*"
```

| Key | Default | Meaning |
|---|---|---|
| `strategy` | `grouped` | `grouped` puts configured buckets first; `name` sorts the whole list by model ID. |
| `order` | built in | Ordered glob patterns matched against the model ID. Models matching nothing tail the list alphabetically. |
| `case_sensitive` | `false` | Match and compare model IDs case sensitively. |

Within one bucket, and in the unmatched tail, entries sort alphabetically by model ID.

### Pattern syntax

| Pattern | Matches |
|---|---|
| `auto` | the exact ID `auto` |
| `gpt-*` | prefix |
| `*-auto` | suffix, so `qoder-auto` matches and `codex-auto-review` does not |
| `*kimi*` | substring |
| `qoder-*-*` | general glob, segments in order |
| `*` | ignored; it would swallow the whole list into one bucket |

Because CPA applies aliases before the list reaches this plugin, both spellings of a
preset are worth listing when you write your own `order`: a deployment that prefixes
every alias per provider serves `qoder-auto`, an unprefixed one serves `auto`.

### Built-in order

Aggregate presets, in tier order, then the GPT and Codex families, then everything
else alphabetically:

```text
auto / auto-* / *-auto
default / default-* / *-default
balanced / balanced-* / *-balanced
fast / fast-* / fast-model / *-fast / *-fast-model / *-fast-*
deep / deep-* / deep-model / *-deep / *-deep-model / *-deep-*
hybrid / hybrid-* / *-hybrid
efficient / *-efficient
performance / *-performance
ultimate / *-ultimate
gpt-*
codex-*
```

## Safety

The plugin is deliberately conservative: a model listing it cannot parse with
certainty is left exactly as CPA produced it, so a listing endpoint can never be
emptied or corrupted by an ordering bug.

* Only responses that CPA produced through `WriteModelListResponse` are touched. That
  call site is the only one that leaves `model`, `requested_model`, the original
  request and the request body all empty, which keeps chat traffic off this path.
* Only the model array is reordered. Its elements are moved verbatim: the plugin splices
  the array back into the original bytes rather than re-encoding the document, so
  nothing else in the response changes shape, key order or number formatting.
* An array whose entries carry no `id` or `name`, a document with no recognisable
  array, or a single entry list all result in a no-op.
* A rejected configuration keeps the previously active order in place rather than
  falling back to defaults mid-flight.

## Coverage

All three listing formats are handled: the OpenAI port (`{"object":"list","data":[…]}`
with `id`), the Anthropic port (`data` with `id`) and the Gemini port
(`{"models":[…]}` with `name`).
