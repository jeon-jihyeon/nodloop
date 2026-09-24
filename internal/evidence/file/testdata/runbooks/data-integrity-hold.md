# Data integrity hold

Use this procedure when the coverage rule reports missing or late points, or when the change context is data_availability_issue or measurement_context_changed. The review holds instead of concluding because the numbers cannot be compared with their baseline.

## Coverage

Count the missing and late points in the window. When the missing ratio is above the coverage threshold the window is incomplete and any metric movement may be an artifact of the gap. Return hold and name the gap.

## Measurement changes

When the change context says the measurement or aggregation changed, the baseline was produced under different rules. Do not compare the window with the baseline. Return hold until a new baseline is collected under the new rules.

## What to record

State the hold reason, the range that is missing or changed, and the condition under which the review is run again. A hold is a complete result and is recorded like any other review.
