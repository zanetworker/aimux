# The `parts:` section of the config file

What the owner writes to say which parts of aimux are on, and how. This page fixes its shape before anything
reads or writes it. The order of building is in the [stage 2 plan](../plans/2026-11-25-stage-2-one-way-in.md);
why there is one file at all is in the [product-shape draft](2026-11-25-aimux-product-shape-draft.md)
("One configuration, four ways to change it").

<!-- no-images: the page is examples of one file; a figure would repeat them -->

## The shape

```yaml
# ~/.aimux/config.yaml
parts:
  inference-proxy:
    enabled: true
    port: 7385
    engines:
      codex:
        service_tier: priority
      anthropic:
        enabled: true
      enmaas:
        type: messages
        url: https://api.enmaas.devshift.net
        enabled: true
        key: {keychain: {item: enmaas-key}}
        enabled: true
  mcp-proxy:
    enabled: true
```

A part has `enabled`, which every part that is listed must say, and the few settings that are true of the whole
part. The inference proxy also has `engines`: a map from an engine's name to its settings.

| Key | Under | Meaning | When left out |
|---|---|---|---|
| `enabled` | a part | `true`: aimux keeps the part's service running. `false`: aimux keeps it stopped | Not allowed. A part that is listed says one or the other; a part that is not listed is not managed |
| `port` | `inference-proxy` | The port on 127.0.0.1 | What the installed job has; 7385 for a job aimux creates |
| `engines` | `inference-proxy` | The engines the proxy can answer with, by name | What the installed job has; Codex only for a job aimux creates |
| `type` | an engine | How aimux talks to it: `codex`, `anthropic`, `messages` | The name, for `codex` and `anthropic`. Required, as `messages`, for any other name |
| `enabled` | an engine | Requests naming it are answered | On, for an engine that is listed. `false` on `codex` is refused: it cannot be turned off |
| `url` | a `messages` engine | Where it is. A bare origin; `https` unless on this machine | Required |
| `key` | a `messages` engine | Where its key is kept, in the form the other files use: `{keychain: {item: ...}}`, `{env: {name: ...}}`, `{file: {path: ...}}`, `{cmd: {argv: [...]}}` | `{keychain: {item: <name>-key}}`. `aimux engine add` writes it out, so renaming the engine later does not change which key it uses |
| `service_tier` | the `codex` engine | `priority` or `standard` | What the installed job has; `priority` for a job aimux creates |
| `binary` | the `codex` engine | The Codex program to run | What the installed job has; otherwise `codex-lean` when installed, else `codex` |

## The rules the shape follows

Each key above is where it is for one of these reasons. A new key has to pass them too.

1. **The file describes the thing, not the command line.** `aimux model proxy serve --codex-command` is a flat
   flag because the proxy began with one engine. In the file the same setting is `engines.codex.binary`. aimux
   turns the file into whatever flags a service takes; the flags can change without the file changing.
2. **A setting lives with what it is true of.** The port is the proxy's. The service tier is Codex's. The address
   is one upstream's. Nothing that belongs to one engine sits at the proxy's level.
3. **A name and a kind are two things.** The name is what the owner types before the colon in a model name
   (`enmaas:claude-opus-5-5`), and is the map key. The kind is `type`. Two engines of one kind have two names.
   The names `codex` and `anthropic` are kept for the two that are built in, where the name says the kind and
   `type` can be left out; any other name says `type: messages`.
4. **Named things are map keys, as everywhere else in aimux's files.** `servers`, `sets`, `sources`,
   `environments`, `runtimes` and `providers` are all maps from a name to its settings, several with a `type`
   inside. A map cannot hold a name twice; one engine is one place in the file, which is what lets a command
   change one value; and order means nothing here, since the model name picks the engine.
5. **Order appears only where order matters.** Trying one engine and then another is routing, which is stage 4.
   It will be a list of names that refers to this map, somewhere else in the file. It is not a reason to make
   this a list.
6. **On and off are separate from existing.** `enabled: false` keeps an engine's `url` and `key`, so it can be
   turned on again without retyping them. Removing the entry forgets it.
7. **A part has three states, and the file says which.** Not listed: aimux neither starts nor stops it.
   `enabled: true`: aimux keeps it running. `enabled: false`: aimux keeps it stopped. A part that is listed
   without `enabled` is refused, so an edit that looks valid cannot quietly do nothing, and adding the first
   part to the file cannot stop another. An engine is different on purpose: listing one turns it on, because
   the part that holds it already decides whether anything runs.
8. **Taking a part under management changes nothing.** For a job that exists, a setting left out means "as it
   is installed": its port, its Codex program, its engines. Defaults are only for a job aimux creates. So
   `inference-proxy: {enabled: true}` on a proxy running with a custom port keeps that port.
9. **A name that is known and off is refused, never answered by something else.** A request naming
   `lab:model` when `lab` is disabled, or naming a prefix nobody defined, gets an error that says so. It does
   not fall through to Codex or Anthropic, with or without other upstreams configured. The proxy does not do
   this today when no upstream is configured; it is part of this stage.
