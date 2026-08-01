# terra-translate

> Translate Terraform modules between cloud providers using an Intermediate
> Representation (IR) and a **PID-controller feedback loop** to maximise
> attribute mapping accuracy.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           terra-translate pipeline                             │
│                                                                         │
│  Source .tf files                                                       │
│       │                                                                 │
│       ▼                                                                 │
│  ┌──────────┐   HCL AST   ┌──────────────┐   IR Module                 │
│  │  Parser  │────────────►│  Classifier  │──────────────┐              │
│  └──────────┘             └──────────────┘              │              │
│                                                         ▼              │
│                                               ┌──────────────────┐     │
│                                               │  IR (cloud-agno- │     │
│                                               │  stic resource   │     │
│                                               │  graph)          │     │
│                                               └────────┬─────────┘     │
│                                                        │               │
│  ┌─────────────────────── Feedback Loop ───────────────┼─────────────┐ │
│  │                                                     ▼             │ │
│  │   effort [0,1]       ┌──────────────────────────────────────────┐ │ │
│  │  ◄────────────────── │           Translator                     │ │ │
│  │                      │  1. Exact AttrMap lookup                 │ │ │
│  │                      │  2. Learned-mappings table  (I term)     │ │ │
│  │                      │  3. Fuzzy / heuristic match (P term)     │ │ │
│  │                      └────────────────┬─────────────────────────┘ │ │
│  │                                       │ accuracy + unmapped attrs  │ │
│  │                                       ▼                            │ │
│  │                      ┌──────────────────────────────────────────┐ │ │
│  │                      │           PID Controller                 │ │ │
│  │                      │                                          │ │ │
│  │                      │  error = 1.0 − accuracy                  │ │ │
│  │                      │  P  →  fuzzy-match threshold             │ │ │
│  │                      │  I  →  learned-mappings accumulation     │ │ │
│  │                      │  D  →  dampen when improving fast        │ │ │
│  │                      │                                          │ │ │
│  │                      │  output = effort for next iteration      │ │ │
│  │                      └──────────────────────────────────────────┘ │ │
│  └─────────────────────────────────────────────────────────────────── ┘ │
│                                                                         │
│       ▼                                                                 │
│  ┌──────────┐                                                           │
│  │  Codegen │  →  output/main.tf  +  output/translation_report.json    │
│  └──────────┘                                                           │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## The PID Feedback Loop

The core insight is that **Terraform translation is a control problem**:
we want to drive attribute coverage (the "process variable") toward 100% (the "setpoint").

| PID term | Translation meaning | Implementation |
|----------|---------------------|----------------|
| **P** (proportional) | React to current unmapped fraction | Increases `effort` → enables fuzzy attribute-name matching |
| **I** (integral) | Remember what was missed across iterations | Builds a `learnedMappings` table from repeated misses |
| **D** (derivative) | Dampen when accuracy is rapidly improving | Reduces effort when `Δaccuracy` is large (avoid overshoot) |

### Effort → translator behaviour

```
effort ∈ [0, 1]
  0.0 – 0.5  →  strict mode: exact AttrMap matches only
  0.5 – 0.8  →  fuzzy mode:  also try name-similarity heuristics
  0.8 – 1.0  →  aggressive:  normalise names + check synonym table
```

### Convergence

The loop terminates when:
- Accuracy ≥ 99% (within tolerance), **or**
- `|Δaccuracy| < 0.001` for two consecutive iterations (D term detects stall), **or**
- `max-iter` is reached

---

## Installation

```bash
git clone https://github.com/yourorg/terra-translate
cd terra-translate
go mod tidy
go build -o terra-translate .
```

---

## Usage

```bash
# AWS → GCP
terra-translate -from aws -to google \
         -input ./my-aws-module \
         -output ./gcp-output \
         -v -pid-report

# AWS → Azure
terra-translate -from aws -to azurerm \
         -input ./aws-infra \
         -output ./azure-infra \
         -kp 0.9 -ki 0.2 -kd 0.05

# GCP → AWS
terra-translate -from google -to aws \
         -input ./gcp -output ./aws-out
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-from` | `aws` | Source provider |
| `-to` | `google` | Target provider |
| `-input` | `.` | Source `.tf` file or directory |
| `-output` | `./terra-translate-output` | Output directory |
| `-schema` | | Path to `terraform providers schema -json` output |
| `-kp` | `0.8` | PID proportional gain |
| `-ki` | `0.1` | PID integral gain |
| `-kd` | `0.05` | PID derivative gain |
| `-max-iter` | `8` | Maximum feedback iterations |
| `-v` | | Verbose per-iteration output |
| `-pid-report` | | Print full PID history table |

### Exit codes

| Code | Meaning |
|------|---------|
| `0` | Success (≥ 90% accuracy) |
| `2` | Partial success (< 90% accuracy — review output manually) |
| `1` | Fatal error (parse failure, I/O error) |

---

## Supported provider pairs

| Source | Target | Coverage |
|--------|--------|----------|
| `aws` | `google` | compute, storage, VPC, subnet, firewall, RDS→CloudSQL, Lambda→CloudFunctions |
| `aws` | `azurerm` | compute, storage, VNet, NSG, SQL |
| `google` | `aws` | compute, storage |

---

## Provider schema sync

For highest accuracy, generate the live schema from your local Terraform installation:

```bash
terraform providers schema -json > provider_schema.json
terra-translate -schema provider_schema.json -from aws -to google ...
```

The schema JSON is used to augment the built-in mapping tables with the full
attribute list of both providers, giving the fuzzy-matcher more candidates.

---

## Output

```
terra-translate-output/
├── main.tf                  # Translated HCL ready for terraform init
└── translation_report.json  # PID history, accuracy per resource, warnings
```

The `translation_report.json` contains the full PID sample history which you
can plot to visualise how the feedback loop drove accuracy to convergence.
