# terra-translate — Full Audit: 31 Issues, Ranked

> **Status: this is a historical audit, not a description of the current code.**
>
> It was written against an earlier version and is kept because the reasoning
> behind each issue is still worth reading. Everything below is in the present
> tense, but several Tier 1 and Tier 2 issues have since been fixed. The table
> immediately below records what was re-checked against the code, and when.
>
> Verified 2026-08-02:
>
> | Issue | Claim | Status now |
> |---|---|---|
> | F1 | `translateResource()` returns exactly one target, so 1:N is impossible | **Fixed.** It returns `[]*ir.TargetResource`, and `ResourceMapping.Expand` drives fan-out — see `expandAWSSGToGCPFirewall` and `expandAWSDBToGCPSQL` in `internal/translator/mappings.go`. |
> | F2 | The reference graph is never rewritten | **Fixed.** `translator.Translate` builds an `ir.RefGraph` in pass 1 and applies `rewriteRefs` in pass 2; `internal/ir/types_test.go` covers the rewriting rules. |
> | F3 | Accuracy measures coverage, not correctness | **Partly addressed.** `TranslationScore` now carries separate coverage, validity, and semantic ratios, combined as `0.40/0.35/0.25`. The CLI's `-min-accuracy` gate is still coverage-only, which is documented in `README.md`. |
> | F4 | The PID loop cannot exceed what the mapping tables provide | **Still true by design.** The loop tunes fuzzy-matching effort; it cannot invent a mapping that does not exist. Catalog-generated candidates widen the table, they do not remove the ceiling. |
> | B1 | `Property.Mapped` mutation corrupts the feedback loop | **Fixed.** Mapping counts accumulate in a per-run `ResourceAccuracy` value; no `Property.Mapped` mutation remains. |
> | B2 | Nested block codegen only handles two levels | **Fixed.** `buildBlockTree`/`writeBlockTree` in `internal/codegen/generator.go` recurse to arbitrary depth. |
> | B3 | `required_providers` emits malformed HCL | **Fixed.** Every test in `internal/codegen/generator_test.go` re-parses the generated file with `hclparse` and fails if it is not valid HCL. |
> | B4 | `evalExpr` with a nil context silently drops expressions | **Behaviour changed.** Expressions that cannot be evaluated statically now become explicit `TODO: ... requires manual translation` markers rather than disappearing. They are still not translated. |
> | B5 | `hclwrite.Tokens` written with a zero-value `Type` field | **Still present, no observed effect.** Raw tokens are still constructed this way, and generated output is verified parseable by both the codegen tests and `terraform fmt -check` in CI. |
>
> The **Tier 3 semantic gaps (S1–S8) all still apply.** They are properties of
> the clouds, not defects in this code, and they are the reason generated
> configuration is a review artifact rather than something to apply. The Tier 4
> PID observations remain accurate descriptions of the control design.

---

## TIER 1: Fatal Design Flaws (the tool produces wrong output)

### F1. 1:1 resource mapping assumption is structurally broken

The entire translator assumes one source resource → one target resource.
Real cross-cloud mappings are routinely 1:N or N:1:

| Source (AWS) | Target (GCP) | Cardinality |
|---|---|---|
| `aws_instance` with inline `ebs_block_device` | `google_compute_instance` + `google_compute_disk` + `google_compute_attached_disk` | 1:3 |
| `aws_security_group` with inline `ingress` blocks | multiple `google_compute_firewall` rules (one per direction/protocol) | 1:N |
| `aws_s3_bucket` + `aws_s3_bucket_policy` + `aws_s3_bucket_acl` (post-v4 provider) | `google_storage_bucket` + `google_storage_bucket_iam_binding` | N:M |
| `aws_route_table` + `aws_route` | `google_compute_route` (one per route) | 1:N |
| `aws_db_instance` | `google_sql_database_instance` + `google_sql_database` + `google_sql_user` | 1:3 |

**Current code:**  `translateResource()` returns exactly one `*ir.TargetResource`. There's no way to express fan-out.

**Fix:** `translateResource()` must return `[]*ir.TargetResource`. A `ResourceMapping` must include an optional `Expand` function:

```go
type ResourceMapping struct {
    SourceType   string
    TargetType   string       // primary target
    LogicalClass ir.ResourceClass
    AttrMaps     []AttrMapping
    // Expand is called when a single source resource must produce
    // multiple target resources. If nil, 1:1 mapping is used.
    Expand       func(*ir.Resource) []*ir.TargetResource
}
```

---

### F2. Reference graph is never rewritten

When `aws_subnet.app.vpc_id = aws_vpc.main.id`, the parser stores `"${aws_vpc.main.id}"`.
The translator maps the *resource* to `google_compute_subnetwork` and the *attribute* to `network` — 
but the *reference value* still says `aws_vpc.main.id`.