10. **No secret is in the file, or in a job file.** A key is a reference, in exactly the form `servers.yaml`
    uses (`internal/secretref`). On another machine or in a container the reference changes (`env`, `file`) and
    nothing else does. The proxy is given the reference and reads the key itself; the value is never an
    argument of its job.
11. **The words are the ones already in use.** `url` for an address, `enabled` for a switch, `type` for a kind,
    `binary` for one program (as `providers.<name>.binary`; `command` in `servers.yaml` is a list of
    arguments), `port`, snake case.

## The file in five situations

**The owner today: Codex through the proxy, nothing else.** Nothing has to be written. With no `parts:`
section aimux manages no job, and everything runs as it does now. Taking the proxy under management is one
line, and changes nothing about how it runs:

```yaml
parts:
  inference-proxy:
    enabled: true
```

**Claude and Codex models in one session.**

```yaml
parts:
  inference-proxy:
    enabled: true
    engines:
      anthropic:
        enabled: true
```

**The internal gateway as well, and a second one being tried.**

```yaml
parts:
  inference-proxy:
    enabled: true
    engines:
      anthropic:
        enabled: true
      enmaas:
        type: messages
        url: https://api.enmaas.devshift.net
      lab:
        type: messages
        url: https://lab.example
        key: {keychain: {item: lab-gateway}}
```

`enmaas`, written by hand here, reads its key from the Keychain item `enmaas-key` because nothing says
otherwise. `lab` names its own.

**The gateway off for a day.** One value changes, and the address stays:

```yaml
      enmaas:
        type: messages
        url: https://api.enmaas.devshift.net
        enabled: false
```

**A new machine, or a container.** The same file. Only where a key is kept differs, and that is one line per
engine:

```yaml
      enmaas:
        type: messages
        url: https://api.enmaas.devshift.net
        key: {env: {name: ENMAAS_KEY}}
```

## What each way of changing it does

| The owner | The file |
|---|---|
| `aimux enable inference-proxy`, `aimux disable inference-proxy` | Sets `parts.inference-proxy.enabled` |
| `aimux engine enable anthropic` | Sets `engines.anthropic.enabled: true`, adding the entry if it is not there |
| `aimux engine add enmaas=https://...` | Adds the entry with `type: messages`, its `url`, and its `key` written out |
| `aimux engine disable enmaas` | Sets `enabled: false` on it; nothing else changes |
| `aimux engine remove enmaas` | Removes the entry |
| Edits the file, then `aimux setup --apply` | Nothing; aimux reads it |

`disable codex`, `remove codex`, `add codex` and `add anthropic` are refused, each with the command that was
meant. A command changes the text of one value and leaves the rest of the file as the owner wrote it.

## What the shape has to survive

- **Routing per task (stage 4).** A rule will say which engine and model a kind of request goes to. It names
  engines by the keys of this map, in a list where order matters, and lives beside the map (`routing:` under
  the part). This map does not change.
- **A project that narrows.** A project's `.aimux/config.yaml` may list the engine names it allows. What a
  session in that project can reach is that list, cut down to the engines enabled here and to the destinations
  the owner has approved for that project; the built-in Codex route and any fallback are cut down the same way.
  A project can never add an engine. An approval is for a name and its `url` together: when the `url` under an
  approved name changes, the owner is asked again. This page fixes only that the names are the join; the
  approvals are designed with routing.
- **A new kind of engine.** An address that speaks another dialect is a new value of `type` with its own
  keys. Existing entries do not change.
- **Another part.** Phone access, sandboxes, schedules and evals each get a key under `parts:` with `enabled`
  and their own settings. What they hold today under other top-level keys (`remote_access`, `otel`) keeps
  working; nothing is moved in this stage.

One thing the shape does not give, though it looks as if it might: a second Anthropic account. The Anthropic
engine sends the caller's own login and takes plain model names, so a second entry of that type would select
nothing. The names `codex` and `anthropic` are reserved for now.

## Not decided here

- **The collector.** `otel.enabled` and `otel.port` exist today, and the loader reads the port only when that
  switch is on. How `parts.collector` relates to them, and which wins when they disagree, is not settled, so
  this stage does not put the collector under `parts:`. Its job stays as it is.
- **The MCP proxy's port.** Its installer records the port in its own manifest. Until that and the file are
  one statement, `parts.mcp-proxy` has `enabled` only.
- **How the proxy is handed a key reference.** `serve` looks up `<name>-key` itself today. It needs to be told
  a reference, by a flag that carries the reference and never the key, or by reading this section itself. The
  plan decides which.
- **What an engine turned off means for a session already on it.** The next request naming it is refused;
  what the client shows was not tried.
- **A `type` this build does not know.** Proposed: the file is refused with the line, and nothing is applied.

## Where a second opinion changed this page

Codex (gpt-6.2-astra) reviewed the first version and kept the shape: named engines as map keys with a `type`,
settings placed with what they are true of. From its findings this page took: a disabled or unknown engine
name is refused and never answered by another engine; a setting left out means "as installed" when a job
exists; `enabled` is required on a part that is listed; a project's narrowing is more than a list of names;
the key reference is `internal/secretref` exactly, written out by `add`; the built-in names are reserved and
the second-account claim is dropped; the collector is left out until its two switches are reconciled; and
`binary`, not `command`.
