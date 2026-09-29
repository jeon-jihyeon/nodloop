# Outcome rate degradation

Use this procedure when the conversion rate, conversion_count over click_count, falls below its control limit inside the test window. The rate is always read with its denominator.

## Confirm the rate

Recompute the window rate from the summed counts and compare it with the baseline rate and the control limit. A rate computed from fewer than the minimum samples is inadequate and the review says so instead of concluding.

## Separate numerator from denominator

Decide whether conversions fell or clicks rose. Conversions falling with flat clicks points at the outcome side such as a broken landing page or tracking. Clicks rising with flat conversions points at the traffic side and continues in the metric anomaly investigation.

## Check tracking changes

When the change context is planned_operational_change or measurement_context_changed, verify the conversion tracking before any traffic conclusion. A tracking change explains a rate drop without any change in traffic quality.

## Decide

Return ready_for_review with the side of the rate that moved as the first cause candidate. Tracking that is not verified yet is the first check, not a reason to hold. Return hold only when the window itself cannot be trusted, such as missing points or a measurement context change, and name what is missing.
