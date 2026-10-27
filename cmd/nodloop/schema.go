package main

//go:generate go run . schema --write ../../schema --sdk ../../sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/jeon-jihyeon/nodloop/internal/call"
	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/consolidate"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/guard"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/replay"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

// One record kind the stores write with the Go types its records are built from
type publishedKind struct {
	name        string
	store       schema.Store
	version     int
	description string
	// The type a line of the store decodes into
	record reflect.Type
	// The input and output of a trace name
	// A nil output for a name that records none
	input, output reflect.Type
	links, text   []string
	// Paths of values a model wrote that keep any string even outside the set of their type
	free []string
	// The trace fields of the draw envelope the name carries
	// A trace of any other name never carries draw or source so its file names neither
	envelope []string
	// Top level fields that name a record of one other kind with that kind
	// A Data Package lists them as foreign keys
	foreign map[string]string
}

func (k publishedKind) withFree(paths ...string) publishedKind {
	k.free = paths
	return k
}

func (k publishedKind) withEnvelope(fields ...string) publishedKind {
	k.envelope = fields
	return k
}

// The trace of a name whose ref names a record of the kind
func (k publishedKind) withRef(kind trace.Name) publishedKind {
	k.foreign = map[string]string{"ref": string(kind)}
	return k
}

// The trace of a name with its input and output types
func traceKind(name trace.Name, description string, input, output reflect.Type, links, text []string) publishedKind {
	return publishedKind{
		name: string(name), store: schema.StoreTraces, version: name.SchemaVersion(), description: description,
		record: reflect.TypeFor[trace.Trace](), input: input, output: output, links: links, text: text,
	}
}

// The kinds a build publishes
type kindTable []publishedKind

