package resource

// Resource metadata for the shells (PRD P2 FR-SH): the Back Office renders
// list / form screens of every master data resource from this description,
// so new P2 masters need no hand-written screen.

import (
	"net/http"
	"sort"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
)

var kindNames = map[Kind]string{String: "text", Text: "textarea", Int: "number", Decimal: "decimal", Bool: "boolean", Date: "date", Timestamp: "datetime",
	UUID: "reference", Enum: "select", Email: "email", JSON: "json", Time: "time", IntList: "intlist", StringList: "list", JSONList: "jsonlist"}

// FieldMeta describes one field.
type FieldMeta struct {
	Name       string   `json:"name"`
	Label      string   `json:"label"`
	Type       string   `json:"type" enum:"text,textarea,number,decimal,boolean,date,datetime,reference,select,email,json,time,intlist,list,jsonlist"`
	Required   bool     `json:"required"`
	Options    []string `json:"options,omitempty"`
	RefPath    string   `json:"refPath,omitempty" doc:"List endpoint of the referenced resource"`
	ReadOnly   bool     `json:"readOnly"`
	CreateOnly bool     `json:"createOnly"`
	Filter     bool     `json:"filter"`
	Default    any      `json:"default,omitempty"`
}

// DefMeta describes one resource.
type DefMeta struct {
	Key       string      `json:"key"`
	Module    string      `json:"module"`
	Tag       string      `json:"tag"`
	Name      string      `json:"name"`
	Plural    string      `json:"plural"`
	Path      string      `json:"path"`
	Perm      string      `json:"perm"`
	CodeField string      `json:"codeField,omitempty"`
	NoDelete  bool        `json:"noDelete"`
	Fields    []FieldMeta `json:"fields"`
}

// Meta lists the resources the caller may view.
func (e *Engine) Meta(p *authz.Principal) []DefMeta {
	byTable := map[string]string{}
	for _, d := range e.defs {
		byTable[d.Table] = d.Path
	}
	out := []DefMeta{}
	for _, d := range e.defs {
		if p == nil {
			continue
		}
		if all, ids := p.PropertiesFor(d.Perm + ".view"); !all && len(ids) == 0 {
			continue
		}
		m := DefMeta{Key: d.Key, Module: d.Module, Tag: d.Tag, Name: d.Name, Plural: d.Plural, Path: d.Path, Perm: d.Perm, CodeField: d.CodeField,
			NoDelete: d.NoDelete, Fields: []FieldMeta{}}
		for _, f := range d.Fields {
			fm := FieldMeta{Name: f.Name, Label: f.Label, Type: kindNames[f.Kind], Required: f.Required, Options: f.Enum, ReadOnly: f.ReadOnly,
				CreateOnly: f.CreateOnly, Filter: f.Filter, Default: f.Default}
			if f.Ref != nil {
				fm.RefPath = byTable[f.Ref.Table]
			}
			if f.Kind == Int && len(f.Enum) > 0 {
				fm.Type = "select"
			}
			m.Fields = append(m.Fields, fm)
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// RegisterMeta adds GET /api/v1/platform/resource-definitions.
func (e *Engine) RegisterMeta(reg *route.Registry) {
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/resource-definitions", Module: "platform", Tag: "Master Data",
		Summary: "Master data resources I can view, with their fields (drives the Back Office screens)", Response: DefMeta{}, List: true,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			httpx.JSON(w, http.StatusOK, httpx.Page[DefMeta]{Items: e.Meta(authz.From(r.Context()))})
		}})
}
