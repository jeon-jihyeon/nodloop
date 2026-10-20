// Package datapackage writes a Frictionless Data Package folder of CSV tables that DuckDB, pandas, Arrow and BI tools read with no glue
package datapackage

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	// The profile the descriptor names as its $schema
	Profile = "https://datapackage.org/profiles/2.0/datapackage.json"
	// The descriptor file of the folder
	Descriptor = "datapackage.json"
	// The CSV cell of a null so it stays apart from an empty string
	Null = `\N`
	// Keeps other users out of the folder of the records
	folderMode = 0o700
	// Keeps other users from reading a table
	fileMode = 0o600
)

// One field of a Table Schema
type Field struct {
	Name string `json:"name"`
	// string, datetime, integer, number, boolean, object, array or any
	Type string `json:"type"`
}

// A foreign key of a Table Schema from fields of the table to the fields of another resource
type ForeignKey struct {
	Fields    []string  `json:"fields"`
	Reference Reference `json:"reference"`
}

type Reference struct {
	Resource string   `json:"resource"`
	Fields   []string `json:"fields"`
}

// One table of the package
type Table struct {
	// The name of the resource and of its file without the .csv
	Name        string
	Fields      []Field
	PrimaryKey  []string
	ForeignKeys []ForeignKey
	// What the writer of the package adds under the nodloop key of the resource
	Nodloop any
}

// A folder being written
// Every table goes in as a whole file and the descriptor that lists them replaces the first one at Close
type Package struct {
	folder  string
	name    string
	created time.Time
	tables  []resource
}

// The resource of a written table as the descriptor lists it
type resource struct {
	Name      string      `json:"name"`
	Path      string      `json:"path"`
	Type      string      `json:"type"`
	Format    string      `json:"format"`
	Mediatype string      `json:"mediatype"`
	Encoding  string      `json:"encoding"`
	Schema    tableSchema `json:"schema"`
	Nodloop   any         `json:"nodloop,omitempty"`
}

type tableSchema struct {
	Fields        []Field      `json:"fields"`
	MissingValues []string     `json:"missingValues"`
	PrimaryKey    []string     `json:"primaryKey,omitempty"`
	ForeignKeys   []ForeignKey `json:"foreignKeys,omitempty"`
}

type descriptor struct {
	Schema    string     `json:"$schema"`
	Name      string     `json:"name"`
	Created   string     `json:"created"`
	Nodloop   any        `json:"nodloop"`
	Resources []resource `json:"resources"`
}

// Makes the folder and writes the first descriptor with the nodloop key the writer passes
// 1. the folder is missing or an empty real folder and never a link
// so the records land nowhere a link would send them
// 2. it is made or set to mode 0700
// 3. the first descriptor lists no table so a reader who opens the folder before Close sees the export is not done
func Create(folder, name string, created time.Time, nodloop any) (*Package, error) {
	if err := makeFolder(folder); err != nil {
		return nil, err
	}
	p := &Package{folder: folder, name: name, created: created}
	b, err := p.descriptor(nodloop)
	if err != nil {
		return nil, err
	}
	f, err := create(filepath.Join(folder, Descriptor))
	if err != nil {
		return nil, err
	}
	_, err = f.Write(b)
	return p, errors.Join(err, f.Close())
}

// A missing folder is made with its parents and an existing one must be an empty real folder
func makeFolder(folder string) error {
	info, err := os.Lstat(folder)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(folder), folderMode); err != nil {
			return err
		}
		return os.Mkdir(folder, folderMode)
	case err != nil:
		return err
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%w: %s", ErrFolderLink, folder)
	case !info.IsDir():
		return fmt.Errorf("%w: %s", ErrNotFolder, folder)
	}
	entries, err := os.ReadDir(folder)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: %s", ErrFolderNotEmpty, folder)
	}
	return os.Chmod(folder, folderMode)
}

// Never opens a file or a link that is already there so a link planted in the folder cannot redirect a write
func create(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
}