// Every kind the stores write: each trace name, the verdict, outcome and knowledge stores and the guard log
// The completeness test fails when a stored kind is missing here
var publishedKinds = kindTable{
	traceKind(trace.NameRun, "An output a producer recorded with the items it applied or a holdout withheld",
		reflect.TypeFor[knowledge.RunInput](), reflect.TypeFor[json.RawMessage](),
		[]string{"session_id", "input.applied", "input.withheld", "input.held", "input.eligible", "input.unit", "input.continued"}, []string{"output"}),
	traceKind(trace.NameCheck, "The coverage check of a compaction",
		reflect.TypeFor[struct{}](), reflect.TypeFor[knowledge.Coverage](),
		[]string{"session_id", "output.items.old", "output.items.covered_by"}, []string{"output.items.lost"}),
	traceKind(trace.NameClassify, "The answers of every member a decision point asked",
		reflect.TypeFor[classify.TraceInput](), reflect.TypeFor[classify.TraceOutput](),
		[]string{"ref", "subject"}, []string{"input.state"}).withRef(trace.NameRun),
	traceKind(trace.NameExtract, "One extraction of a lesson from a corrected run with its drafts and conclusion",
		reflect.TypeFor[extract.TraceInput](), reflect.TypeFor[extract.Record](),
		[]string{"ref", "input.verdict_id", "output.attempts.classify", "output.candidate", "output.related", "output.triage.classify"},
		[]string{"output.attempts.draft", "output.attempts.critique", "output.attempts.error", "output.excerpt", "error"}).
		withFree("output.attempts.draft.kind").withRef(trace.NameRun),
	traceKind(trace.NameReplay, "A lesson judged against the recorded outputs it should and should not change",
		reflect.TypeFor[replay.TraceInput](), reflect.TypeFor[replay.Result](),
		[]string{"subject", "input.id", "input.version", "output.cases.run"}, []string{"input.content", "output.cases.why"}),
	traceKind(trace.NameConsolidate, "Groups of waiting candidates of one producer that may say the same lesson",
		reflect.TypeFor[consolidate.TraceInput](), reflect.TypeFor[consolidate.Result](),
		[]string{"subject", "input.clusters", "output.groups.members"}, []string{"output.groups.why", "error"}),
	traceKind(trace.NameRefill, "The items a session start added again after the context of the session was emptied",
		reflect.TypeFor[knowledge.RunInput](), nil, []string{"session_id", "input.applied"}, []string{}),
	traceKind(trace.NameTurn, "The start of a turn of a session with its context item references",
		reflect.TypeFor[turnInput](), nil, []string{"session_id", "input.context"}, []string{}),
	traceKind(trace.NameCalls, "The shapes of tool calls no stop of a run, start of a turn or typed message falls between, with no argument text",
		reflect.TypeFor[call.Input](), nil, []string{"session_id"}, []string{}),
	traceKind(trace.NameCompact, "One drafting of a compaction through a model",
		reflect.TypeFor[struct{}](), nil, []string{"session_id", "subject"}, []string{"error"}),
	traceKind(trace.NameInstructions, "A rules file a session loaded with the HMAC of its content",
		reflect.TypeFor[instructionsInput](), nil, []string{"session_id"}, []string{"input.path"}),
	traceKind(trace.NameCapture, "One pass of the capture over a transcript of a session with the records it wrote",
		reflect.TypeFor[captureInput](), reflect.TypeFor[captureOutput](),
		[]string{"session_id", "output.runs", "output.records", "output.classify", "output.stops", "output.stops.run"}, []string{}),
	traceKind(trace.NameDecide, "One change curate judged with the audit draw of the decision",
		reflect.TypeFor[curate.Change](), reflect.TypeFor[curate.Decision](),
		[]string{
			"ref", "source", "draw.cycle_id", "input.id", "input.version", "input.compaction", "input.old", "input.extract", "input.signal",
			"input.anchor", "input.proposal", "output.judge", "output.triage.classify", "output.triage.grant", "output.delegation",
		},
		[]string{}).withEnvelope("draw", "source").withRef(trace.NameRun),
	traceKind(trace.NameKnob, "The knobs, the seed and the judge of a cycle at its start or at a narrowing inside it",
		reflect.TypeFor[curate.KnobEvent](), nil,
		[]string{
			"subject", "input.cycle_id", "input.policy_version", "input.before", "input.judge.sets",
			"input.changes.grant", "input.knobs.delegation_paused",
		}, []string{}),
	traceKind(trace.NameAnchor, "One version of the judgment of the person on record that curate reads",
		reflect.TypeFor[curate.AnchorVersion](), nil, []string{"input.version", "input.refs", "input.cause"}, []string{"input.files.path"}),
	traceKind(trace.NameExpose, "A question or a notice shown to the person",
		reflect.TypeFor[curate.Exposure](), nil,
		[]string{"session_id", "ref", "source", "draw.cycle_id", "input.change", "input.run"}, []string{}).withEnvelope("draw", "source").
		withRef(trace.NameDecide),
	traceKind(trace.NameAnswer, "The answer of the person to a question or no answer after its second round",
		reflect.TypeFor[curate.Answer](), reflect.TypeFor[curate.AnswerOutput](),
		[]string{
			"session_id", "ref", "source", "draw.cycle_id", "input.decide", "input.change", "input.overturn", "input.authority",
			"input.proof", "input.refs", "output.records", "output.carried",
		},
		[]string{}).withEnvelope("draw", "source").withRef(trace.NameExpose),
	traceKind(trace.NameIntervention, "One action of a person or an agent with the proof of its caller and the records it wrote",
		reflect.TypeFor[trace.Intervention](), reflect.TypeFor[trace.InterventionOutput](),
		[]string{
			"session_id", "source", "input.target", "input.id", "input.version", "input.compaction", "input.decide", "input.refs", "input.reverses",
			"input.authority", "input.proof", "output.records", "output.answer",
		},
		[]string{}).withEnvelope("source"),
	traceKind(trace.NameRollup, "The counts of a closed cycle the reports read once a prune may remove its runs",
		reflect.TypeFor[loop.Rollup](), nil,
		[]string{"subject", "input.cycle_id", "input.producers.units.unit", "input.items.id", "input.items.version", "input.items.units.unit"},
		[]string{}),
	{
		name: "verdict", store: schema.StoreVerdicts, version: feedback.VerdictVersion,
		description: "A verdict on a run: approve, edit, reject or withdraw", record: reflect.TypeFor[feedback.Feedback](),
		links: []string{"trace_id", "reviewer"}, text: []string{"reason", "edited"}, foreign: map[string]string{"trace_id": string(trace.NameRun)},
	},
	{
		name: "outcome", store: schema.StoreOutcomes, version: feedback.OutcomeVersion,
		description: "What a later check of a run found", record: reflect.TypeFor[feedback.Outcome](),
		links: []string{"trace_id"}, text: []string{"confirmed_cause", "note"}, foreign: map[string]string{"trace_id": string(trace.NameRun)},
	},
	{
		name: "knowledge", store: schema.StoreKnowledge, version: knowledge.SchemaVersion,
		description: "One status record of a knowledge version, compaction records included",
		record:      reflect.TypeFor[knowledge.Knowledge](),
		links: []string{
			"id", "version", "base", "supersedes", "evidence.feedback_trace_ids", "evidence.outcome_trace_ids", "evidence.knowledge",
			"extract", "compaction", "decision", "reason", "approver",
		},
		text: []string{"content", "veto.example"},
	},
	{
		name: "guard", store: schema.StoreGuard, version: guard.SchemaVersion,
		description: "A tool call the guard blocked or handed to the person", record: reflect.TypeFor[guard.Entry](),
		links: []string{"session_id", "veto"}, text: []string{"cwd"},
	},
}

