package clientgen

import (
	"bytes"
	"strings"
	"testing"

	"encr.dev/parser/encoding"
	"encr.dev/pkg/clientgen/clientgentypes"
	meta "encr.dev/proto/encore/parser/meta/v1"
	schema "encr.dev/proto/encore/parser/schema/v1"
)

func TestSwiftToStringExprUsesRawValueForStringUnion(t *testing.T) {
	decls := make([]*schema.Decl, 2)
	decls[1] = &schema.Decl{
		Id:   1,
		Name: "Status",
		Type: &schema.Type{
			Typ: &schema.Type_Union{
				Union: &schema.Union{
					Types: []*schema.Type{
						{
							Typ: &schema.Type_Literal{
								Literal: &schema.Literal{
									Value: &schema.Literal_Str{Str: "open"},
								},
							},
						},
						{
							Typ: &schema.Type_Literal{
								Literal: &schema.Literal{
									Value: &schema.Literal_Str{Str: "closed"},
								},
							},
						},
					},
				},
			},
		},
	}

	sw := &swift{
		md: &meta.Data{
			Decls: decls,
		},
	}

	typ := &schema.Type{
		Typ: &schema.Type_Named{
			Named: &schema.Named{Id: 1},
		},
	}

	got := sw.toStringExpr(typ, "status")
	if got != "status.rawValue" {
		t.Fatalf("toStringExpr() = %q, want %q", got, "status.rawValue")
	}
}

func TestSwiftParamIsOptionalForOptionType(t *testing.T) {
	sw := &swift{md: &meta.Data{Decls: []*schema.Decl{}}}
	p := &encoding.ParameterEncoding{
		Type: &schema.Type{
			Typ: &schema.Type_Option{
				Option: &schema.Option{
					Value: &schema.Type{
						Typ: &schema.Type_Builtin{
							Builtin: schema.Builtin_STRING,
						},
					},
				},
			},
		},
		Optional: false,
	}

	if !sw.paramIsOptional(p) {
		t.Fatal("paramIsOptional() = false, want true for option type")
	}
}

func TestSwiftWriteHeaderJSONAssignmentUsesSourceFieldName(t *testing.T) {
	sw := &swift{md: &meta.Data{Decls: []*schema.Decl{}}}
	var buf bytes.Buffer
	w := &indentWriter{
		w:                &buf,
		depth:            0,
		indent:           "    ",
		firstWriteOnLine: true,
	}

	p := &encoding.ParameterEncoding{
		SrcName:    "order_id",
		WireFormat: "x-order-id",
		Type: &schema.Type{
			Typ: &schema.Type_Builtin{
				Builtin: schema.Builtin_INT64,
			},
		},
	}

	if err := sw.writeHeaderJSONAssignment(w, "mergedObject", p, "rawHeader"); err != nil {
		t.Fatalf("writeHeaderJSONAssignment() returned error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Int64(rawHeader)") {
		t.Fatalf("generated code missing Int64 parse:\n%s", out)
	}
	if !strings.Contains(out, "mergedObject[\"order_id\"] = parsedHeaderValue") {
		t.Fatalf("generated code missing merge assignment by SrcName:\n%s", out)
	}
}

func TestSwiftPrepareDeclTypeNamesDisambiguatesDuplicates(t *testing.T) {
	declA := &schema.Decl{
		Id:   10,
		Name: "HealthCheckResponse",
		Loc: &schema.Loc{
			PkgName: "controllers",
			PkgPath: "api/controllers",
		},
	}
	declB := &schema.Decl{
		Id:   11,
		Name: "HealthCheckResponse",
		Loc: &schema.Loc{
			PkgName: "controllers",
			PkgPath: "mcp/controllers",
		},
	}

	sw := &swift{
		md: &meta.Data{
			Decls: []*schema.Decl{
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				declA,
				declB,
			},
		},
		typs: &typeRegistry{
			namespaces: map[string][]*schema.Decl{
				"controllers": {declA, declB},
			},
		},
	}

	sw.prepareDeclTypeNames()

	nameA := sw.declTypeNames[declA.Id]
	nameB := sw.declTypeNames[declB.Id]
	if nameA == nameB {
		t.Fatalf("expected disambiguated names to differ, got %q and %q", nameA, nameB)
	}
	if !strings.HasPrefix(nameA, "HealthCheckResponse_") {
		t.Fatalf("unexpected disambiguated name %q", nameA)
	}
	if !strings.HasPrefix(nameB, "HealthCheckResponse_") {
		t.Fatalf("unexpected disambiguated name %q", nameB)
	}
}

func TestSwiftServiceClientInitIsFileprivate(t *testing.T) {
	sw := &swift{
		Buffer: &bytes.Buffer{},
		md:     &meta.Data{},
	}
	w := sw.newIndentWriter(0)
	svc := &meta.Service{Name: "svc", Rpcs: []*meta.RPC{}}
	if err := sw.writeServiceClient(w, "svc", svc, clientgentypes.TagSet{}); err != nil {
		t.Fatalf("writeServiceClient returned error: %v", err)
	}

	out := sw.Buffer.String()
	if !strings.Contains(out, "fileprivate init(baseClient: BaseClient)") {
		t.Fatalf("expected fileprivate ServiceClient initializer, got:\n%s", out)
	}
}
