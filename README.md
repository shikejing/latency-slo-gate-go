# latslo — 可嵌入的延迟直方图与 SLO 判定库

`latslo` 是一个可以嵌进服务进程里的 Go 库，用于在持续压测和回归测试中判断性能有没有退化。
耗时样本由多个上报方并发写入，内存占用不随样本量增长，进程重启后累计结果可以接着用。

## 特性

- **固定内存直方图**：桶数只取决于配置的量程和精度，与样本量无关。
- **百分位误差界**：百分位估计为保守上界，相对误差不超过 `10^-p`（`p` 为有效位数）。
- **越界样本单独识别**：低于 `MinTrackableValue` 计入 underflow，高于 `MaxTrackableValue` 计入 overflow，均单独暴露。
- **并发安全**：上报与快照可并发进行，快照取一致时间点视图，多次快照不重复累计也不丢样本（`-race` 干净）。
- **跨实例合并**：基于 G-Counter CRDT，合并满足结合律、交换律、幂等律，同一来源重复合并不会算重。
- **滚动窗口 SLO**：窗口可滚动推进，支持同时配置多个目标（百分位阈值 + 错误预算），判定结果给出可区分原因。
- **回归比较**：基于 seed 的 bootstrap，样本不足或波动大时输出"不确定"，区分真实退化与随机噪声。
- **持久化与恢复**：原子写入（临时文件 + rename），带 magic 头与 CRC-32 校验和；损坏数据明确拒绝继续累计。
- **确定性**：相同输入 + 相同随机种子，统计输出逐次一致，历史结论可复现。

## 统计语义与误差边界

直方图采用以 2 为底的对数分桶布局，每个量级桶内有 `M = 10^p` 个线性子桶（`p` 为 `SignificantDigits`，1..3）。
落入宽度为 `w` 的子桶的值，其百分位估计取该子桶的上沿，因此：

- 百分位估计是**保守上界**（报告的是与直方图一致的最大延迟）。
- 相对误差不超过 `1/M = 10^-p`。例如 `p=2` 时相对误差 ≤ 1%。
- `ValueAtPercentile` 返回的是上界，SLO 判定中"百分位 ≤ 阈值"因此是保守的。
- `CountAbove(threshold)` 统计延迟严格大于阈值的样本数，采用保守口径：只要子桶上沿高于阈值，该桶就计入；overflow 样本全部计入（因为它们高于量程上限，通常也高于阈值）。
- `Mean` 用桶中点估算均值；underflow 样本按 `MinTrackableValue` 计、overflow 样本按 `MaxTrackableValue` 计。

`SignificantDigits` 与误差、内存的关系（以默认量程 1ns..1h 为例）：

| p | 相对误差 | 子桶数/量级 | 总桶数（约） |
|---|---------|------------|------------|
| 1 | ≤ 10%   | 10         | 370        |
| 2 | ≤ 1%    | 100        | 3700       |
| 3 | ≤ 0.1%  | 1000       | 37000      |

## 配置项

### Config（直方图配置）

```go
type Config struct {
    MinTrackableValue time.Duration // 量程下限，低于它计入 underflow
    MaxTrackableValue time.Duration // 量程上限，高于它计入 overflow
    SignificantDigits int           // 有效位数 1..3，决定误差与内存
}
```

默认配置：`MinTrackableValue=1ns`、`MaxTrackableValue=1h`、`SignificantDigits=2`（1% 误差）。

### RegressionConfig（回归门配置）

```go
type RegressionConfig struct {
    MinSamples          int64   // 每侧最小样本量，不足则"不确定"
    Tolerance           float64 // 相对等效裕度，如 0.1 表示 ±10%
    Alpha               float64 // 显著性水平，如 0.05
    BootstrapIterations int     // bootstrap 重采样次数
    Seed                int64   // 随机种子，决定输出确定性
    Percentile          float64 // 要比较的百分位，如 0.99
}
```

默认值：`MinSamples=1000`、`Tolerance=0.1`、`Alpha=0.05`、`BootstrapIterations=200`、`Seed=1`、`Percentile=0.99`。

### SLO（SLO 目标）

```go
type SLO struct {
    Name        string        // 目标名
    Percentile  float64       // 百分位，如 0.99
    Threshold   time.Duration // 百分位阈值
    ErrorBudget float64       // 允许的超阈值样本比例，如 0.001
}
```

## 并发模型