// The fields of the kind that name a record of one other kind with that kind
func (kt kindTable) foreign(name string) map[string]string {
	for _, k := range kt {
		if k.name == name {
			return k.foreign
		}
	}
	return nil
}

// The schemas of the typed strings with a valid set and of the types that encode to something else than their Go shape
func typeSchemas() map[reflect.Type]*jsonschema.Schema {
	return map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[json.RawMessage]():        {},
		reflect.TypeFor[time.Time]():              {Type: "string", Format: "date-time"},
		reflect.TypeFor[feedback.Verdict]():       enum(feedback.Verdicts()),
		reflect.TypeFor[feedback.Result]():        enum(feedback.Results()),
		reflect.TypeFor[feedback.Actor]():         enum(feedback.Actors()),
		reflect.TypeFor[trace.Proof]():            enum(trace.Proofs()),
		reflect.TypeFor[trace.Authority]():        enum(trace.Authorities()),
		reflect.TypeFor[trace.Action]():           enum(trace.Actions()),
		reflect.TypeFor[trace.Choice]():           enum(trace.Choices()),
		reflect.TypeFor[trace.Source]():           enum(trace.Sources()),
		reflect.TypeFor[knowledge.Kind]():         enum(knowledge.Kinds()),
		reflect.TypeFor[knowledge.Status]():       enum(knowledge.Statuses()),
		reflect.TypeFor[knowledge.Basis]():        enum(knowledge.Bases()),
		reflect.TypeFor[knowledge.Reason]():       enum(knowledge.Reasons()),
		reflect.TypeFor[veto.Action]():            enum(veto.Actions()),
		reflect.TypeFor[curate.ChangeKind]():      enum(curate.ChangeKinds()),
		reflect.TypeFor[curate.RuleCode]():        enum(curate.RuleCodes()),
		reflect.TypeFor[curate.Action]():          enum(curate.Actions()),
		reflect.TypeFor[curate.FailureClass]():    enum(curate.FailureClasses()),
		reflect.TypeFor[curate.Reopen]():          enum(curate.Reopens()),
		reflect.TypeFor[curate.Outcome]():         enum(curate.Outcomes()),
		reflect.TypeFor[curate.Bin]():             enum(curate.Bins()),
		reflect.TypeFor[curate.QuestionKind]():    enum(curate.QuestionKinds()),
		reflect.TypeFor[curate.Surface]():         enum(curate.Surfaces()),
		reflect.TypeFor[curate.Choice]():          enum(curate.Choices()),
		reflect.TypeFor[curate.KnobCause]():       enum(curate.KnobCauses()),
		reflect.TypeFor[curate.KnobRule]():        enum(curate.KnobRules()),
		reflect.TypeFor[classify.Nature]():        enum(classify.Natures()),
		reflect.TypeFor[classify.Reversibility](): enum(classify.Reversibilities()),
		reflect.TypeFor[classify.Source]():        enum(classify.Sources()),
		reflect.TypeFor[classify.Protection]():    enum(classify.Protections()),
		reflect.TypeFor[loop.Arm]():               enum(loop.Arms()),
	}
}

func enum[T ~string](values []T) *jsonschema.Schema {
	s := &jsonschema.Schema{Type: "string"}
	for _, v := range values {
		s.Enum = append(s.Enum, string(v))
	}
	return s
}