The generated HCL emits: `network = aws_vpc.main.id`

This is invalid. It must be: `network = google_compute_network.main.self_link`

**Current code:** `setNestedAttr()` writes `prop.Value` verbatim. There is zero traversal rewriting.

**Fix:** Build a reference-rewrite table from the resource translations:

```go
type RefRewriter struct {
    // "aws_vpc.main" → "google_compute_network.main"
    resourceRenames map[string]string
    // "aws_vpc.main.id" → "google_compute_network.main.self_link"
    attrRenames     map[string]string
}
```

Apply it to every string value that starts with `${` before emitting.

---

### F3. Accuracy metric is a vanity metric — measures coverage, not correctness

The PID loop optimises `mapped_attrs / total_attrs`. But "mapped" only means
"a target attribute name was found." It says nothing about whether:

- The **value** is valid in the target provider (`ami-0c55b159cbfafe1f0` is meaningless on GCP)
- The **type** is correct (AWS `cidr_block` string → GCP `auto_create_subnetworks` bool)
- The **semantics** are preserved (`multi_az = true` does NOT mean the same as `availability_type = "REGIONAL"` in all cases)

A mapping can be 100% "accurate" and produce completely invalid Terraform.

**Fix:** Split accuracy into three signals fed to the PID controller:

```go
type TranslationScore struct {
    CoverageRatio   float64 // attrs mapped / attrs total (current metric)
    ValueValidRatio float64 // attrs with valid target values / attrs mapped
    SemanticRatio   float64 // attrs with preserved semantics / attrs mapped
}
```

The PID setpoint should be a weighted composite, not just coverage.

---

### F4. The PID loop cannot improve beyond what the mapping tables provide

If `aws_db_instance.password` has no AttrMap entry, and no fuzzy match finds one,
then no amount of iteration will map it. The integral term (`LearnFromMissed`) only
searches *within the existing AttrMaps* for normalised-name matches:

```go
for _, am := range rm.AttrMaps {
    if normaliseName(am.TargetAttr) == bare {
        t.learnedMappings[learnKey] = am.TargetAttr
```

This means it can only rediscover mappings that already exist in the table.
If the table doesn't mention `password` at all, the loop converges at the same
accuracy it started at. The "feedback" doesn't actually learn anything new.

**Fix:** The integral term needs an external knowledge source:

1. Load the target provider's full attribute schema (`terraform providers schema -json`)
2. Match unmapped source attrs against ALL target attrs (not just the declared AttrMaps)
3. Use Levenshtein distance or embeddings-based similarity, not just suffix matching

---

## TIER 2: Code Bugs (the code doesn't do what it says)

### B1. `Property.Mapped` mutation corrupts the feedback loop

```go
// translator.go:130
prop.Mapped = true
```

This mutates the *source IR*, not a copy. On iteration 2, the IR already has
`Mapped = true` from iteration 1, but `acc.Total` is recounted from `len(res.Properties)`.
The count is right but the state is stale — if a different effort level changes which
attrs are mapped, the `Mapped` flags from the previous iteration are still set.

**Fix:** Never mutate the IR. Track mapped state in the `Result`, not the source.

---

### B2. Nested block codegen only handles 2 levels deep

`appendNestedBlocks` does `SplitN(dotPath, ".", 2)` → one block + one attr.
But GCP Cloud SQL needs:

```hcl
settings {
  backup_configuration {
    enabled = true
  }
}
```

That's `settings.backup_configuration.enabled` — 3 levels.
The current code produces `settings { backup_configuration_enabled = true }`.

**Fix:** Recursive block building from dotted paths.

---

### B3. `required_providers` block emits malformed HCL

```go
reqBody.SetAttributeRaw(g.targetProvider, hclwrite.Tokens{
    {Bytes: []byte(fmt.Sprintf("{ source = %q }\n", providerSource(g.targetProvider)))},
})
```

`SetAttributeRaw` with a single token containing `{ source = "..." }` is not how
`hclwrite` works. The token type isn't set (defaults to `TokenNil`). This will either
panic or emit unparseable output.

**Fix:** Use proper hclwrite block API or emit the entire terraform block as raw text.

---

### B4. `evalExpr` with nil context silently drops most real-world expressions

Any expression using `var.`, `local.`, `data.`, `each.`, `count.` returns
`${unknown_expr}` because the eval context is nil. In the example module:

- `aws_vpc.main.id` → `${aws_vpc.main.id}` ✓ (handled by ScopeTraversalExpr)
- `"${var.region}"` (inside a template) → `${unknown_expr}` ✗
- `lookup(local.sizes, var.env)` → `${lookup(...)}` ✗

**Fix:** For template expressions, walk the parts and concatenate. For function calls,
preserve the function name and arguments as raw HCL text (read from source bytes).

