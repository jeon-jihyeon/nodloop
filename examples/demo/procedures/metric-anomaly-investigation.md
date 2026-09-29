# Metric anomaly investigation

Use this procedure when a count metric such as click_count moves far from its baseline inside the test window. A jump is a signal to investigate, never a conclusion about fraud.

## Confirm the signal

Compare the window mean with the baseline mean and read the peak hour. Check that the window has enough samples and that the jump is not one hour of noise. A change that only shows in one hour is treated as normal variation.

## Check the segment

Find which source or topic carries the change. A spike confined to one source points at that source's traffic and the review names the source. A spike across every source points at a global cause such as a campaign launch.

## Check downstream outcomes

Compare conversion_count over the same window. Clicks rising while conversions stay flat means the extra clicks did not convert and the source is a low quality traffic candidate. Clicks and conversions rising together means demand grew.

## Decide

Return ready_for_review with the cause candidates in order of likelihood and the checks above as the check order. Blocking, billing changes and account actions are outside this procedure and are escalated to the traffic quality owner.