// The schema of the kind as a transported record
// 1. record_kind and record_id first and schema_version pinned to the version of the file
// 2. a trace name pinned with its typed input and output
// 3. objects allow fields they do not name so a new optional field is no break for a reader of the file
func (k publishedKind) schema() (*jsonschema.Schema, error) {
	opts := &jsonschema.ForOptions{TypeSchemas: typeSchemas()}
	s, err := jsonschema.ForType(k.record, opts)
	if err != nil {
		return nil, err
	}
	if k.store == schema.StoreTraces {
		if err := k.typeTrace(s, opts); err != nil {
			return nil, err
		}
	}
	// The domain input keeps run while timed records use a separate wire scope
	if k.store == schema.StoreKnowledge {
		s.Properties["conditional_run"] = s.Properties["run"]
		s.PropertyOrder = slices.Insert(s.PropertyOrder, slices.Index(s.PropertyOrder, "run")+1, "conditional_run")
	}
	s.Schema, s.ID, s.Title, s.Description = schema.Dialect, schema.Base+schema.FileName(k.name, k.version), k.name, k.description
	s.Properties["record_kind"] = &jsonschema.Schema{Type: "string", Const: jsonschema.Ptr[any](k.name)}
	s.Properties["record_id"] = &jsonschema.Schema{Type: "string"}
	s.Properties["schema_version"] = &jsonschema.Schema{Type: "integer", Const: jsonschema.Ptr[any](k.version)}
	stored := func(p string) bool { return p == "schema_version" }
	s.PropertyOrder = slices.Concat([]string{"record_kind", "record_id"}, slices.DeleteFunc(s.PropertyOrder, stored), []string{"schema_version"})
	s.Required = slices.Concat([]string{"record_kind", "record_id"}, slices.DeleteFunc(s.Required, stored), []string{"schema_version"})
	open(s)
	for _, path := range k.free {
		if f := find(s, strings.Split(path, ".")); f != nil {
			f.Enum = nil
		}
	}
	for _, path := range k.text {
		optional(s, strings.Split(path, "."))
	}
	return s, k.check(s)
}

// Leaves the field of a text path out of the required fields of its object
// A transport leaves the text out without the content opt in and the record still validates
func optional(s *jsonschema.Schema, path []string) {
	parent, field := find(s, path[:len(path)-1]), path[len(path)-1]
	if parent != nil {
		parent.Required = slices.DeleteFunc(parent.Required, func(f string) bool { return f == field })
	}
}

// Pins the name of the trace and types its input and output
// The envelope fields the name never carries and the output of a name that records none are left out
func (k publishedKind) typeTrace(s *jsonschema.Schema, opts *jsonschema.ForOptions) error {
	s.Properties["name"] = &jsonschema.Schema{Type: "string", Const: jsonschema.Ptr[any](k.name)}
	in, err := jsonschema.ForType(k.input, opts)
	if err != nil {
		return err
	}
	s.Properties["input"] = in
	unused := slices.DeleteFunc([]string{"draw", "source"}, func(f string) bool { return slices.Contains(k.envelope, f) })
	if k.output == nil {
		unused = append(unused, "output")
	}
	for _, f := range unused {
		delete(s.Properties, f)
	}
	s.PropertyOrder = slices.DeleteFunc(s.PropertyOrder, func(p string) bool { return slices.Contains(unused, p) })
	if k.output == nil {
		return nil
	}
	out, err := jsonschema.ForType(k.output, opts)
	if err != nil {
		return err
	}
	s.Properties["output"] = out
	return nil
}

// Every link, text and free path names a field of the schema so a typo never ships in the index
func (k publishedKind) check(s *jsonschema.Schema) error {
	for _, path := range slices.Concat(k.links, k.text, k.free) {
		if find(s, strings.Split(path, ".")) == nil {
			return fmt.Errorf("%w: %s names no field of %s", errSchemaPath, path, k.name)
		}
	}
	return nil
}

// The schema the property path leads to with arrays passed through and nil when it names no field
func find(s *jsonschema.Schema, path []string) *jsonschema.Schema {
	for s != nil && s.Items != nil {
		s = s.Items
	}
	if s == nil || len(path) == 0 {
		return s
	}
	return find(s.Properties[path[0]], path[1:])
}

// Drops the additionalProperties false a struct infers to so a reader of a file accepts fields added later
func open(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if a := s.AdditionalProperties; a != nil && a.Not != nil && reflect.ValueOf(*a.Not).IsZero() {
		s.AdditionalProperties = nil
	}
	open(s.Items)
	open(s.AdditionalProperties)
	for _, p := range s.Properties {
		open(p)
	}
}

// The index entry of the kind
func (k publishedKind) entry() schema.Kind {
	versions := make([]int, 0, k.version)
	for v := 1; v <= k.version; v++ {
		versions = append(versions, v)
	}
	return schema.Kind{
		Name: k.name, Version: k.version, Versions: versions, Store: k.store, Schema: schema.Base + schema.FileName(k.name, k.version),
		Description: k.description, Links: k.links, Text: k.text,
	}
}

