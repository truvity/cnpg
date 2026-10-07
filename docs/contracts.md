# Value contracts

Each chart's `values.schema.json` is generated from a Pkl contract in
[`contracts/`](../contracts) with the generators of
[truvity/pkl-contracts](https://github.com/truvity/pkl-contracts) v0.5.0. The
contract is the single source; the committed schema is its output, and CI fails
when the two differ.

| Chart | Contract |
|---|---|
| `cnpg-cluster` | `contracts/cnpg-cluster/Values.pkl` |
| `cnpg-database` | `contracts/cnpg-database/Values.pkl` |
| `cnpg-platform` | `contracts/cnpg-platform/Values.pkl` |
| `cnpg-project-platform` | `contracts/cnpg-project-platform/Values.pkl` |
| `cnpg-projects` | `contracts/cnpg-projects/Values.pkl` |

`barman-cloud-crds` is not here: its (empty) schema belongs to the upstream CRD
mirror. `cnpg-client` is a library chart and has no schema.

## Working with a contract

```console
$ just contract         # regenerate charts/*/values.schema.json from contracts/
$ just contract-check   # what CI runs: regenerate aside, fail on any difference
```

Edit the contract, run `just contract`, commit both. Never edit a
`values.schema.json` by hand. `values.yaml` (the defaults) is still written by
hand; moving it into the contracts is a follow-up.

Pkl is run as `hack/pkl`, a pinned wrapper (Pkl 0.32.1, sha256-checked; the same
as `bin/pkl` of pkl-contracts) until nixpkgs ships Pkl 0.32. It lives in `hack/`
because `bin/` is ignored here. The contract packages are pinned by checksum in
`contracts/PklProject.deps.json`.

## Layout

One Pkl project at the repository root, not a `contract/` directory in each
chart:

- Helm packages a chart's directory, so Pkl sources inside a chart would ship in
  every `.tgz`.
- Types are shared (`Common.pkl`: the string map, Kubernetes names, the
  `scheduledBackups[]` and `serverTLS[]` shapes that `cnpg-project-platform` and
  `cnpg-projects` both take), so one project means one lock file and one set of
  pins.

```
contracts/
  PklProject, PklProject.deps.json   the four pkl-contracts packages, pinned
  Common.pkl                         types shared between charts
  Generate.pkl                       the command: one chart's contract -> values.schema.json
  Gaps.pkl                           what v0.5.0 cannot say yet (below)
  <chart>/Values.pkl                 the contract of one chart
```

A contract is a module annotated `@A.Chart`; its doc comment is the schema's
`description`, and a property's doc comment is that property's `description`. A
property that is not nullable is `required`; `X?` is optional. A string with a
rule is a local `typealias` carrying the hand-written pattern. `@A.Def` makes
a typealias a `definitions` entry reached by `$ref` (`stringMap`, `k8sName`,
`duration`). Counts are annotations: `@A.Items { min; unique }`,
`@A.Properties`, `@A.Range`.

## What v0.5.0 cannot say, and `Gaps.pkl`

The generated schema must accept and refuse exactly what the hand-written one
did. Five things in today's schemas are outside what the generator can say, so
`Gaps.pkl` applies them to its output; each goes away when the generator learns
it.

| Gap | Today's rule | What `Gaps.pkl` does |
|---|---|---|
| line-break guard | the generator pairs every `pattern` with `not: { pattern: <line breaks> }`; the hand-written patterns allow a line break (the `caCertificates` patterns are unanchored and match multi-line PEM) | removes the guard everywhere |
| `$defs` of the chart itself | `definitions` + `$ref` | adds the `@Def` types to the root `$defs` (v0.5.0 emits `$defs` for `@Schema` documents only, so the refs would dangle) |
| `instances` | `type: [integer, null]`, minimum 1 (null = profile default) | sets `type` to integer or null |
| `size` of a client certificate (2 places) | `type: [integer, null]` and `enum: [256, 384, 521, 2048, 3072, 4096, null]` | sets both |
| empty `$defs` | none | removed (the generator always writes one) |

## Parity with the hand-written schemas

This move changes no behaviour. How that was shown:

1. **Structure.** Both schemas with every `$ref` inlined, `description`/`title`
   left out and compared apart: zero differences in all five charts, and zero
   description differences.
2. **Fixtures.** Every golden case (`tests/cases/*`: 35 `helm template`
   renders, byte-identical output), every negative fixture of the five
   charts and of `cnpg-client` (`tests/invalid/*`: 100 files; the 101st is
   `barman-cloud-crds`, whose schema is not touched) and the `bogusKey` and
   `helm lint` checks of `just charts` give the same result with the old and the
   new schema. 32 of the negative fixtures
   are refused by the schema alone (the render succeeds with
   `--skip-schema-validation`), and all 32 are still refused.
3. **Differential run.** The Helm validation engine (santhosh-tekuri/jsonschema
   v6) asked both schemas about 1.09 million documents: every fixture, every
   fixture merged over the chart's defaults, and each of those with every value
   replaced in turn by 140 probes (wrong types, `null`, empty, boundary
   numbers, pattern near-misses, line breaks, a multi-line PEM, lists with a
   duplicate, an unknown key, the key removed). No verdict differed. The same run
   flags a schema with the line-break guard left in (a multi-line PEM refused)
   and one with `maxLength` off by one, so it can see a difference.

### What changed in each file, and why it does not matter

| Change | Where | Why behaviour is the same |
|---|---|---|
| `$schema` draft-07 to 2020-12 | all five | the keywords in use (`type`, `properties`, `required`, `additionalProperties`, `items` as one schema, `enum`, `pattern`, `min/maxLength`, `minimum`/`maximum`, numeric `exclusiveMinimum`/`exclusiveMaximum`, `minItems`, `uniqueItems`, `minProperties`, `$ref`, `anyOf`) mean the same in both; no `$ref` has a sibling keyword (draft-07 ignores one, 2020-12 applies it) |
| `definitions` to `$defs`, `#/definitions/x` to `#/$defs/x` | platform, project-platform, projects | the same three definitions each, resolved against the document root |
| `oneOf` to `anyOf` | `cnpg-platform`: `metricsNetworkPolicy.namespaces[]` | the members are a non-empty string and an object; no value is both, so exactly-one and at-least-one accept the same documents |
| a string `enum` loses `type: string` | cluster 5, database 10, platform 6, project-platform 7, projects 4 | every member is a string, so `enum` alone admits the same values |
| `x-set-at-install: true` added | `serverTLS[].caCertificates` (project-platform, projects), `clusters[].values`, `backupAccess.roles[].serviceAccounts` (projects) | an unknown keyword, ignored by validators. The contract needs it to make a list or an open object that has no default `required`, which today's schemas already say |
| root `$defs` order, property order | all | JSON objects are unordered |
| unused `definitions` | none dropped | |

The `description` of every property and every schema is identical.

## Differences from the vocabulary, kept on purpose

The contract keeps today's rule wherever the vocabulary differs. Each is a
follow-up: change the rule, deliberately, in a PR of its own.

- Durations are local patterns, not the vocabulary types: `cnpg-platform`'s
  `^[0-9]+(ms|s|m|h|d|w|y)$` is narrower than `PromDuration` (one part, no
  `1h30m`); the project charts' `^[0-9]+(ns|us|ms|s|m|h)(...)*$` is narrower than
  `Duration` (no decimals, no `µs`). `cnpg-cluster` and `cnpg-database` take
  durations as plain strings (`duration`, `renewBefore`, `interval`).
- Names are local: `k8sName` has the pattern of `DnsName` with a length of 1 to
  253; `DnsSubdomain` is stricter. `DnsLabel` is used for `trust.policy.requester`
  because it is exactly the same rule.
- `""` is a member of several enums (`profile`, the compression and encryption
  settings) and of two patterns, and `values.yaml` defaults many strings to `""`
  where the vocabulary says "absent".
- `cnpg-projects` `clusters[].values` is an open object, validated at render time
  by the `cnpg-cluster` rules; it can be closed with the `cnpg-cluster` contract.
- `instances` and a certificate `size` admit `null`; the vocabulary never does.
- A few patterns spell `.`, `\S` and `\s` (the object-store prefix, the cron
  schedule, the e-mail address), which the vocabulary avoids because engines read
  them differently; they are kept as written.
