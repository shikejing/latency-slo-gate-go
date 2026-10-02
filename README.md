# slogate — embeddable latency histograms & SLO gating

`slogate` is a small, dependency-free Go library you can embed inside a
service process to decide, during continuous load tests and regression
runs, whether latency has degraded.

- **Fixed memory.** A histogram holds a fixed number of counters derived
  from its config; memory never grows with sample count. Samples can
  vastly outnumber the bucket capacity.
- **Concurrent reporting and snapshotting.** Many goroutines may observe
  while others snapshot. Snapshots are consistent views of one definite
  instant with no data races.
- **Out-of-range samples are kept.** Samples below `MinValue` and above
  the tracked range are counted separately (underflow/overflow), still
  included in totals/means/error rates, and are individually
  identifiable per quantile.
- **Mergeable.** Histograms from independent instances combine bucket by
  bucket. Merge is associative and re-merging the same source never double
  counts.
- **Rolling-window SLO evaluation** with multiple simultaneous objectives
  (percentile latency + error budget) and distinguishable reasons.
- **Baseline regression detection** using a Mann–Whitney test plus a
  deterministic bootstrap confidence interval: real regressions are
  separated from random noise, and small/high-variance samples return
  *inconclusive* rather than a false verdict.
- **Durable state.** Checksummed binary format with atomic file writes;
  restarted processes continue accumulation with no loss or duplication,
  and corrupt state is rejected outright.
- **Deterministic.** Given identical inputs and the same configured random
  seed, statistics and verdicts are bit-for-bit reproducible.

## Quick start

```go
cfg := slogate.HistogramConfig{MinValue: 1, MaxValue: 1_000_000, RelativeError: 0.01}
h, _ := slogate.New(cfg)

// concurrent reporters
h.ObserveOutcome(latencyNanos, isError)

s := h.Snapshot()
p99, region, _ := s.QuantileEx(0.99) // region marks under/in/overflow

objectives := []slogate.Objective{{
    Name: "p99", Percentile: 99, LatencyLimit: 200_000,
    ErrorBudget: 0.001, MinSamples: 1000, WarnFraction: 0.9,
}}
report := slogate.Evaluate(s, time.Now(), objectives)
// report.Status: Pass / Warn / Fail / Unknown; Results[i].Reasons explain why.
```

Rolling window:

```go
w, _ := slogate.NewWindow(cfg, slogate.WindowConfig{
    SliceWidth: time.Minute, SliceCount: 15, Clock: nil, // nil => time.Now
})
w.Observe(ts, latency, isError)
report := slogate.Evaluate(w.Snapshot(), now, objectives)
```

Persistence across restarts:

```go
snap := h.Snapshot()
_ = slogate.Save("/var/lib/svc/latency.bin", snap)

loaded, err := slogate.Load("/var/lib/svc/latency.bin", cfg) // corrupt -> error
if err == nil {
    h2, _ := slogate.Restore(loaded) // continues under the same source & seq
}
```

## Histogram semantics and error bound

The bucket layout is logarithmic. With growth factor

```
g = (1 + RelativeError)^2
```

bucket `i` covers the half-open interval

```
[MinValue * g^i, MinValue * g^(i+1))
```

and a value in that bucket is represented by the bucket's geometric
midpoint `MinValue * g^(i+0.5)`. The relative deviation of that
representative from **any** value in the bucket is at most
`RelativeError`. Bucket counts are exact; only the value assigned within a
bucket is approximate.

`N = BucketCount = floor(log(MaxValue/MinValue)/log(g)) + 1`, which is a
few hundred counters with the default config (`1..1_000_000`, 1%).

### Quantile estimation

Quantiles use a position CDF: sample positions `0..N-1` get anchor values
and fractional positions are log-linearly interpolated. Within a populated
bucket the mass spreads from the bucket's lower edge toward its
geometric-midpoint anchor. Empty buckets carry no mass, so an estimate is
always anchored to a populated bucket value; consequently every
**in-range** estimate lies within `RelativeError` of the true value (with a
small interpolation margin; see `TestQuantileErrorBound`).

`QuantileEx` additionally returns a `Region`:

- `RegionUnderflow` — estimate anchored to observed samples `< MinValue`
  (range `[UnderflowMin, MinValue)`); no bucket-error guarantee, but the
  exact observed minimum is tracked.
- `RegionInRange` — bounded by `RelativeError`.
- `RegionOverflow` — estimate at/above the last bucket, anchored to the
  exact observed maximum; flagged so consumers can widen their config.

Other exact aggregates: `Total`, `Sum`/`Mean`, `Errors`, `Underflow`,
`Overflow`, `UnderflowMin`, `OverflowMax`.

## Configuration reference

### HistogramConfig

| Field          | Meaning                                              |
|----------------|------------------------------------------------------|
| `MinValue`     | smallest tracked latency, `> 0`; below → underflow   |
| `MaxValue`     | largest intended latency; above last bucket → over   |
| `RelativeError`| per-bucket relative error bound, must be in `(0,1)`  |

All samples share one unit (e.g. nanoseconds); configs must match to merge
or decode.

### WindowConfig

| Field        | Meaning                                                          |
|--------------|------------------------------------------------------------------|
| `SliceWidth` | duration of one histogram slice (fixed memory granularity)       |
| `SliceCount` | slices retained; window length = `SliceWidth * SliceCount`       |
| `Clock`      | time source; inject a fake clock in tests, `nil` uses `time.Now` |