// Every file go generate writes by name: the newest schema of each kind and the index
// Files of older versions stay as they were since their Go types are gone
func (kt kindTable) files(history schema.Files) (map[string][]byte, error) {
	files := map[string][]byte{}
	var ix schema.Index
	for _, k := range kt {
		s, err := k.schema()
		if err != nil {
			return nil, err
		}
		b, err := headed(s)
		if err != nil {
			return nil, err
		}
		files[schema.FileName(k.name, k.version)] = b
		old, err := k.history(history)
		if err != nil {
			return nil, err
		}
		maps.Copy(files, old)
		ix.Kinds = append(ix.Kinds, k.entry())
	}
	// An index of strings and numbers always encodes
	b, _ := json.Marshal(ix)
	files[schema.IndexFile] = indented(b)
	return files, nil
}

// The files of every earlier version of the kind byte for byte from the published history
func (k publishedKind) history(history schema.Files) (map[string][]byte, error) {
	if k.version <= 1 {
		return nil, nil
	}
	ix, err := history.Index()
	if err != nil {
		return nil, err
	}
	old, err := ix.Kind(k.name)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for v := 1; v < k.version; v++ {
		b, err := old.File(v)
		if err != nil {
			return nil, err
		}
		files[schema.FileName(k.name, v)] = b
	}
	return files, nil
}

// The schema with $schema, $id, title and description first so a reader sees what the file is before its fields
func headed(s *jsonschema.Schema) ([]byte, error) {
	// Four strings always encode
	head, _ := json.Marshal(struct {
		Schema      string `json:"$schema"`
		ID          string `json:"$id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}{s.Schema, s.ID, s.Title, s.Description})
	s.Schema, s.ID, s.Title, s.Description = "", "", "", ""
	body, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return indented(slices.Concat(head[:len(head)-1], []byte(","), body[1:])), nil
}

// Two spaces per level and a final newline as the files are committed
func indented(b []byte) []byte {
	var out bytes.Buffer
	// Encoded JSON always indents
	_ = json.Indent(&out, b, "", "  ")
	return append(out.Bytes(), '\n')
}

// The published files a build reads and the kinds it writes them from
type schemaCommand struct {
	files schema.Files
	kinds kindTable
	out   io.Writer
}

// Every kind with its newest version and the store it lives in
func (c schemaCommand) list() error {
	ix, err := c.files.Index()
	if err != nil {
		return err
	}
	for _, k := range ix.Kinds {
		fmt.Fprintf(c.out, "%s\tv%d\t%s\n", k.Name, k.Version, k.Store)
	}
	return nil
}

// The file of the newest version of the kind as it is published
func (c schemaCommand) show(name string) error {
	ix, err := c.files.Index()
	if err != nil {
		return err
	}
	k, err := ix.Kind(name)
	if err != nil {
		return err
	}
	b, err := k.File(k.Version)
	if err != nil {
		return err
	}
	_, err = c.out.Write(b)
	return err
}

// Writes every file of the Go types of this build into the folder as the step of go generate
func (c schemaCommand) write(dir string) error {
	files, err := c.kinds.files(c.files)
	if err != nil {
		return err
	}
	return c.writeFiles(dir, files)
}

// Writes the record types of both SDKs under their folder as the step of go generate
func (c schemaCommand) writeSDK(dir string) error {
	files, err := c.kinds.sdk()
	if err != nil {
		return err
	}
	return c.writeFiles(dir, files)
}

func (c schemaCommand) writeFiles(dir string, files map[string][]byte) error {
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := os.WriteFile(filepath.Join(dir, name), files[name], 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintf(c.out, "wrote %d files to %s\n", len(files), dir)
	return nil
}

// The steps of go generate the flags name
func (c schemaCommand) generate(dir, sdk string) error {
	if dir != "" {
		if err := c.write(dir); err != nil {
			return err
		}
	}
	if sdk != "" {
		return c.writeSDK(sdk)
	}
	return nil
}

func runSchema(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("schema", stderr)
	write := fs.String("write", "", "folder to write every published file into from the Go types of this build")
	sdk := fs.String("sdk", "", "sdk folder to write the record types of the Python and TypeScript SDKs into from the Go types of this build")
	kind, err := parseID(fs, args)
	if err != nil {
		return parseFailed(err)
	}
	cmd := schemaCommand{files: schema.Published, kinds: publishedKinds, out: stdout}
	switch {
	case *write != "" || *sdk != "":
		err = cmd.generate(*write, *sdk)
	case kind != "":
		err = cmd.show(kind)
	default:
		err = cmd.list()
	}
	if err != nil {
		return fail(stderr, "schema", err)
	}
	return 0
}
