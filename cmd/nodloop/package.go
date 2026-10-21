package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/atomicfile"
	"github.com/jeon-jihyeon/nodloop/internal/datapackage"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
)

// The error of the first descriptor until the export ends
const notFinished = "not finished"

// export package writes the raw records as a Frictionless Data Package folder
func runPackage(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	fs := newFlagSet("export package", stderr)
	var records recordFlags
	records.bind(fs)
	var e packageExport
	fs.StringVar(&e.folder, "out", "", "the folder to write, missing or empty")
	fs.Var((*listFlag)(&e.kinds), "kind", "a record kind to write, repeatable. Every kind when left out")
	fs.StringVar(&e.since, "since", "", "the datapackage.json of an earlier export whose cursors start each kind")
	fs.BoolVar(&e.content, "with-content", false, "also write outputs, reasons, notes, lesson content and paths")
	if err := fs.Parse(args); err != nil {
		return parseFailed(err)
	}
	if e.folder == "" {
		return fail(stderr, "export package", fmt.Errorf("--out %w", errRequired))
	}
	a, err := records.app(getenv, now)
	if err != nil {
		return fail(stderr, "export package", err)
	}
	s, err := a.stores()
	if err != nil {
		return fail(stderr, "export package", err)
	}
	c := packageCommand{source: s.source(a.cfg.home), files: schema.Published, kinds: publishedKinds, now: now, version: buildVersion(), out: stdout}
	if err := c.export(context.Background(), e); err != nil {
		return fail(stderr, "export package", err)
	}
	return 0
}

// What one export writes
type packageExport struct {
	folder string
	// Every kind of the index when empty
	kinds []string
	// The descriptor of an earlier export and empty to write every record
	since   string
	content bool
}

// The kinds of the index the export names in the order of the index
// A name outside the index fails naming every kind
func (e packageExport) selected(ix schema.Index) ([]schema.Kind, error) {
	for _, name := range e.kinds {
		if _, err := ix.Kind(name); err != nil {
			return nil, err
		}
	}
	if len(e.kinds) == 0 {
		return ix.Kinds, nil
	}
	return slices.DeleteFunc(slices.Clone(ix.Kinds), func(k schema.Kind) bool { return !slices.Contains(e.kinds, k.Name) }), nil
}

// The cursor each kind ended at in the descriptor of the earlier export
// A cursor no page answered fails before the folder is made
func (e packageExport) cursors() (map[string]string, error) {
	out := map[string]string{}
	if e.since == "" {
		return out, nil
	}
	b, err := atomicfile.Read(e.since, inputLimit)
	if err != nil {
		return nil, err
	}
	var earlier struct {
		Nodloop packageMeta `json:"nodloop"`
	}
	if err := json.Unmarshal(b, &earlier); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errSinceInvalid, e.since, err)
	}
	for kind, cursor := range earlier.Nodloop.Cursors {
		if _, err := schema.ParseCursor(cursor); err != nil {
			return nil, fmt.Errorf("%w: %s: %s: %w", errSinceInvalid, e.since, kind, err)
		}
	}
	maps.Copy(out, earlier.Nodloop.Cursors)
	return out, nil
}

// What the descriptor keeps under its nodloop key
type packageMeta struct {
	Version string `json:"version"`
	// The cursor of the last record written of each kind so a later export with --since starts after it
	Cursors  map[string]string `json:"cursors"`
	Complete bool              `json:"complete"`
	Error    string            `json:"error,omitempty"`
}

// What a resource keeps under its nodloop key
type tableMeta struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	Rows          int    `json:"rows"`
	ID            string `json:"$id"`
}

// The stores and the kinds an export reads
type packageCommand struct {
	source schema.Source
	// The published files whose index names the kinds and whose schemas give each table its fields
	files   schema.Files
	kinds   kindTable
	now     func() time.Time
	version string
	out     io.Writer
}