Memory is fixed at `SliceCount` histograms. Snapshotting lazily rolls the
window forward, expiring aged slices even without new samples. Samples
older than the retained window are discarded; far-future samples advance
it.

### Objective

| Field           | Meaning                                                          |
|-----------------|------------------------------------------------------------------|
| `Percentile`    | `(0,100]`, e.g. `99`                                             |
| `LatencyLimit`  | hard latency threshold for that percentile                       |
| `ErrorBudget`   | tolerated error fraction, e.g. `0.001`                           |
| `MinSamples`    | below this window count → `Unknown/InsufficientData`             |
| `WarnFraction`  | optional `(0,1)` early-warning fraction of limit/budget burn     |

Status priority is `Fail > Warn > Pass > Unknown` across objectives.
Reasons distinguish `PercentileBreach`, `PercentileNearLimit`,
`ErrorBudgetExhausted`, `ErrorBudgetBurning`, and `InsufficientData`.

### RegressionConfig

| Field                | Meaning                                                          |
|----------------------|------------------------------------------------------------------|
| `MinSamples`         | required size of **each** distribution, else inconclusive        |
| `Alpha`              | one-sided significance level (e.g. `0.05`)                       |
| `Tolerance`          | minimum relative median increase that "matters" (e.g. `0.05`)    |
| `NoiseMargin`        | CI clearance beyond the tolerance band needed for a verdict      |
| `BootstrapReplicates`| deterministic bootstrap count; `0` collapses CI to point estimate|
| `Seed`               | PRNG seed making all statistics reproducible                     |

### Verdicts

- **Regression**: one-sided p `< Alpha` **and** the bootstrap CI's lower
  bound clears `Tolerance - NoiseMargin` **and** the point effect meets
  tolerance.
- **Improvement**: symmetric in the beneficial direction.
- **Inconclusive / HighVariance**: statistically detectable shift whose
  CI straddles the tolerance band — cannot separate regression from noise.
- **NoRegression / EffectBelowTolerance**: no significant shift, or a
  significant shift smaller than the configured tolerance.
- **Inconclusive / InsufficientSamples**: either distribution is below
  `MinSamples`.

The bootstrap resamples bucket counts via an exact multinomial (sequential
conditional binomials), needs no per-sample storage, and is seeded
deterministically; equal inputs + equal seed always yield the same CI and
verdict.

## Merging

Each histogram carries a `SourceID{ID, Epoch}` and a monotonically
increasing snapshot `Seq`. `Merger` keeps, per source, only its **newest
cumulative** snapshot:

- distinct sources are summed bucket by bucket;
- re-adding an identical `(source, seq)` snapshot is a no-op (safe retry);
- a newer `seq` replaces that source's prior contribution (never added to
  it), so repeated aggregation cannot double count;
- an older `seq`, or a newer snapshot whose cumulative counts move
  backwards, is rejected (`ErrSourceRegression`);
- changing `Epoch` (e.g. a fresh, non-restored process) makes the source
  distinct, so a reset can never be mistaken for continuation.

`Merger.Merge(other)` is the associative union primitive used to combine
per-node aggregators in any grouping order. All inputs must share the same
`HistogramConfig` (`ErrConfigMismatch` otherwise). The merged quantiles
match, within bucket error, feeding every sample to one histogram.

## Persistence format

`Encode`/`Save` write a little-endian self-describing blob:

```
"SLGT" | version(u8) | config(f64 x3) | source(id + epoch) |
seq/timestamps | aggregates | bucketCount(u32) + buckets(u64...) |
CRC32-IEEE(u32)
```

`Save` writes a temp file in the destination directory, fsyncs it, then
atomically renames it, so a crash cannot leave a torn state that loads as
valid. `Decode`/`Load` verify:

1. CRC over the entire body (bit flips / truncation → `ErrCorruptState`),
2. magic and format version (`ErrUnsupportedVer`),
3. exact config match (`ErrConfigMismatch`),
4. structural consistency: bucket count, region totals, error count,
   extremal sanity (`ErrCorruptState`).

Corruption is never silently accumulated. A normal restart loads the
snapshot and calls `Restore`, preserving source identity and sequence so
counts continue exactly once.

## Thread safety and determinism

`Histogram` and `Window` are safe for concurrent use; snapshots deep-copy
counters under a mutex. All evaluation/comparison functions are pure in
their inputs. The only randomness in the library is the explicit
`RegressionConfig.Seed`; no package-global or wall-clock-seeded RNG is used
for statistics. (`NewEpoch` uses wall time only to mint a source epoch and
is not part of any statistical computation.)

## Testing

`go test ./...` covers:

- fixed memory, region accounting and the quantile error bound;
- concurrent reporting vs. snapshotting under `-race` (no lost/duplicate
  samples, consistent totals);
- cross-instance merging, idempotent re-merge, associativity, layout
  mismatch;
- rolling-window advance, expiry, out-of-window rejection;
- SLO pass/warn/fail, error-budget reasons, insufficient data, multi-target
  worst-case aggregation;
- regression detection of a real shift, an improvement, noise that must not
  trigger a verdict, insufficient samples, and seed-based reproducibility;
- restart continuity (no loss/duplication) and rejection of bit-flipped,
  truncated, garbage, bad-magic and bad-version state.