// Writes the rows as the CSV of the table and lists it as a resource
// Each row is a JSON object whose fields Cell reads by name
func (p *Package) Write(t Table, rows []json.RawMessage) error {
	path := t.Name + ".csv"
	f, err := create(filepath.Join(p.folder, path))
	if err != nil {
		return err
	}
	err = writeRows(f, t.Fields, rows)
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	p.tables = append(p.tables, resource{
		Name: t.Name, Path: path, Type: "table", Format: "csv", Mediatype: "text/csv", Encoding: "utf-8",
		Schema:  tableSchema{Fields: t.Fields, MissingValues: []string{Null}, PrimaryKey: t.PrimaryKey, ForeignKeys: t.ForeignKeys},
		Nodloop: t.Nodloop,
	})
	return nil
}

// The header of the field names and one line per row
func writeRows(f *os.File, fields []Field, rows []json.RawMessage) error {
	w := csv.NewWriter(f)
	head := make([]string, 0, len(fields))
	for _, fd := range fields {
		head = append(head, fd.Name)
	}
	if err := w.Write(head); err != nil {
		return err
	}
	line := make([]string, len(fields))
	for i, raw := range rows {
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil || values == nil {
			return fmt.Errorf("%w: row %d", ErrRowInvalid, i+1)
		}
		for j, fd := range fields {
			cell, err := fd.Cell(values[fd.Name])
			if err != nil {
				return fmt.Errorf("%w: row %d field %s: %w", ErrRowInvalid, i+1, fd.Name, err)
			}
			line[j] = cell
		}
		if err := w.Write(line); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// The CSV cell of the JSON value of the field
// 1. a null or a field the row lacks is Null
// 2. a string of a field of type string or datetime is the string itself
// 3. any other value is its JSON text so an object, an array or a field of any type holds JSON
func (f Field) Cell(v json.RawMessage) (string, error) {
	if len(v) == 0 || string(v) == "null" {
		return Null, nil
	}
	if f.Type != "string" && f.Type != "datetime" {
		return string(v), nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", err
	}
	return s, nil
}

// Replaces the first descriptor by a rename with the one that lists every table written and the nodloop key the writer passes
// A reader sees the old descriptor or the new one and never half of either
func (p *Package) Close(nodloop any) error {
	b, err := p.descriptor(nodloop)
	if err != nil {
		return err
	}
	// CreateTemp opens a new file of mode 0600
	f, err := os.CreateTemp(p.folder, ".datapackage-*.json")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	err = errors.Join(err, f.Close())
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(p.folder, Descriptor))
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}

func (p *Package) descriptor(nodloop any) ([]byte, error) {
	tables := p.tables
	if tables == nil {
		tables = []resource{}
	}
	b, err := json.MarshalIndent(descriptor{
		Schema: Profile, Name: p.name, Created: p.created.UTC().Format(time.RFC3339), Nodloop: nodloop, Resources: tables,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// The Table Schema field of a top level property of a JSON Schema
// 1. a string of format date-time is a datetime
// 2. a type that allows null is the other type it allows
// 3. a property of no type or of several is any
func FieldOf(name string, property json.RawMessage) (Field, error) {
	var s any
	if err := json.Unmarshal(property, &s); err != nil {
		return Field{}, fmt.Errorf("%w: %s: %w", ErrFieldInvalid, name, err)
	}
	obj, _ := s.(map[string]any)
	var types []string
	switch t := obj["type"].(type) {
	case string:
		types = []string{t}
	case []any:
		for _, v := range t {
			if n, ok := v.(string); ok && n != "null" {
				types = append(types, n)
			}
		}
	}
	if len(types) != 1 {
		return Field{Name: name, Type: "any"}, nil
	}
	if types[0] == "string" && obj["format"] == "date-time" {
		return Field{Name: name, Type: "datetime"}, nil
	}
	return Field{Name: name, Type: types[0]}, nil
}
