// Command demo writes the synthetic Traffic Quality Review data set
// Deterministic so the committed files are reproducible
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

// Approved seed knowledge shipped with the demo
// Evidence points at runbook paragraphs because no correction exists yet
const (
	knowledgeAggregation = "k-aggregation-basis"
	knowledgeMeasurement = "k-measurement-first"
)

func demoKnowledge() []knowledge.Knowledge {
	return []knowledge.Knowledge{
		{
			ID: knowledgeAggregation, Version: 1, Kind: knowledge.KindMeaning,
			Content: "click_count and conversion_count use different aggregation time bases. " +
				"A conversion is attributed to the hour of its click and arrives up to four hours later, " +
				"so the newest hours of a window undercount conversions against clicks and their rate reads low " +
				"until attribution catches up. " +
				"A rate that is low only in the newest hours while the earlier hours sit at baseline is attribution lag, " +
				"not a degradation, and needs no action beyond a re-read after the lag",
			Scope:    knowledge.Scope{Metrics: []string{"conversion_count"}},
			Evidence: knowledge.Evidence{ParagraphIDs: []string{rateConfirm, rateTracking}},
			Basis:    knowledge.BasisStated, Status: knowledge.StatusApproved,
			Approver: "demo", ApprovedAt: start, Time: start, Author: "demo",
		},
		{
			ID: knowledgeMeasurement, Version: 1, Kind: knowledge.KindJudgment,
			Content: "Right after a planned operational change, " +
				"the conversion tracking and aggregation check comes before any other check. " +
				"Do not name a source as low quality until the measurement is confirmed unchanged, " +
				"and say so in the purpose of the first check",
			Scope:    knowledge.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}},
			Evidence: knowledge.Evidence{ParagraphIDs: []string{rateTracking, holdMeasurement}},
			Basis:    knowledge.BasisStated, Status: knowledge.StatusApproved,
			Approver: "demo", ApprovedAt: start, Time: start, Author: "demo",
		},
	}
}

// Every event type has perKind events in plan order and the first seeds of them are seed events
// The gap of a data availability hold starts two hours into the window so it has points on both sides
const (
	hours      = 48
	windowSize = 12
	gapStart   = hours - windowSize + 2
	perKind    = 4
	seeds      = 2
	topic      = "shopping"
	rate       = 0.03
)

// sources is sorted so the rows and the rng draws keep their order across runs
var (
	baseClicks = map[string]float64{"source-a": 1000, "source-b": 800, "source-c": 600}
	sources    = slices.Sorted(maps.Keys(baseClicks))
	start      = time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
)

// Paragraph ids of the demo runbooks
// The contract suite checks them against the split output
const (
	anomalyConfirm       = "metric-anomaly-investigation#Metric anomaly investigation/Confirm the signal#1"
	anomalySegment       = "metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"
	anomalyDownstream    = "metric-anomaly-investigation#Metric anomaly investigation/Check downstream outcomes#1"
	rateConfirm          = "outcome-rate-degradation#Outcome rate degradation/Confirm the rate#1"
	rateSeparate         = "outcome-rate-degradation#Outcome rate degradation/Separate numerator from denominator#1"
	rateTracking         = "outcome-rate-degradation#Outcome rate degradation/Check tracking changes#1"
	concentrationMeasure = "segment-concentration-review#Segment concentration review/Measure the share shift#1"
	concentrationTotal   = "segment-concentration-review#Segment concentration review/Compare against total volume#1"
	concentrationSegment = "segment-concentration-review#Segment concentration review/Check the concentrated segment#1"
	holdCoverage         = "data-integrity-hold#Data integrity hold/Coverage#1"
	holdMeasurement      = "data-integrity-hold#Data integrity hold/Measurement changes#1"
)

// The paragraph a hold event must cite for each context the runbook holds on
var holdChecks = map[evidence.Context]evidence.ParagraphID{
	evidence.ContextDataAvailability:   holdCoverage,
	evidence.ContextMeasurementChanged: holdMeasurement,
}

