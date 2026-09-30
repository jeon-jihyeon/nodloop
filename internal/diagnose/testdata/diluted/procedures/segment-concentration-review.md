# Segment concentration review

Use this procedure when one source's share of click_count moves by more than the share threshold inside the test window while other sources lose share.

## Measure the share shift

Read the window share and the baseline share of the concentrated source and of the sources that lost share. The shift is the delta in share, not the change in the source's own count.

## Compare against total volume

Compare the total click_count of the window with the baseline total. When the window mean of the total is at least 1.5 times its baseline mean, one source grew on its own and the metric anomaly investigation applies to that source. A total below 1.5 times its baseline with a shifted share means traffic was redistributed between sources.

## Check the concentrated segment

Read the conversion rate of the concentrated source. A source that gains share while its conversion rate falls is a low quality traffic candidate. A source that gains share with a stable conversion rate is a distribution change.

## Decide

Return ready_for_review naming the concentrated source and whether the shift is redistribution or growth. Pausing or reweighting a source is outside this procedure.