- `Histogram` 内部用 `sync.RWMutex` 保护。`Record` 短暂持有写锁；`Snapshot` 持有读锁并拷贝整个桶数组，返回一致时间点视图。
- 多次 `Snapshot` 之间不共享任何状态，因此多次快照不会重复累计也不会丢样本。
- `Window` 内部用 `sync.RWMutex` 保护桶环；`Record` 写入当前时间所在桶，`Snapshot` 累加窗口内桶的计数。
- 所有方法均为 goroutine-safe，可直接用 `-race` 验证。

## 合并语义（CRDT）

每个直方图实例有唯一 source id 和随上报单调递增的 clock。`Merge` 对每个 source 的 grow-only 计数器取**逐点最大值**（G-Counter CRDT），因此：

- 结合律：`(A merge B) merge C == A merge (B merge C)`
- 交换律：`A merge B == B merge A`
- 幂等律：`A merge A == A`，同一来源重复合并不会算重
- 合并结果与把全部样本交给单个实例累计的结果在误差范围内一致

`Add` 则是逐分量相加，用于聚合不相交的时间桶（如窗口内各桶），不应用于合并副本。

## 滚动窗口与 SLO 判定

`Window` 维护一个桶环，桶大小需整除窗口大小。`Record(now, v)` 把样本写入 `now` 所在桶；`Snapshot(now)` 累加窗口 `[now-windowSize, now]` 内的桶。`Advance(now)` 可手动推进并清理过期桶。

`SLO.Evaluate(snap)` 判定逻辑：

1. 样本数为 0 → 通过（无数据即无违反）。
2. 计算观测百分位 `ValueAtPercentile(Percentile)`。
3. 计算超阈值比例 `CountAbove(Threshold) / Total`。
4. 百分位 > 阈值 → `ReasonPercentileExceeded`（优先）。
5. 超阈值比例 > 错误预算 → `ReasonErrorBudgetExhausted`。
6. 否则通过，`ReasonOK`。

## 回归比较

`RegressionGate.Compare(current, baseline)` 在配置的百分位上比较当前快照与历史基线：

- 任一侧样本数 < `MinSamples` → `ConclusionInconclusive`（样本不足）。
- 否则用 Poisson bootstrap 对桶计数重采样，计算 current/baseline 比值的置信区间（CI）。
- CI 下界 > `1+Tolerance` → `ConclusionDegraded`（真实退化）。
- CI 上界 < `1-Tolerance` → `ConclusionImproved`（真实改善）。
- CI 完全落在 `[1-Tolerance, 1+Tolerance]` 内 → `ConclusionNoChange`。
- CI 跨越等效裕度边界 → `ConclusionInconclusive`（波动大，无法区分噪声）。

bootstrap RNG 由 `Seed` 播种，相同输入 + 相同种子 → 输出逐次一致。

## 持久化与恢复

- `Store.Save` 原子写入：先写临时文件，再 `fsync`，最后 `rename`。
- 文件格式：`magic(8B) | payloadLen(8B) | gob payload | crc32(4B)`。
- `Store.Load` 校验 magic、长度、CRC-32，并做内部一致性校验（桶数、非负、`total == sum(buckets)+underflow+overflow`、配置合法）。
- 任何校验失败返回 `ErrCorrupt`（用 `errors.Is` 判定），**拒绝继续累计**，不会默默给出错误结论。
- 恢复后继续上报、再次保存、再次加载，累计量不重复也不丢失。

## 快速示例

```go
package main

import (
    "fmt"
    "time"
    "github.com/shikejing/latency-slo-gate-go"
)

func main() {
    cfg := latslo.DefaultConfig()
    h, _ := latslo.NewHistogram(cfg, 1)
    for i := 0; i < 10000; i++ {
        h.Record(time.Duration(i) * time.Microsecond)
    }
    snap := h.Snapshot()
    fmt.Printf("total=%d p99=%s\n", snap.Total(), snap.ValueAtPercentile(0.99))

    slos := []latslo.SLO{
        {Name: "p99", Percentile: 0.99, Threshold: 100 * time.Millisecond, ErrorBudget: 0.001},
    }
    for _, r := range latslo.EvaluateAll(snap, slos) {
        fmt.Printf("%s: pass=%v reason=%s\n", r.Name, r.Pass, r.Reason)
    }
}
```

## 测试

```bash
go test -race ./...
```

覆盖场景：并发上报与快照（含 `-race`）、跨实例合并（幂等/交换/结合/统计等价）、滚动窗口、SLO 判定（三种原因）、回归比较（退化/改善/无变化/样本不足/高波动/确定性）、重启恢复、损坏数据拒绝。