---

### B5. `hclwrite.Tokens` with zero-value `Type` field

Multiple places use:
```go
hclwrite.Tokens{{Type: 0, Bytes: []byte(...)}}
```

`hcl.TokenType(0)` is `hclsyntax.TokenNone` which is the zero value. This
may work in practice (hclwrite may just write the bytes), but it's relying on
undocumented behavior. If hclwrite validates token types, this breaks silently.

---

## TIER 3: Cross-Cloud Semantic Gaps (things that can't be 1:1 mapped)

### S1. AMI → GCP Image: opaque IDs vs image families

`ami-0c55b159cbfafe1f0` is a region-specific opaque identifier. It encodes an OS,
version, architecture, and EBS configuration. The tool maps it to:

```
boot_disk.initialize_params.image = "ami-0c55b159cbfafe1f0"
```

This is invalid on GCP. GCP expects `"debian-cloud/debian-11"` or a self_link.

**Fix:** The transform function must either:
1. Resolve the AMI ID via AWS API → extract OS/version → map to GCP image family
2. Emit a `# TODO: replace AMI` comment and a placeholder
3. Accept an AMI→image mapping file as input

Option 2 is the honest minimum. Option 1 is the correct solution.

---

### S2. AWS Security Group ingress/egress → GCP firewall is fundamentally different

AWS: Security groups are **stateful**. An ingress rule implicitly allows return traffic.
Each SG can have multiple inline ingress/egress blocks.

GCP: Firewall rules are **stateless** and are separate resources. Each rule is one
`google_compute_firewall` with a `direction` of INGRESS or EGRESS.

The current mapping naively maps the entire SG to one firewall:
```go
{SourceAttr: "ingress", TargetAttr: "allow", IRKey: "allow_rules"},
{SourceAttr: "egress", TargetAttr: "deny", IRKey: "deny_rules"},
```

This is semantically wrong. Egress doesn't map to deny rules. Ingress rules need
their port/protocol/CIDR blocks restructured into GCP's `allow { protocol ports }` blocks.

**Fix:** This requires the `Expand` function from F1 — one SG → N firewall rules.

---

### S3. AWS VPC `cidr_block` → GCP has NO VPC-level CIDR

GCP `google_compute_network` has no CIDR. CIDRs exist only on subnetworks.
The current mapping does:

```go
{SourceAttr: "cidr_block", TargetAttr: "auto_create_subnetworks", IRKey: "cidr",
    Transform: func(_ interface{}) interface{} { return false }},
```

This *discards* the CIDR and sets `auto_create_subnetworks = false`, which means
the VPC has NO subnets. The original CIDR information (`10.0.0.0/16`) is lost.

**Fix:** Emit a comment: `# AWS VPC CIDR 10.0.0.0/16 — define as google_compute_subnetwork`.
Or better: if `aws_subnet` resources reference this VPC, let those carry the CIDRs.

---

### S4. IAM models are structurally incompatible

AWS: `iam_instance_profile` → tied to an IAM Role with an AssumeRole policy and attached policies.
GCP: `service_account { email = "..." scopes = [...] }` — completely different authorization model.
Azure: Managed identities — yet another model.

The mapping `iam_instance_profile → service_account.email` maps a profile *name*
to a service account *email*, which are not the same kind of value.

**Fix:** IAM translation requires a dedicated sub-translator that understands the
full IAM graph (roles, policies, bindings), not a single attribute mapping.

---

### S5. AWS RDS `password` → GCP Cloud SQL `root_password` is wrong

GCP Cloud SQL doesn't accept `root_password` as a top-level attribute.
It uses `google_sql_user` as a separate resource. The mapping:

```go
{SourceAttr: "username", TargetAttr: "root_password", IRKey: "admin_user"},
```

This maps username to root_password — wrong attribute, wrong semantics, wrong value.

**Fix:** `aws_db_instance` must expand to `google_sql_database_instance` + `google_sql_user`.

---

### S6. Several AWS attributes have NO cross-cloud equivalent

| AWS attribute | Status |
|---|---|
| `skip_final_snapshot` | GCP has no equivalent — deletions always destroy |
| `engine_version = "14"` | GCP requires `POSTGRES_14` (different string format) |
| `key_name` (EC2 keypair) | GCP uses project-level SSH keys in metadata, not per-instance keypairs |
| `iam_instance_profile` | GCP uses service accounts (see S4) |
| AWS metadata service (`169.254.169.254`) in user_data scripts | GCP metadata service is at a different IP/path |

---

### S7. `aws_lambda_function` → `google_cloudfunctions_function` is the wrong target

`google_cloudfunctions_function` is the v1 Cloud Functions API, which is deprecated.
The correct target is `google_cloudfunctions2_function` (v2 API) or
`google_cloud_run_v2_service` for container-based workloads.