type event struct {
	id      string
	kind    evidence.EventType
	context evidence.Context
	seed    bool
	// Factors applied inside the window
	// A source without a click factor keeps its clicks and a conversion factor of 1 keeps the conversions
	clickFactors     map[string]float64
	conversionFactor float64
	// Hours dropped inside the window from gapStart on
	dropHours int
	// Conversion factors of the newest window hours in oldest first order
	// Models attribution that has not caught up
	lag []float64
}

// One batch per event type with the window shape its events share
// 1. the hold type splits into two batches because the runbook holds on two contexts with different shapes
// 2. the attribution lag rate is at baseline for eight hours and falls only in the newest four
// 3. without the aggregation knowledge the attribution lag reads as a fresh degradation
func plan() []event {
	none := evidence.ContextNoKnownChange
	unknown := evidence.ContextUnknown
	planned := evidence.ContextPlannedChange
	availability := evidence.ContextDataAvailability
	measurement := evidence.ContextMeasurementChanged
	half := map[string]float64{"source-a": 0.5, "source-b": 0.5, "source-c": 0.5}
	concentrated := map[string]float64{"source-a": 2.2, "source-b": 0.45, "source-c": 0.45}
	out := slices.Concat(
		batch(evidence.TypeNormalVariation, event{conversionFactor: 1}, none, none, none, unknown),
		batch(evidence.TypeClickSpike, event{clickFactors: map[string]float64{"source-a": 4}, conversionFactor: 1},
			none, unknown, none, none),
		batch(evidence.TypeConversionRateDrop, event{conversionFactor: 0.35}, planned, none, planned, none),
		batch(evidence.TypeSourceConcentration, event{clickFactors: concentrated, conversionFactor: 1},
			none, none, none, none),
		batch(evidence.TypeHold, event{dropHours: 9, conversionFactor: 1}, availability, availability),
		batch(evidence.TypeHold, event{clickFactors: half, conversionFactor: 0.5}, measurement, measurement),
		batch(evidence.TypeAttributionLag, event{conversionFactor: 1, lag: []float64{0.5, 0.3, 0.15, 0.05}},
			none, none, none, none),
	)
	for i := range out {
		out[i].id = fmt.Sprintf("tq-%03d", i+1)
		out[i].seed = i%perKind < seeds
	}
	return out
}

// Events of one kind that share a window shape in the given contexts
func batch(kind evidence.EventType, shape event, contexts ...evidence.Context) []event {
	out := make([]event, len(contexts))
	for i, c := range contexts {
		out[i] = shape
		out[i].kind, out[i].context = kind, c
	}
	return out
}

// A label as labels.jsonl stores it
// The hold key stays last in every line so the written data set keeps its bytes
type label struct {
	evidence.Label
	Hold bool `json:"hold"`
}

// Normal variation and attribution lag expect no_action which carries no checks so they require none
func (e event) label() label {
	l := evidence.Label{
		EventID: e.id, Type: e.kind, Seed: e.seed, Anomalies: []evidence.SeriesRef{},
		Paragraphs: []evidence.ParagraphID{}, RequiredChecks: []evidence.ParagraphID{}, Knowledge: e.knowledge(),
	}
	switch e.kind {
	case evidence.TypeClickSpike:
		l.Anomalies = anomalies("click_count", "source-a")
		l.Paragraphs = []evidence.ParagraphID{anomalySegment, anomalyDownstream}
		l.RequiredChecks = []evidence.ParagraphID{anomalyConfirm, anomalySegment}
	case evidence.TypeConversionRateDrop:
		l.Anomalies = anomalies("conversion_count", sources...)
		l.Paragraphs = []evidence.ParagraphID{rateSeparate, rateTracking}
		l.RequiredChecks = []evidence.ParagraphID{rateConfirm, rateSeparate}
	case evidence.TypeAttributionLag:
		l.Anomalies = anomalies("conversion_count", sources...)
	case evidence.TypeSourceConcentration:
		l.Anomalies = anomalies("click_count", "source-a")
		l.Paragraphs = []evidence.ParagraphID{concentrationTotal, concentrationSegment}
		l.RequiredChecks = []evidence.ParagraphID{concentrationMeasure, concentrationTotal}
	case evidence.TypeHold:
		l.RequiredChecks = []evidence.ParagraphID{holdChecks[e.context]}
	}
	return label{Label: l, Hold: l.IsHold()}
}

