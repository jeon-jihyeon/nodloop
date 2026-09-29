| condition | events | status acc | hold acc | hold precision | citation p | citation r | required checks | first check | knowledge hit | misapplied | revised | forced holds | failures | annotated | approve:edit:reject | edit rate | mean edit width | mean cost usd | mean input tokens | mean output tokens | p50 ms | p95 ms |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| seed | 12 | 0.17 | 1.00 | 0.17 | 0.00 | 0.00 | 0.00 | 0.00 | 0.00 | 0 | 0 | 0 | 0 | 0 | 0:0:0 | - | - | 0.0100 | 0 | 0 | 0 | 0 |
| feedback:off | 12 | 0.17 | 1.00 | 0.17 | 0.00 | 0.00 | 0.00 | 0.00 | 0.00 | 0 | 0 | 0 | 0 | 0 | 0:0:0 | - | - | 0.0100 | 0 | 0 | 0 | 0 |
| feedback:on | 12 | 0.17 | 1.00 | 0.17 | 0.00 | 0.00 | 0.00 | 0.00 | 0.00 | 0 | 0 | 0 | 0 | 0 | 0:0:0 | - | - | 0.0100 | 0 | 0 | 0 | 0 |
| knowledge:on | 12 | 0.17 | 1.00 | 0.17 | 0.00 | 0.00 | 0.00 | 0.00 | 0.00 | 0 | 0 | 0 | 0 | 0 | 0:0:0 | - | - | 0.0100 | 0 | 0 | 0 | 0 |
| knowledge:all | 12 | 0.17 | 1.00 | 0.17 | 0.00 | 0.00 | 0.00 | 0.00 | 0.00 | 0 | 0 | 0 | 0 | 0 | 0:0:0 | - | - | 0.0100 | 0 | 0 | 0 | 0 |

Against feedback:off

| condition | fixed | regressed | weakened |
|---|---|---|---|
| feedback:on | - | - | - |
| knowledge:on | - | - | - |
| knowledge:all | - | - | - |

| reference | condition | paired events | fixed | regressed | exact McNemar p |
|---|---|---|---|---|---|
| feedback:off | feedback:on | 12 | 0 | 0 | 1.0000 |
| feedback:off | knowledge:on | 12 | 0 | 0 | 1.0000 |
| feedback:off | knowledge:all | 12 | 0 | 0 | 1.0000 |
| feedback:on | knowledge:on | 12 | 0 | 0 | 1.0000 |
| feedback:on | knowledge:all | 12 | 0 | 0 | 1.0000 |
| knowledge:on | knowledge:all | 12 | 0 | 0 | 1.0000 |

Paired event-cluster percentile bootstrap: 95% interval, 10000 resamples, seed 0
Each event is one cluster; repeated reviews do not increase independent event count.

| reference | condition | metric | events | pairs | difference | lower | upper |
|---|---|---|---|---|---|---|---|
| feedback:off | feedback:on | status_accuracy | 12 | 12 | 0.000 | 0.000 | 0.000 |
| feedback:off | feedback:on | misapplied | 12 | 12 | 0.000 | 0.000 | 0.000 |
| feedback:off | knowledge:on | status_accuracy | 12 | 12 | 0.000 | 0.000 | 0.000 |
| feedback:off | knowledge:on | misapplied | 12 | 12 | 0.000 | 0.000 | 0.000 |
| feedback:off | knowledge:all | status_accuracy | 12 | 12 | 0.000 | 0.000 | 0.000 |
| feedback:off | knowledge:all | misapplied | 12 | 12 | 0.000 | 0.000 | 0.000 |
| feedback:on | knowledge:on | status_accuracy | 12 | 12 | 0.000 | 0.000 | 0.000 |
| feedback:on | knowledge:on | misapplied | 12 | 12 | 0.000 | 0.000 | 0.000 |
| feedback:on | knowledge:all | status_accuracy | 12 | 12 | 0.000 | 0.000 | 0.000 |
| feedback:on | knowledge:all | misapplied | 12 | 12 | 0.000 | 0.000 | 0.000 |
| knowledge:on | knowledge:all | status_accuracy | 12 | 12 | 0.000 | 0.000 | 0.000 |
| knowledge:on | knowledge:all | misapplied | 12 | 12 | 0.000 | 0.000 | 0.000 |