Lambda also requires a deployment package (S3 or zip), while Cloud Functions
requires a GCS source archive or source repo — these are structurally different.

---

### S8. `aws_s3_bucket` → `google_storage_bucket` loses bucket policy and ACL semantics

AWS S3 has bucket policies (JSON IAM policy documents), ACLs (canned or custom),
and block public access settings. GCP uses IAM bindings (`google_storage_bucket_iam_*`).
The mapping `acl → predefined_acl` handles only the simplest case.

---

## TIER 4: PID Feedback Loop Design Issues

### P1. Effort → behavior is a step function, not a smooth control

```go
if !mapped && effort > 0.5 {
    if targetAttr, ok := t.fuzzyMatch(srcKey, rm); ok {
```

Below 0.5: no fuzzy matching at all. Above 0.5: full fuzzy matching.
This is a binary switch, not a continuous control signal. The PID output
between 0.0-0.5 changes nothing, and between 0.5-1.0 also changes nothing.
The smooth PID output is wasted.

**Fix:** Make fuzzy match *threshold* continuous. e.g., Levenshtein distance ≤ `(1 - effort) * max_distance`.

---

### P2. Derivative term doesn't dampen — it just triggers early stop

The D term is computed correctly in `pid.Compute()` but its effect on the
translator is: nothing. The only place `D` matters is the convergence check:

```go
if math.Abs(delta) < 0.001 && iter > 1 {
    break
}
```

This isn't dampening — it's giving up. A real D term should *reduce* fuzzy
aggressiveness when accuracy is improving fast (to prevent false-positive matches
from degrading quality).

**Fix:** The effort value already carries D information, but the translator
doesn't use it granularly. Introduce a "caution" signal from the D term that
*tightens* the fuzzy matching even when effort is high.

---

### P3. `LearnFromMissed` can only rediscover existing mappings

As described in F4. The learned-mappings accumulation (the I term's real-world
effect) only searches within `rm.AttrMaps` — the same table that the exact
match already searched. It's circular.

---

### P4. All iterations re-translate the entire module

Every PID iteration calls `l.trans.Translate(module, effort)` which iterates
all resources and all attributes. Only the unmapped attributes need re-processing.
On large modules this is wasteful.

**Fix:** Track which resources/attrs are fully mapped and skip them in subsequent iterations.

---

### P5. `fuzzyMatch` `hasSimilarSuffix` produces dangerous false positives

```go
suffixes := []string{"name", "size", "type", "id", "enabled", "count", "region", "location"}
if strings.HasSuffix(a, s) && strings.HasSuffix(b, s) {
    return true
}
```

`engine_version` and `database_version` both end in `version` (if added to list).
More critically: `instance_type` and `disk_type` both end in `type`.
`cache_size` and `disk_size` both end in `size`.

Fuzzy-matching `instance_type` → `disk_type` would write a disk type of `"t3.medium"`.
In infrastructure code, a wrong value is worse than a missing value.

**Fix:** Fuzzy matching must never apply transforms — only match names. And it
must score matches and reject anything below a configurable threshold.

---

## TIER 5: Missing Capabilities

| # | Missing feature | Impact |
|---|---|---|
| M1 | No data source translation | `data "aws_ami"` becomes `${unknown_expr}` |
| M2 | No module composition | `module "vpc" { source = "..." }` is ignored |
| M3 | No provider alias handling | Multi-provider configs are silently broken |
| M4 | No state migration guidance | Users get HCL but no `terraform import` commands |
| M5 | No `count`/`for_each` expression rewriting | Meta-arguments with provider-specific logic break |
| M6 | Output references aren't rewritten | `output "vpc_id" { value = aws_vpc.main.id }` is emitted as-is |
| M7 | Variables aren't passed through to output | Input variables are parsed but never emitted |
| M8 | No target-provider required-attribute injection | GCP `google_compute_instance` REQUIRES `name`, `boot_disk`, `network_interface` — codegen doesn't add defaults for missing required attrs |
| M9 | No declarative mapping format | Adding a resource type requires Go code change + recompile |
| M10 | No validation against target provider schema | No check that output HCL is valid before writing |

---

## Priority Fix Order

1. **F1 + F2** — without 1:N mapping and reference rewriting, the output is always invalid
2. **F3** — without correctness scoring, the PID loop is optimising the wrong thing
3. **B1 + B2 + B3** — these are straightforward code bugs
4. **S1-S5** — cross-cloud semantic gaps that produce wrong infrastructure
5. **P1 + P5** — PID loop issues that make the feedback mechanism a no-op
6. **M8** — required attribute injection is critical for valid output
7. **M6 + M7** — output/variable handling for completeness