// Anomalous series of one metric in the given sources of the demo topic
func anomalies(metric string, srcs ...string) []evidence.SeriesRef {
	out := make([]evidence.SeriesRef, 0, len(srcs))
	for _, s := range srcs {
		out = append(out, evidence.SeriesRef{Metric: metric, Dims: map[string]string{"source": s, "topic": topic}})
	}
	return out
}

// Related events name the knowledge a good review uses and every other event names none
// 1. the aggregation item belongs to every event whose conversion rate reading is part of the picture
// 2. a click surge with flat conversions lowers the rate too so it counts
// 3. the measurement item belongs to every event right after a planned change
// 4. a measurement context change is a hold by runbook and the item must not reach it
func (e event) knowledge() []string {
	var ids []string
	switch e.kind {
	case evidence.TypeConversionRateDrop, evidence.TypeAttributionLag,
		evidence.TypeClickSpike, evidence.TypeSourceConcentration:
		ids = append(ids, knowledgeAggregation)
	}
	if e.context == evidence.ContextPlannedChange {
		ids = append(ids, knowledgeMeasurement)
	}
	return ids
}

func main() {
	out := flag.String("out", ".", "output directory")
	flag.Parse()
	if err := write(*out); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

// The seeded source makes every run write the same bytes
func write(dir string) error {
	events := plan()
	rng := rand.New(rand.NewSource(1))
	points := [][]string{{"event_id", "timestamp", "source", "topic", "metric", "value"}}
	contexts := [][]string{{"event_id", "change_context"}}
	labels := make([]label, 0, len(events))
	for _, e := range events {
		points = append(points, e.rows(rng)...)
		contexts = append(contexts, []string{e.id, string(e.context)})
		labels = append(labels, e.label())
	}
	if err := writeCSV(filepath.Join(dir, "events.csv"), points); err != nil {
		return err
	}
	if err := writeCSV(filepath.Join(dir, "contexts.csv"), contexts); err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(dir, "labels.jsonl"), labels); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(analysis.DefaultPolicyYAML), 0o644); err != nil {
		return err
	}
	return writeJSONL(filepath.Join(dir, "knowledge.jsonl"), demoKnowledge())
}

// events.csv rows of the event
// The shared rng advances in event order so every run draws the same values
func (e event) rows(rng *rand.Rand) [][]string {
	var rows [][]string
	for h := range hours {
		if h >= gapStart && h < gapStart+e.dropHours {
			continue
		}
		inWindow := h >= hours-windowSize
		ts := start.Add(time.Duration(h) * time.Hour).Format(time.RFC3339)
		for _, s := range sources {
			clicks := baseClicks[s] * (1 + (rng.Float64()-0.5)*0.08)
			conversions := clicks * rate * (1 + (rng.Float64()-0.5)*0.2)
			if inWindow {
				if m, ok := e.clickFactors[s]; ok {
					clicks *= m
				}
				conversions *= e.conversionFactor
				if lagAt := h - (hours - len(e.lag)); lagAt >= 0 {
					conversions *= e.lag[lagAt]
				}
			}
			rows = append(rows,
				[]string{e.id, ts, s, topic, "click_count", strconv.Itoa(int(math.Round(clicks)))},
				[]string{e.id, ts, s, topic, "conversion_count", strconv.Itoa(int(math.Round(conversions)))},
			)
		}
	}
	return rows
}

// The whole file is encoded in memory first so a failed encode leaves no partial file behind
// os errors name the path themselves
func writeCSV(path string, rows [][]string) error {
	var buf bytes.Buffer
	if err := csv.NewWriter(&buf).WriteAll(rows); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func writeJSONL[T any](path string, records []T) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("encode %s: %w", path, err)
		}
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