// Writes every record of the kinds after their cursors as one table per kind and schema version
// 1. the descriptor says complete false until every table is written and then names the cursor each kind ended at
// 2. a failure keeps the tables written so far with complete false, the error and the cursors of the kinds they hold
func (c packageCommand) export(ctx context.Context, e packageExport) error {
	ix, err := c.files.Index()
	if err != nil {
		return err
	}
	kinds, err := e.selected(ix)
	if err != nil {
		return err
	}
	cursors, err := e.cursors()
	if err != nil {
		return err
	}
	created := c.now().UTC()
	meta := packageMeta{Version: c.version, Cursors: cursors, Error: notFinished}
	pkg, err := datapackage.Create(e.folder, "nodloop-"+created.Format("20060102t150405z"), created, meta)
	if err != nil {
		return err
	}
	written, err := c.write(ctx, pkg, kinds, cursors, e.content)
	meta.Complete, meta.Error = err == nil, ""
	if err != nil {
		meta.Error = err.Error()
	}
	if err := errors.Join(err, pkg.Close(meta)); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "exported %d records of %d kinds to %s\n", written, len(kinds), e.folder)
	return nil
}

// Writes the tables of each kind and moves its cursor to the last record written
func (c packageCommand) write(ctx context.Context, pkg *datapackage.Package, kinds []schema.Kind, cursors map[string]string, content bool) (int, error) {
	newest := map[string]int{}
	for _, k := range kinds {
		newest[k.Name] = k.Version
	}
	written := 0
	for _, k := range kinds {
		// The cursors were checked when they were read
		after, _ := schema.ParseCursor(cursors[k.Name])
		page, next, err := c.source.Read(ctx, k, after, 0)
		if err != nil {
			return written, err
		}
		tables, err := c.tables(k, page, content, newest)
		if err != nil {
			return written, err
		}
		for _, t := range tables {
			if err := pkg.Write(t.Table, t.rows); err != nil {
				return written, err
			}
		}
		if next != (schema.Cursor{}) {
			cursors[k.Name] = next.String()
		}
		written += len(page)
	}
	return written, nil
}

// One table with its rows
type packageTable struct {
	datapackage.Table
	rows []json.RawMessage
}

// One table per schema version the records carry and the newest version even without a record
// newest names the newest version of every kind in the package so a foreign key names a table that exists
func (c packageCommand) tables(k schema.Kind, page schema.Records, content bool, newest map[string]int) ([]packageTable, error) {
	rows := map[int][]json.RawMessage{k.Version: nil}
	for _, r := range page {
		if !content {
			var err error
			if r, err = k.Strip(r); err != nil {
				return nil, err
			}
		}
		rows[r.Version] = append(rows[r.Version], r.JSON)
	}
	var out []packageTable
	for _, version := range slices.Sorted(maps.Keys(rows)) {
		props, err := k.Properties(version)
		if err != nil {
			return nil, err
		}
		fields := make([]datapackage.Field, 0, len(props))
		for _, p := range props {
			f, err := datapackage.FieldOf(p.Name, p.Schema)
			if err != nil {
				return nil, err
			}
			fields = append(fields, f)
		}
		out = append(out, packageTable{Table: datapackage.Table{
			Name: tableName(k.Name, version), Fields: fields, PrimaryKey: []string{"record_id"}, ForeignKeys: c.foreignKeys(k.Name, newest),
			Nodloop: tableMeta{Kind: k.Name, SchemaVersion: version, Rows: len(rows[version]), ID: schema.Base + schema.FileName(k.Name, version)},
		}, rows: rows[version]})
	}
	return out, nil
}

// The foreign keys of the fields of the kind that name a kind the package holds
func (c packageCommand) foreignKeys(kind string, newest map[string]int) []datapackage.ForeignKey {
	var out []datapackage.ForeignKey
	foreign := c.kinds.foreign(kind)
	for _, field := range slices.Sorted(maps.Keys(foreign)) {
		if version, ok := newest[foreign[field]]; ok {
			out = append(out, datapackage.ForeignKey{
				Fields: []string{field}, Reference: datapackage.Reference{Resource: tableName(foreign[field], version), Fields: []string{"record_id"}},
			})
		}
	}
	return out
}

// The resource name of a kind at a version such as run.v1
func tableName(kind string, version int) string {
	return kind + ".v" + strconv.Itoa(version)
}
