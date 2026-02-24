package clientgen

import (
	"bufio"
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/cockroachdb/errors"

	"encr.dev/parser/encoding"
	"encr.dev/pkg/clientgen/clientgentypes"
	"encr.dev/pkg/idents"
	meta "encr.dev/proto/encore/parser/meta/v1"
	schema "encr.dev/proto/encore/parser/schema/v1"
)

// swiftGenVersion allows us to introduce breaking changes in generated code.
type swiftGenVersion int

const (
	// SwiftInitial is the originally released Swift client generator.
	SwiftInitial swiftGenVersion = iota

	// SwiftExperimental can be used for in-progress generated code.
	SwiftExperimental
)

const swiftGenLatestVersion = SwiftExperimental - 1

type swift struct {
	*bytes.Buffer
	md               *meta.Data
	appSlug          string
	typs             *typeRegistry
	currDecl         *schema.Decl
	declTypeNames    map[uint32]string
	generatorVersion swiftGenVersion

	hasAuth         bool
	authIsComplex   bool
	authCookieOnly  bool
	authDescription *encoding.AuthEncoding
}

func (sw *swift) Version() int {
	return int(sw.generatorVersion)
}

func (sw *swift) Generate(p clientgentypes.GenerateParams) (err error) {
	sw.Buffer = p.Buf
	sw.md = p.Meta
	sw.appSlug = p.AppSlug
	sw.typs = getNamedTypes(p.Meta, p.Services)
	sw.prepareDeclTypeNames()

	if sw.md.AuthHandler != nil {
		sw.authCookieOnly = sw.isAuthCookieOnly()
		sw.hasAuth = !sw.authCookieOnly
		sw.authIsComplex = sw.hasAuth && sw.md.AuthHandler.Params.GetBuiltin() != schema.Builtin_STRING
		if sw.authIsComplex {
			authDesc, derr := encoding.DescribeAuth(sw.md, sw.md.AuthHandler.Params, &encoding.Options{SrcNameTag: "json"})
			if derr != nil {
				return errors.Wrap(derr, "describe auth")
			}
			sw.authDescription = authDesc
		}
	}

	sw.WriteString("// " + doNotEditHeader() + "\n\n")
	sw.WriteString("import Foundation\n\n")

	sw.writeJSONValue()
	sw.writeErrorTypes()
	sw.writeClient(p.Services)

	seenNs := make(map[string]bool)
	for _, svc := range sw.md.Svcs {
		decls := sw.typs.Decls(svc.Name)
		includeService := hasPublicRPC(svc) && p.Services.Has(svc.Name)
		if len(decls) == 0 && !includeService {
			continue
		}
		if err := sw.writeNamespace(svc.Name, decls, svc, includeService, p.Tags); err != nil {
			return err
		}
		seenNs[svc.Name] = true
	}
	for _, ns := range sw.typs.Namespaces() {
		if seenNs[ns] {
			continue
		}
		if err := sw.writeNamespace(ns, sw.typs.Decls(ns), nil, false, clientgentypes.TagSet{}); err != nil {
			return err
		}
	}

	if err := sw.writeBaseClient(); err != nil {
		return err
	}
	return nil
}

func (sw *swift) writeClient(set clientgentypes.ServiceSet) {
	w := sw.newIndentWriter(0)
	w.WriteString("public typealias BaseURL = String\n\n")
	w.WriteString("public let Local: BaseURL = \"http://localhost:4000\"\n\n")
	w.WriteString("public func Environment(_ name: String) -> BaseURL {\n")
	w.Indent().WriteStringf("return \"https://\\(name)-%s.encr.app\"\n", sw.appSlug)
	w.WriteString("}\n\n")
	w.WriteString("public func PreviewEnv(_ pr: Int) -> BaseURL {\n")
	w.Indent().WriteString("return Environment(\"pr\\(pr)\")\n")
	w.WriteString("}\n\n")

	if sw.hasAuth {
		if sw.authIsComplex {
			w.WriteString("public typealias AuthData = ")
			w.WriteString(sw.swiftType("", sw.md.AuthHandler.Params))
			w.WriteString("\n")
			w.WriteString("public typealias AuthDataProvider = () async -> AuthData?\n\n")
		} else {
			w.WriteString("public typealias AuthTokenProvider = () async -> String?\n\n")
		}
	}

	w.WriteString("public struct ClientOptions {\n")
	{
		ww := w.Indent()
		ww.WriteString("public var session: URLSession\n")
		ww.WriteString("public var requestHeaders: [String: String]\n")
		if sw.hasAuth {
			if sw.authIsComplex {
				ww.WriteString("public var authData: AuthData?\n")
				ww.WriteString("public var authDataProvider: AuthDataProvider?\n")
			} else {
				ww.WriteString("public var authToken: String?\n")
				ww.WriteString("public var authTokenProvider: AuthTokenProvider?\n")
			}
		}

		ww.WriteString("\n")
		ww.WriteString("public init(\n")
		{
			www := ww.Indent()
			www.WriteString("session: URLSession = .shared,\n")
			www.WriteString("requestHeaders: [String: String] = [:]")
			if sw.hasAuth {
				if sw.authIsComplex {
					www.WriteString(",\nauthData: AuthData? = nil,\nauthDataProvider: AuthDataProvider? = nil")
				} else {
					www.WriteString(",\nauthToken: String? = nil,\nauthTokenProvider: AuthTokenProvider? = nil")
				}
			}
			www.WriteString("\n")
		}
		ww.WriteString(") {\n")
		{
			www := ww.Indent()
			www.WriteString("self.session = session\n")
			www.WriteString("self.requestHeaders = requestHeaders\n")
			if sw.hasAuth {
				if sw.authIsComplex {
					www.WriteString("self.authData = authData\n")
					www.WriteString("self.authDataProvider = authDataProvider\n")
				} else {
					www.WriteString("self.authToken = authToken\n")
					www.WriteString("self.authTokenProvider = authTokenProvider\n")
				}
			}
		}
		ww.WriteString("}\n")
	}
	w.WriteString("}\n\n")

	w.WriteString("public final class Client {\n")
	{
		ww := w.Indent()
		ww.WriteString("private let baseClient: BaseClient\n")
		for _, svc := range sw.md.Svcs {
			if !hasPublicRPC(svc) || !set.Has(svc.Name) {
				continue
			}
			ww.WriteStringf("public let %s: %s.ServiceClient\n", sw.memberName(svc.Name), sw.namespaceName(svc.Name))
		}
		ww.WriteString("\n")
		ww.WriteString("public init(_ target: BaseURL, options: ClientOptions = ClientOptions()) {\n")
		{
			www := ww.Indent()
			www.WriteString("self.baseClient = BaseClient(target: target, options: options)\n")
			for _, svc := range sw.md.Svcs {
				if !hasPublicRPC(svc) || !set.Has(svc.Name) {
					continue
				}
				www.WriteStringf("self.%s = %s.ServiceClient(baseClient: self.baseClient)\n", sw.memberName(svc.Name), sw.namespaceName(svc.Name))
			}
		}
		ww.WriteString("}\n")
	}
	w.WriteString("}\n\n")
}

func (sw *swift) writeNamespace(ns string, decls []*schema.Decl, svc *meta.Service, includeService bool, tags clientgentypes.TagSet) error {
	if len(decls) == 0 && !includeService {
		return nil
	}

	nsName := sw.namespaceName(ns)
	sw.WriteString(fmt.Sprintf("public enum %s {\n", nsName))
	w := sw.newIndentWriter(1)

	sortedDecls := append([]*schema.Decl(nil), decls...)
	sort.Slice(sortedDecls, func(i, j int) bool {
		return sortedDecls[i].Name < sortedDecls[j].Name
	})

	for i, decl := range sortedDecls {
		if i > 0 {
			w.WriteString("\n")
		}
		sw.writeDeclDef(w, ns, decl)
	}

	if includeService && svc != nil {
		if len(sortedDecls) > 0 {
			w.WriteString("\n")
		}
		if err := sw.writeServiceClient(w, ns, svc, tags); err != nil {
			return err
		}
	}

	sw.WriteString("}\n\n")
	return nil
}

func (sw *swift) writeDeclDef(w *indentWriter, ns string, decl *schema.Decl) {
	prevDecl := sw.currDecl
	sw.currDecl = decl
	defer func() { sw.currDecl = prevDecl }()

	if decl.Doc != "" {
		scanner := bufio.NewScanner(strings.NewReader(decl.Doc))
		for scanner.Scan() {
			w.WriteString("/// ")
			w.WriteString(scanner.Text())
			w.WriteString("\n")
		}
	}

	typeParams := sw.swiftTypeParamsDecl(decl)
	if allStringCases, ok := sw.stringUnionCases(decl.Type); ok && len(decl.TypeParams) == 0 {
		w.WriteStringf("public enum %s%s: String, Codable {\n", sw.declTypeName(decl), typeParams)
		ww := w.Indent()
		for _, c := range allStringCases {
			ww.WriteStringf("case %s = %s\n", sw.enumCaseName(c), strconv.Quote(c))
		}
		w.WriteString("}\n")
		return
	}

	if st := sw.resolveStructType(decl.Type); st != nil {
		w.WriteStringf("public struct %s%s: Codable {\n", sw.declTypeName(decl), typeParams)
		ww := w.Indent()

		fields := make([]*schema.Field, 0, len(st.Fields))
		for _, f := range st.Fields {
			if f.Wire.GetCookie() != nil || encoding.IgnoreField(f) {
				continue
			}
			fields = append(fields, f)
		}

		needsCodingKeys := false
		for _, field := range fields {
			orig := sw.fieldNameInStruct(field)
			swiftName := sw.fieldName(orig)
			if sw.unquote(swiftName) != orig {
				needsCodingKeys = true
			}

			if field.Doc != "" {
				scanner := bufio.NewScanner(strings.NewReader(field.Doc))
				for scanner.Scan() {
					ww.WriteString("/// ")
					ww.WriteString(scanner.Text())
					ww.WriteString("\n")
				}
			}

			typ := sw.swiftType(ns, field.Typ)
			if field.Optional || sw.isRecursive(field.Typ) {
				typ = sw.ensureOptional(typ)
			}
			ww.WriteStringf("public var %s: %s\n", swiftName, typ)
		}

		if needsCodingKeys {
			ww.WriteString("\n")
			ww.WriteString("enum CodingKeys: String, CodingKey {\n")
			www := ww.Indent()
			for _, field := range fields {
				orig := sw.fieldNameInStruct(field)
				swiftName := sw.fieldName(orig)
				if sw.unquote(swiftName) == orig {
					www.WriteStringf("case %s\n", swiftName)
				} else {
					www.WriteStringf("case %s = %s\n", swiftName, strconv.Quote(orig))
				}
			}
			ww.WriteString("}\n")
		}

		w.WriteString("}\n")
		return
	}

	w.WriteStringf("public typealias %s%s = %s\n", sw.declTypeName(decl), typeParams, sw.swiftType(ns, decl.Type))
}

func (sw *swift) writeServiceClient(w *indentWriter, ns string, svc *meta.Service, tags clientgentypes.TagSet) error {
	w.WriteString("public final class ServiceClient {\n")
	ww := w.Indent()
	ww.WriteString("private let baseClient: BaseClient\n\n")
	ww.WriteString("fileprivate init(baseClient: BaseClient) {\n")
	ww.Indent().WriteString("self.baseClient = baseClient\n")
	ww.WriteString("}\n")

	for _, rpc := range svc.Rpcs {
		if rpc.AccessType == meta.RPC_PRIVATE || !tags.IsRPCIncluded(rpc) {
			continue
		}
		if rpc.StreamingRequest || rpc.StreamingResponse {
			continue
		}
		ww.WriteString("\n")
		if err := sw.writeRPC(ww, ns, rpc); err != nil {
			return errors.Wrapf(err, "rpc %s.%s", svc.Name, rpc.Name)
		}
	}

	w.WriteString("}\n")
	return nil
}

func (sw *swift) writeRPC(w *indentWriter, ns string, rpc *meta.RPC) error {
	if rpc.Doc != nil && *rpc.Doc != "" {
		scanner := bufio.NewScanner(strings.NewReader(*rpc.Doc))
		for scanner.Scan() {
			w.WriteString("/// ")
			w.WriteString(scanner.Text())
			w.WriteString("\n")
		}
	}

	rpcEncoding, err := encoding.DescribeRPC(sw.md, rpc, &encoding.Options{SrcNameTag: "json"})
	if err != nil {
		return err
	}

	sigParts := make([]string, 0, 4)
	for _, seg := range rpc.Path.Segments {
		if seg.Type == meta.PathSegment_LITERAL {
			continue
		}
		sigParts = append(sigParts, fmt.Sprintf("%s: %s", sw.fieldName(seg.Value), sw.swiftPathType(seg)))
	}

	if rpc.Proto == meta.RPC_RAW {
		sigParts = append(sigParts,
			"method: String",
			"body: Data? = nil",
			"headers: [String: String] = [:]",
			"queryItems: [URLQueryItem] = []",
		)
	} else if rpc.RequestSchema != nil {
		sigParts = append(sigParts, fmt.Sprintf("params: %s", sw.swiftType(ns, rpc.RequestSchema)))
	}

	ret := "Void"
	if rpc.Proto == meta.RPC_RAW {
		ret = "Data"
	} else if rpc.ResponseSchema != nil {
		ret = sw.swiftType(ns, rpc.ResponseSchema)
	}

	w.WriteStringf("public func %s(%s) async throws -> %s {\n", sw.memberName(rpc.Name), strings.Join(sigParts, ", "), ret)
	ww := w.Indent()
	ww.WriteStringf("let path = %s\n", sw.swiftPathExpr(rpc))

	if rpc.Proto == meta.RPC_RAW {
		ww.WriteString("let (data, _) = try await self.baseClient.callAPI(\n")
		www := ww.Indent()
		www.WriteString("method: method,\n")
		www.WriteString("path: path,\n")
		www.WriteString("body: body,\n")
		www.WriteString("headers: headers,\n")
		www.WriteString("queryItems: queryItems\n")
		ww.WriteString(")\n")
		ww.WriteString("return data\n")
		w.WriteString("}\n")
		return nil
	}

	reqEnc := rpcEncoding.DefaultRequestEncoding
	if reqEnc == nil {
		reqEnc = &encoding.RequestEncoding{}
	}
	hasHeaders := len(reqEnc.HeaderParameters) > 0
	hasQuery := len(reqEnc.QueryParameters) > 0
	if hasHeaders {
		ww.WriteString("var headers: [String: String] = [:]\n")
		for _, p := range reqEnc.HeaderParameters {
			if err := sw.writeHeaderAssignment(ww, "params", p); err != nil {
				return err
			}
		}
	} else {
		ww.WriteString("let headers: [String: String] = [:]\n")
	}

	if hasQuery {
		ww.WriteString("var queryItems: [URLQueryItem] = []\n")
		for _, p := range reqEnc.QueryParameters {
			if err := sw.writeQueryAssignment(ww, "params", p); err != nil {
				return err
			}
		}
	} else {
		ww.WriteString("let queryItems: [URLQueryItem] = []\n")
	}

	if len(reqEnc.BodyParameters) > 0 {
		if len(reqEnc.HeaderParameters) == 0 && len(reqEnc.QueryParameters) == 0 {
			ww.WriteString("let bodyData = try JSONEncoder().encode(params)\n")
		} else {
			ww.WriteString("struct BodyPayload: Encodable {\n")
			www := ww.Indent()
			needCodingKeys := false
			for _, p := range reqEnc.BodyParameters {
				name := sw.fieldName(p.SrcName)
				typ := sw.swiftType(ns, p.Type)
				if p.Optional {
					typ = sw.ensureOptional(typ)
				}
				www.WriteStringf("let %s: %s\n", name, typ)
				if sw.unquote(name) != p.WireFormat {
					needCodingKeys = true
				}
			}
			if needCodingKeys {
				www.WriteString("\n")
				www.WriteString("enum CodingKeys: String, CodingKey {\n")
				wwww := www.Indent()
				for _, p := range reqEnc.BodyParameters {
					name := sw.fieldName(p.SrcName)
					if sw.unquote(name) == p.WireFormat {
						wwww.WriteStringf("case %s\n", name)
					} else {
						wwww.WriteStringf("case %s = %s\n", name, strconv.Quote(p.WireFormat))
					}
				}
				www.WriteString("}\n")
			}
			ww.WriteString("}\n")

			args := make([]string, 0, len(reqEnc.BodyParameters))
			for _, p := range reqEnc.BodyParameters {
				name := sw.fieldName(p.SrcName)
				args = append(args, fmt.Sprintf("%s: %s", name, sw.fieldAccess("params", p.SrcName)))
			}
			ww.WriteStringf("let payload = BodyPayload(%s)\n", strings.Join(args, ", "))
			ww.WriteString("let bodyData = try JSONEncoder().encode(payload)\n")
		}
	} else {
		ww.WriteString("let bodyData: Data? = nil\n")
	}

	respEnc := rpcEncoding.ResponseEncoding
	if respEnc == nil {
		respEnc = &encoding.ResponseEncoding{}
	}

	respVar := "_"
	if len(respEnc.HeaderParameters) > 0 {
		respVar = "response"
	}

	ww.WriteStringf("let (data, %s) = try await self.baseClient.callAPI(\n", respVar)
	www := ww.Indent()
	www.WriteStringf("method: %s,\n", strconv.Quote(rpcEncoding.DefaultMethod))
	www.WriteString("path: path,\n")
	www.WriteString("body: bodyData,\n")
	www.WriteString("headers: headers,\n")
	www.WriteString("queryItems: queryItems\n")
	ww.WriteString(")\n")

	if rpc.ResponseSchema == nil {
		ww.WriteString("return\n")
	} else {
		if len(respEnc.HeaderParameters) == 0 {
			ww.WriteStringf("return try self.baseClient.decode(%s.self, from: data)\n", sw.swiftType(ns, rpc.ResponseSchema))
		} else {
			ww.WriteString("var mergedObject: [String: Any] = [:]\n")
			ww.WriteString("if !data.isEmpty {\n")
			{
				www := ww.Indent()
				www.WriteString("let bodyJSON = try JSONSerialization.jsonObject(with: data)\n")
				www.WriteString("guard let bodyObject = bodyJSON as? [String: Any] else {\n")
				www.Indent().WriteString("throw SwiftClientError.invalidResponseBodyShape\n")
				www.WriteString("}\n")
				www.WriteString("mergedObject = bodyObject\n")
			}
			ww.WriteString("}\n")

			for _, p := range respEnc.HeaderParameters {
				if sw.paramIsOptional(p) {
					ww.WriteStringf("if let rawHeader = response.value(forHTTPHeaderField: %s) {\n", strconv.Quote(p.WireFormat))
					if err := sw.writeHeaderJSONAssignment(ww.Indent(), "mergedObject", p, "rawHeader"); err != nil {
						return err
					}
					ww.WriteString("}\n")
				} else {
					ww.WriteStringf("guard let rawHeader = response.value(forHTTPHeaderField: %s) else {\n", strconv.Quote(p.WireFormat))
					ww.Indent().WriteStringf("throw SwiftClientError.missingHeader(%s)\n", strconv.Quote(p.WireFormat))
					ww.WriteString("}\n")
					if err := sw.writeHeaderJSONAssignment(ww, "mergedObject", p, "rawHeader"); err != nil {
						return err
					}
				}
			}

			ww.WriteString("let mergedData = try JSONSerialization.data(withJSONObject: mergedObject)\n")
			ww.WriteStringf("return try self.baseClient.decode(%s.self, from: mergedData)\n", sw.swiftType(ns, rpc.ResponseSchema))
		}
	}

	w.WriteString("}\n")
	return nil
}

func (sw *swift) writeHeaderAssignment(w *indentWriter, baseVar string, p *encoding.ParameterEncoding) error {
	access := sw.fieldAccess(baseVar, p.SrcName)
	isOptional := sw.paramIsOptional(p)
	if elemType := sw.listElementType(p.Type, map[uint32]bool{}); elemType != nil {
		if isOptional {
			w.WriteStringf("if let values = %s {\n", access)
			ww := w.Indent()
			ww.WriteStringf("headers[%s] = values.map { value in %s }.joined(separator: \",\")\n", strconv.Quote(p.WireFormat), sw.toStringExpr(elemType, "value"))
			w.WriteString("}\n")
		} else {
			w.WriteStringf("headers[%s] = %s.map { value in %s }.joined(separator: \",\")\n", strconv.Quote(p.WireFormat), access, sw.toStringExpr(elemType, "value"))
		}
		return nil
	}

	if isOptional {
		w.WriteStringf("if let value = %s {\n", access)
		ww := w.Indent()
		ww.WriteStringf("headers[%s] = %s\n", strconv.Quote(p.WireFormat), sw.toStringExpr(p.Type, "value"))
		w.WriteString("}\n")
	} else {
		w.WriteStringf("headers[%s] = %s\n", strconv.Quote(p.WireFormat), sw.toStringExpr(p.Type, access))
	}
	return nil
}

func (sw *swift) writeQueryAssignment(w *indentWriter, baseVar string, p *encoding.ParameterEncoding) error {
	access := sw.fieldAccess(baseVar, p.SrcName)
	isOptional := sw.paramIsOptional(p)
	if elemType := sw.listElementType(p.Type, map[uint32]bool{}); elemType != nil {
		if isOptional {
			w.WriteStringf("if let values = %s {\n", access)
			ww := w.Indent()
			ww.WriteString("for value in values {\n")
			www := ww.Indent()
			www.WriteStringf("queryItems.append(URLQueryItem(name: %s, value: %s))\n", strconv.Quote(p.WireFormat), sw.toStringExpr(elemType, "value"))
			ww.WriteString("}\n")
			w.WriteString("}\n")
		} else {
			w.WriteStringf("for value in %s {\n", access)
			ww := w.Indent()
			ww.WriteStringf("queryItems.append(URLQueryItem(name: %s, value: %s))\n", strconv.Quote(p.WireFormat), sw.toStringExpr(elemType, "value"))
			w.WriteString("}\n")
		}
		return nil
	}

	if isOptional {
		w.WriteStringf("if let value = %s {\n", access)
		ww := w.Indent()
		ww.WriteStringf("queryItems.append(URLQueryItem(name: %s, value: %s))\n", strconv.Quote(p.WireFormat), sw.toStringExpr(p.Type, "value"))
		w.WriteString("}\n")
	} else {
		w.WriteStringf("queryItems.append(URLQueryItem(name: %s, value: %s))\n", strconv.Quote(p.WireFormat), sw.toStringExpr(p.Type, access))
	}
	return nil
}

func (sw *swift) toStringExpr(typ *schema.Type, valueExpr string) string {
	if typ == nil {
		return fmt.Sprintf("String(describing: %s)", valueExpr)
	}

	switch tt := typ.Typ.(type) {
	case *schema.Type_Option:
		return sw.toStringExpr(tt.Option.Value, valueExpr)
	case *schema.Type_Pointer:
		return sw.toStringExpr(tt.Pointer.Base, valueExpr)
	case *schema.Type_Config:
		return sw.toStringExpr(tt.Config.Elem, valueExpr)
	case *schema.Type_Named:
		if decl := sw.md.Decls[tt.Named.Id]; decl != nil {
			if _, ok := sw.stringUnionCases(decl.Type); ok && len(decl.TypeParams) == 0 {
				return valueExpr + ".rawValue"
			}
		}
		return fmt.Sprintf("String(describing: %s)", valueExpr)
	case *schema.Type_Builtin:
		switch tt.Builtin {
		case schema.Builtin_STRING, schema.Builtin_UUID, schema.Builtin_USER_ID, schema.Builtin_DECIMAL, schema.Builtin_TIME, schema.Builtin_BYTES:
			return valueExpr
		case schema.Builtin_BOOL:
			return fmt.Sprintf("(%s ? \"true\" : \"false\")", valueExpr)
		case schema.Builtin_JSON:
			return fmt.Sprintf("stringifyJSONValue(%s)", valueExpr)
		case schema.Builtin_INT8, schema.Builtin_INT16, schema.Builtin_INT32, schema.Builtin_INT64, schema.Builtin_INT,
			schema.Builtin_UINT8, schema.Builtin_UINT16, schema.Builtin_UINT32, schema.Builtin_UINT64, schema.Builtin_UINT,
			schema.Builtin_FLOAT32, schema.Builtin_FLOAT64:
			return fmt.Sprintf("String(%s)", valueExpr)
		default:
			return fmt.Sprintf("String(describing: %s)", valueExpr)
		}
	default:
		return fmt.Sprintf("String(describing: %s)", valueExpr)
	}
}

func (sw *swift) paramIsOptional(p *encoding.ParameterEncoding) bool {
	return p.Optional || sw.typeIsOptional(p.Type, map[uint32]bool{})
}

func (sw *swift) typeIsOptional(typ *schema.Type, seen map[uint32]bool) bool {
	if typ == nil {
		return false
	}
	switch tt := typ.Typ.(type) {
	case *schema.Type_Option, *schema.Type_Pointer:
		return true
	case *schema.Type_Config:
		return sw.typeIsOptional(tt.Config.Elem, seen)
	case *schema.Type_Named:
		if seen[tt.Named.Id] {
			return false
		}
		seen[tt.Named.Id] = true
		decl := sw.md.Decls[tt.Named.Id]
		if decl == nil {
			return false
		}
		return sw.typeIsOptional(decl.Type, seen)
	default:
		return false
	}
}

func (sw *swift) listElementType(typ *schema.Type, seen map[uint32]bool) *schema.Type {
	if typ == nil {
		return nil
	}

	switch tt := typ.Typ.(type) {
	case *schema.Type_List:
		return tt.List.Elem
	case *schema.Type_Option:
		return sw.listElementType(tt.Option.Value, seen)
	case *schema.Type_Pointer:
		return sw.listElementType(tt.Pointer.Base, seen)
	case *schema.Type_Config:
		return sw.listElementType(tt.Config.Elem, seen)
	case *schema.Type_Named:
		if seen[tt.Named.Id] {
			return nil
		}
		seen[tt.Named.Id] = true
		decl := sw.md.Decls[tt.Named.Id]
		if decl == nil {
			return nil
		}
		return sw.listElementType(decl.Type, seen)
	default:
		return nil
	}
}

func (sw *swift) writeHeaderJSONAssignment(w *indentWriter, targetMap string, p *encoding.ParameterEncoding, rawVar string) error {
	key := strconv.Quote(p.SrcName)

	typ := p.Type
	if elemType := sw.listElementType(typ, map[uint32]bool{}); elemType != nil {
		w.WriteStringf("let headerParts = %s.split(separator: \",\").map { String($0).trimmingCharacters(in: .whitespaces) }\n", rawVar)
		w.WriteString("var parsedHeaderValues: [Any] = []\n")
		w.WriteString("for headerPart in headerParts {\n")
		ww := w.Indent()
		if err := sw.writeScalarHeaderJSONValue(ww, elemType, "headerPart", "parsedHeaderValue", p.WireFormat, map[uint32]bool{}); err != nil {
			return err
		}
		ww.WriteString("parsedHeaderValues.append(parsedHeaderValue)\n")
		w.WriteString("}\n")
		w.WriteStringf("%s[%s] = parsedHeaderValues\n", targetMap, key)
		return nil
	}

	w.WriteString("do {\n")
	ww := w.Indent()
	if err := sw.writeScalarHeaderJSONValue(ww, typ, rawVar, "parsedHeaderValue", p.WireFormat, map[uint32]bool{}); err != nil {
		return err
	}
	ww.WriteStringf("%s[%s] = parsedHeaderValue\n", targetMap, key)
	w.WriteString("}\n")
	return nil
}

func (sw *swift) writeScalarHeaderJSONValue(
	w *indentWriter,
	typ *schema.Type,
	rawExpr string,
	outVar string,
	headerName string,
	seen map[uint32]bool,
) error {
	if typ == nil {
		w.WriteStringf("let %s: Any = %s\n", outVar, rawExpr)
		return nil
	}

	switch tt := typ.Typ.(type) {
	case *schema.Type_Option:
		return sw.writeScalarHeaderJSONValue(w, tt.Option.Value, rawExpr, outVar, headerName, seen)
	case *schema.Type_Pointer:
		return sw.writeScalarHeaderJSONValue(w, tt.Pointer.Base, rawExpr, outVar, headerName, seen)
	case *schema.Type_Config:
		return sw.writeScalarHeaderJSONValue(w, tt.Config.Elem, rawExpr, outVar, headerName, seen)
	case *schema.Type_Named:
		if seen[tt.Named.Id] {
			w.WriteStringf("let %s: Any = %s\n", outVar, rawExpr)
			return nil
		}
		decl := sw.md.Decls[tt.Named.Id]
		if decl == nil {
			w.WriteStringf("let %s: Any = %s\n", outVar, rawExpr)
			return nil
		}
		if _, ok := sw.stringUnionCases(decl.Type); ok && len(decl.TypeParams) == 0 {
			w.WriteStringf("let %s: Any = %s\n", outVar, rawExpr)
			return nil
		}
		seen[tt.Named.Id] = true
		return sw.writeScalarHeaderJSONValue(w, decl.Type, rawExpr, outVar, headerName, seen)
	case *schema.Type_Literal:
		switch tt.Literal.Value.(type) {
		case *schema.Literal_Boolean:
			w.WriteStringf("let loweredHeaderValue = %s.lowercased()\n", rawExpr)
			w.WriteString("guard loweredHeaderValue == \"true\" || loweredHeaderValue == \"false\" else {\n")
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"Bool\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = (loweredHeaderValue == \"true\")\n", outVar)
		case *schema.Literal_Int:
			w.WriteStringf("guard let parsedHeaderInt = Int(%s) else {\n", rawExpr)
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"Int\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderInt\n", outVar)
		case *schema.Literal_Float:
			w.WriteStringf("guard let parsedHeaderDouble = Double(%s) else {\n", rawExpr)
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"Double\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderDouble\n", outVar)
		default:
			w.WriteStringf("let %s: Any = %s\n", outVar, rawExpr)
		}
		return nil
	case *schema.Type_Builtin:
		switch tt.Builtin {
		case schema.Builtin_BOOL:
			w.WriteStringf("let loweredHeaderValue = %s.lowercased()\n", rawExpr)
			w.WriteString("guard loweredHeaderValue == \"true\" || loweredHeaderValue == \"false\" else {\n")
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"Bool\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = (loweredHeaderValue == \"true\")\n", outVar)
		case schema.Builtin_INT8:
			w.WriteStringf("guard let parsedHeaderInt = Int8(%s) else {\n", rawExpr)
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"Int8\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderInt\n", outVar)
		case schema.Builtin_INT16:
			w.WriteStringf("guard let parsedHeaderInt = Int16(%s) else {\n", rawExpr)
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"Int16\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderInt\n", outVar)
		case schema.Builtin_INT32:
			w.WriteStringf("guard let parsedHeaderInt = Int32(%s) else {\n", rawExpr)
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"Int32\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderInt\n", outVar)
		case schema.Builtin_INT64:
			w.WriteStringf("guard let parsedHeaderInt = Int64(%s) else {\n", rawExpr)
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"Int64\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderInt\n", outVar)
		case schema.Builtin_INT:
			w.WriteStringf("guard let parsedHeaderInt = Int(%s) else {\n", rawExpr)
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"Int\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderInt\n", outVar)
		case schema.Builtin_UINT8:
			w.WriteStringf("guard let parsedHeaderInt = UInt8(%s) else {\n", rawExpr)
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"UInt8\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderInt\n", outVar)
		case schema.Builtin_UINT16:
			w.WriteStringf("guard let parsedHeaderInt = UInt16(%s) else {\n", rawExpr)
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"UInt16\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderInt\n", outVar)
		case schema.Builtin_UINT32:
			w.WriteStringf("guard let parsedHeaderInt = UInt32(%s) else {\n", rawExpr)
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"UInt32\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderInt\n", outVar)
		case schema.Builtin_UINT64:
			w.WriteStringf("guard let parsedHeaderInt = UInt64(%s) else {\n", rawExpr)
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"UInt64\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderInt\n", outVar)
		case schema.Builtin_UINT:
			w.WriteStringf("guard let parsedHeaderInt = UInt(%s) else {\n", rawExpr)
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"UInt\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderInt\n", outVar)
		case schema.Builtin_FLOAT32:
			w.WriteStringf("guard let parsedHeaderFloat = Float(%s) else {\n", rawExpr)
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"Float\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderFloat\n", outVar)
		case schema.Builtin_FLOAT64:
			w.WriteStringf("guard let parsedHeaderDouble = Double(%s) else {\n", rawExpr)
			w.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"Double\")\n", strconv.Quote(headerName), rawExpr)
			w.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderDouble\n", outVar)
		case schema.Builtin_JSON:
			w.WriteStringf("guard let parsedHeaderJSONData = %s.data(using: .utf8),\n", rawExpr)
			ww := w.Indent()
			ww.WriteString("let parsedHeaderJSONValue = try? JSONSerialization.jsonObject(with: parsedHeaderJSONData) else {\n")
			ww.Indent().WriteStringf("throw SwiftClientError.invalidHeaderValue(name: %s, value: %s, expected: \"JSON\")\n", strconv.Quote(headerName), rawExpr)
			ww.WriteString("}\n")
			w.WriteStringf("let %s: Any = parsedHeaderJSONValue\n", outVar)
		default:
			w.WriteStringf("let %s: Any = %s\n", outVar, rawExpr)
		}
		return nil
	default:
		w.WriteStringf("let %s: Any = %s\n", outVar, rawExpr)
		return nil
	}
}

func (sw *swift) writeBaseClient() error {
	w := sw.newIndentWriter(0)
	w.WriteString("private final class BaseClient {\n")
	{
		ww := w.Indent()
		ww.WriteString("private let baseURL: String\n")
		ww.WriteString("private let session: URLSession\n")
		ww.WriteString("private let requestHeaders: [String: String]\n")
		if sw.hasAuth {
			if sw.authIsComplex {
				ww.WriteString("private let authData: AuthData?\n")
				ww.WriteString("private let authDataProvider: AuthDataProvider?\n")
			} else {
				ww.WriteString("private let authToken: String?\n")
				ww.WriteString("private let authTokenProvider: AuthTokenProvider?\n")
			}
		}
		ww.WriteString("\n")

		ww.WriteString("init(target: BaseURL, options: ClientOptions) {\n")
		{
			www := ww.Indent()
			www.WriteString("self.baseURL = target\n")
			www.WriteString("self.session = options.session\n")
			www.WriteString("self.requestHeaders = options.requestHeaders\n")
			if sw.hasAuth {
				if sw.authIsComplex {
					www.WriteString("self.authData = options.authData\n")
					www.WriteString("self.authDataProvider = options.authDataProvider\n")
				} else {
					www.WriteString("self.authToken = options.authToken\n")
					www.WriteString("self.authTokenProvider = options.authTokenProvider\n")
				}
			}
		}
		ww.WriteString("}\n\n")

		if sw.hasAuth {
			if sw.authIsComplex {
				ww.WriteString("private func resolveAuthData() async -> AuthData? {\n")
				{
					www := ww.Indent()
					www.WriteString("if let provider = authDataProvider {\n")
					www.Indent().WriteString("return await provider()\n")
					www.WriteString("}\n")
					www.WriteString("return authData\n")
				}
				ww.WriteString("}\n\n")
			} else {
				ww.WriteString("private func resolveAuthToken() async -> String? {\n")
				{
					www := ww.Indent()
					www.WriteString("if let provider = authTokenProvider {\n")
					www.Indent().WriteString("return await provider()\n")
					www.WriteString("}\n")
					www.WriteString("return authToken\n")
				}
				ww.WriteString("}\n\n")
			}
		}

		ww.WriteString("func callAPI(\n")
		{
			www := ww.Indent()
			www.WriteString("method: String,\n")
			www.WriteString("path: String,\n")
			www.WriteString("body: Data? = nil,\n")
			www.WriteString("headers: [String: String] = [:],\n")
			www.WriteString("queryItems: [URLQueryItem] = []\n")
		}
		ww.WriteString(") async throws -> (Data, HTTPURLResponse) {\n")
		{
			www := ww.Indent()
			www.WriteString("guard var components = URLComponents(string: baseURL + path) else {\n")
			www.Indent().WriteString("throw SwiftClientError.invalidURL(baseURL + path)\n")
			www.WriteString("}\n")
			hasDynamicAuthQueryItems := sw.hasAuth &&
				sw.authIsComplex &&
				sw.authDescription != nil &&
				len(sw.authDescription.QueryParameters) > 0
			if hasDynamicAuthQueryItems {
				www.WriteString("var effectiveQueryItems = queryItems\n")
			} else {
				www.WriteString("let effectiveQueryItems = queryItems\n")
			}
			www.WriteString("var effectiveHeaders = requestHeaders\n")
			www.WriteString("for (k, v) in headers {\n")
			www.Indent().WriteString("effectiveHeaders[k] = v\n")
			www.WriteString("}\n")

			if sw.hasAuth {
				if sw.authIsComplex {
					www.WriteString("if let authData = await resolveAuthData() {\n")
					if sw.authDescription != nil {
						aw := www.Indent()
						for _, p := range sw.authDescription.QueryParameters {
							if err := sw.writeAuthQueryAssignment(aw, p); err != nil {
								return err
							}
						}
						for _, p := range sw.authDescription.HeaderParameters {
							if err := sw.writeAuthHeaderAssignment(aw, p); err != nil {
								return err
							}
						}
					}
					www.WriteString("}\n")
				} else {
					www.WriteString("if let token = await resolveAuthToken(), !token.isEmpty {\n")
					www.Indent().WriteString("effectiveHeaders[\"Authorization\"] = \"Bearer \\(token)\"\n")
					www.WriteString("}\n")
				}
			}

			www.WriteString("if !effectiveQueryItems.isEmpty {\n")
			www.Indent().WriteString("components.queryItems = effectiveQueryItems\n")
			www.WriteString("}\n")
			www.WriteString("guard let url = components.url else {\n")
			www.Indent().WriteString("throw SwiftClientError.invalidURL(baseURL + path)\n")
			www.WriteString("}\n")
			www.WriteString("var request = URLRequest(url: url)\n")
			www.WriteString("request.httpMethod = method\n")
			www.WriteString("request.httpBody = body\n")
			www.WriteString("if body != nil && effectiveHeaders[\"Content-Type\"] == nil {\n")
			www.Indent().WriteString("effectiveHeaders[\"Content-Type\"] = \"application/json\"\n")
			www.WriteString("}\n")
			www.WriteString("for (k, v) in effectiveHeaders {\n")
			www.Indent().WriteString("request.setValue(v, forHTTPHeaderField: k)\n")
			www.WriteString("}\n")

			www.WriteString("let (data, response) = try await session.data(for: request)\n")
			www.WriteString("guard let httpResponse = response as? HTTPURLResponse else {\n")
			www.Indent().WriteString("throw SwiftClientError.invalidResponse\n")
			www.WriteString("}\n")
			www.WriteString("guard (200...299).contains(httpResponse.statusCode) else {\n")
			{
				wwww := www.Indent()
				wwww.WriteString("if let apiErr = try? JSONDecoder().decode(APIErrorResponse.self, from: data) {\n")
				wwww.Indent().WriteString("throw APIError(statusCode: httpResponse.statusCode, response: apiErr)\n")
				wwww.WriteString("}\n")
				wwww.WriteString("throw APIError(statusCode: httpResponse.statusCode, response: APIErrorResponse(code: \"unknown\", message: \"request failed with status \\(httpResponse.statusCode)\", details: nil))\n")
			}
			www.WriteString("}\n")
			www.WriteString("return (data, httpResponse)\n")
		}
		ww.WriteString("}\n\n")

		ww.WriteString("func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {\n")
		{
			www := ww.Indent()
			www.WriteString("if data.isEmpty, T.self == EmptyResponse.self {\n")
			www.Indent().WriteString("return EmptyResponse() as! T\n")
			www.WriteString("}\n")
			www.WriteString("let decoder = JSONDecoder()\n")
			www.WriteString("decoder.dateDecodingStrategy = .iso8601\n")
			www.WriteString("return try decoder.decode(type, from: data)\n")
		}
		ww.WriteString("}\n")
	}
	w.WriteString("}\n\n")

	w.WriteString("private func encodePathSegment(_ value: String) -> String {\n")
	{
		ww := w.Indent()
		ww.WriteString("var allowed = CharacterSet.urlPathAllowed\n")
		ww.WriteString("allowed.remove(charactersIn: \"/\")\n")
		ww.WriteString("return value.addingPercentEncoding(withAllowedCharacters: allowed) ?? value\n")
	}
	w.WriteString("}\n\n")

	w.WriteString("private func stringifyJSONValue(_ value: JSONValue) -> String {\n")
	{
		ww := w.Indent()
		ww.WriteString("guard let data = try? JSONEncoder().encode(value),\n")
		www := ww.Indent()
		www.WriteString("let raw = String(data: data, encoding: .utf8) else {\n")
		www.Indent().WriteString("return \"null\"\n")
		www.WriteString("}\n")
		ww.WriteString("return raw\n")
	}
	w.WriteString("}\n\n")

	return nil
}

func (sw *swift) writeAuthHeaderAssignment(w *indentWriter, p *encoding.ParameterEncoding) error {
	access := sw.fieldAccess("authData", p.SrcName)
	isOptional := sw.paramIsOptional(p)
	if elemType := sw.listElementType(p.Type, map[uint32]bool{}); elemType != nil {
		if isOptional {
			w.WriteStringf("if let values = %s {\n", access)
			ww := w.Indent()
			ww.WriteStringf("effectiveHeaders[%s] = values.map { value in %s }.joined(separator: \",\")\n", strconv.Quote(p.WireFormat), sw.toStringExpr(elemType, "value"))
			w.WriteString("}\n")
		} else {
			w.WriteStringf("effectiveHeaders[%s] = %s.map { value in %s }.joined(separator: \",\")\n", strconv.Quote(p.WireFormat), access, sw.toStringExpr(elemType, "value"))
		}
		return nil
	}

	if isOptional {
		w.WriteStringf("if let value = %s {\n", access)
		ww := w.Indent()
		ww.WriteStringf("effectiveHeaders[%s] = %s\n", strconv.Quote(p.WireFormat), sw.toStringExpr(p.Type, "value"))
		w.WriteString("}\n")
	} else {
		w.WriteStringf("effectiveHeaders[%s] = %s\n", strconv.Quote(p.WireFormat), sw.toStringExpr(p.Type, access))
	}
	return nil
}

func (sw *swift) writeAuthQueryAssignment(w *indentWriter, p *encoding.ParameterEncoding) error {
	access := sw.fieldAccess("authData", p.SrcName)
	isOptional := sw.paramIsOptional(p)
	if elemType := sw.listElementType(p.Type, map[uint32]bool{}); elemType != nil {
		if isOptional {
			w.WriteStringf("if let values = %s {\n", access)
			ww := w.Indent()
			ww.WriteString("for value in values {\n")
			www := ww.Indent()
			www.WriteStringf("effectiveQueryItems.append(URLQueryItem(name: %s, value: %s))\n", strconv.Quote(p.WireFormat), sw.toStringExpr(elemType, "value"))
			ww.WriteString("}\n")
			w.WriteString("}\n")
		} else {
			w.WriteStringf("for value in %s {\n", access)
			ww := w.Indent()
			ww.WriteStringf("effectiveQueryItems.append(URLQueryItem(name: %s, value: %s))\n", strconv.Quote(p.WireFormat), sw.toStringExpr(elemType, "value"))
			w.WriteString("}\n")
		}
		return nil
	}

	if isOptional {
		w.WriteStringf("if let value = %s {\n", access)
		ww := w.Indent()
		ww.WriteStringf("effectiveQueryItems.append(URLQueryItem(name: %s, value: %s))\n", strconv.Quote(p.WireFormat), sw.toStringExpr(p.Type, "value"))
		w.WriteString("}\n")
	} else {
		w.WriteStringf("effectiveQueryItems.append(URLQueryItem(name: %s, value: %s))\n", strconv.Quote(p.WireFormat), sw.toStringExpr(p.Type, access))
	}
	return nil
}

func (sw *swift) writeJSONValue() {
	sw.WriteString(`
public enum JSONValue: Codable {
    case string(String)
    case number(Double)
    case bool(Bool)
    case object([String: JSONValue])
    case array([JSONValue])
    case null

    public init(from decoder: Decoder) throws {
        if let container = try? decoder.singleValueContainer() {
            if container.decodeNil() {
                self = .null
                return
            }
            if let value = try? container.decode(Bool.self) {
                self = .bool(value)
                return
            }
            if let value = try? container.decode(Double.self) {
                self = .number(value)
                return
            }
            if let value = try? container.decode(String.self) {
                self = .string(value)
                return
            }
            if let value = try? container.decode([String: JSONValue].self) {
                self = .object(value)
                return
            }
            if let value = try? container.decode([JSONValue].self) {
                self = .array(value)
                return
            }
        }
        throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "Unsupported JSON value"))
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        switch self {
        case .string(let value):
            try container.encode(value)
        case .number(let value):
            try container.encode(value)
        case .bool(let value):
            try container.encode(value)
        case .object(let value):
            try container.encode(value)
        case .array(let value):
            try container.encode(value)
        case .null:
            try container.encodeNil()
        }
    }
}

`)
}

func (sw *swift) writeErrorTypes() {
	sw.WriteString(`
public enum SwiftClientError: Error {
    case invalidURL(String)
    case invalidResponse
    case invalidResponseBodyShape
    case missingHeader(String)
    case invalidHeaderValue(name: String, value: String, expected: String)
}

public struct APIErrorResponse: Codable {
    public let code: String
    public let message: String
    public let details: JSONValue?

    public init(code: String, message: String, details: JSONValue? = nil) {
        self.code = code
        self.message = message
        self.details = details
    }
}

public struct APIError: Error, CustomStringConvertible {
    public let statusCode: Int
    public let code: String
    public let message: String
    public let details: JSONValue?

    public init(statusCode: Int, response: APIErrorResponse) {
        self.statusCode = statusCode
        self.code = response.code
        self.message = response.message
        self.details = response.details
    }

    public var description: String {
        "APIError(status=\(statusCode), code=\(code), message=\(message))"
    }
}

public struct EmptyResponse: Codable {
    public init() {}
}

`)
}

func (sw *swift) swiftPathExpr(rpc *meta.RPC) string {
	var b strings.Builder
	b.WriteString("\"")
	for _, seg := range rpc.Path.Segments {
		b.WriteString("/")
		if seg.Type == meta.PathSegment_LITERAL {
			b.WriteString(seg.Value)
			continue
		}
		name := sw.fieldName(seg.Value)
		if seg.Type == meta.PathSegment_WILDCARD || seg.Type == meta.PathSegment_FALLBACK {
			b.WriteString("\\(")
			b.WriteString(name)
			b.WriteString(".map { encodePathSegment(String(describing: $0)) }.joined(separator: \"/\"))")
		} else {
			b.WriteString("\\(encodePathSegment(String(describing: ")
			b.WriteString(name)
			b.WriteString(")))")
		}
	}
	b.WriteString("\"")
	return b.String()
}

func (sw *swift) swiftPathType(seg *meta.PathSegment) string {
	base := "String"
	switch seg.ValueType {
	case meta.PathSegment_BOOL:
		base = "Bool"
	case meta.PathSegment_INT8:
		base = "Int8"
	case meta.PathSegment_INT16:
		base = "Int16"
	case meta.PathSegment_INT32:
		base = "Int32"
	case meta.PathSegment_INT64:
		base = "Int64"
	case meta.PathSegment_INT:
		base = "Int"
	case meta.PathSegment_UINT8:
		base = "UInt8"
	case meta.PathSegment_UINT16:
		base = "UInt16"
	case meta.PathSegment_UINT32:
		base = "UInt32"
	case meta.PathSegment_UINT64:
		base = "UInt64"
	case meta.PathSegment_UINT:
		base = "UInt"
	case meta.PathSegment_STRING, meta.PathSegment_UUID:
		base = "String"
	}
	if seg.Type == meta.PathSegment_WILDCARD || seg.Type == meta.PathSegment_FALLBACK {
		return "[" + base + "]"
	}
	return base
}

func (sw *swift) namespaceName(identifier string) string {
	name := idents.Convert(identifier, idents.PascalCase)
	if name == "" {
		return "Namespace"
	}
	if unicode.IsDigit([]rune(name)[0]) {
		name = "_" + name
	}
	return sw.safeIdentifier(name)
}

func (sw *swift) typeName(identifier string) string {
	name := idents.Convert(identifier, idents.PascalCase)
	if name == "" {
		return "Type"
	}
	if unicode.IsDigit([]rune(name)[0]) {
		name = "_" + name
	}
	return sw.safeIdentifier(name)
}

func (sw *swift) memberName(identifier string) string {
	name := idents.Convert(identifier, idents.CamelCase)
	if name == "" {
		return "value"
	}
	if unicode.IsDigit([]rune(name)[0]) {
		name = "_" + name
	}
	return sw.safeIdentifier(name)
}

func (sw *swift) fieldName(identifier string) string {
	if identifier == "" {
		return "value"
	}
	if unicode.IsDigit([]rune(identifier)[0]) {
		identifier = "_" + identifier
	}
	return sw.safeIdentifier(identifier)
}

func (sw *swift) fieldAccess(base, field string) string {
	name := sw.fieldName(field)
	return base + "." + name
}

func (sw *swift) safeIdentifier(identifier string) string {
	if identifier == "" {
		return "_"
	}

	var b strings.Builder
	for i, r := range identifier {
		if i == 0 {
			if !(unicode.IsLetter(r) || r == '_' || r == '$') {
				b.WriteRune('_')
				if unicode.IsDigit(r) {
					b.WriteRune(r)
				}
				continue
			}
			b.WriteRune(r)
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}

	out := b.String()
	switch out {
	case "associatedtype", "class", "deinit", "enum", "extension", "fileprivate", "func", "import", "init", "inout", "internal", "let", "open", "operator", "private", "protocol", "public", "static", "struct", "subscript", "typealias", "var", "break", "case", "continue", "default", "defer", "do", "else", "fallthrough", "for", "guard", "if", "in", "repeat", "return", "switch", "where", "while", "as", "Any", "catch", "false", "is", "nil", "rethrows", "super", "self", "Self", "throw", "throws", "true", "try", "associativity", "convenience", "dynamic", "didSet", "final", "get", "infix", "indirect", "lazy", "left", "mutating", "none", "nonmutating", "optional", "override", "postfix", "precedence", "prefix", "Protocol", "required", "right", "set", "Type", "unowned", "weak", "willSet", "async", "await", "actor", "some":
		return "`" + out + "`"
	default:
		return out
	}
}

func (sw *swift) unquote(identifier string) string {
	return strings.Trim(identifier, "`")
}

func (sw *swift) declTypeName(decl *schema.Decl) string {
	if decl == nil {
		return "Type"
	}
	if name, ok := sw.declTypeNames[decl.Id]; ok && name != "" {
		return name
	}
	return sw.typeName(decl.Name)
}

func (sw *swift) prepareDeclTypeNames() {
	sw.declTypeNames = make(map[uint32]string)

	for _, ns := range sw.typs.Namespaces() {
		decls := append([]*schema.Decl(nil), sw.typs.Decls(ns)...)
		sort.Slice(decls, func(i, j int) bool {
			return decls[i].Id < decls[j].Id
		})

		baseNameCount := make(map[string]int)
		for _, decl := range decls {
			baseNameCount[sw.typeName(decl.Name)]++
		}

		usedNames := make(map[string]bool)
		for _, decl := range decls {
			base := sw.typeName(decl.Name)
			name := base
			if baseNameCount[base] > 1 {
				disamb := sw.declDisambiguator(decl, ns)
				if disamb != "" {
					name = sw.safeIdentifier(base + "_" + disamb)
				}
			}

			if name == "" || usedNames[name] {
				name = sw.safeIdentifier(fmt.Sprintf("%s_D%d", base, decl.Id))
			}
			if usedNames[name] {
				i := 2
				for usedNames[name] {
					name = sw.safeIdentifier(fmt.Sprintf("%s_D%d_%d", base, decl.Id, i))
					i++
				}
			}

			sw.declTypeNames[decl.Id] = name
			usedNames[name] = true
		}
	}
}

func (sw *swift) declDisambiguator(decl *schema.Decl, ns string) string {
	if decl == nil || decl.Loc == nil {
		return ""
	}

	if path := strings.TrimSpace(decl.Loc.PkgPath); path != "" {
		parts := strings.Split(path, "/")
		filtered := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			filtered = append(filtered, p)
		}
		if len(filtered) > 0 {
			last := filtered[len(filtered)-1]
			if strings.EqualFold(last, ns) {
				filtered = filtered[:len(filtered)-1]
			}
		}
		if len(filtered) > 0 {
			var b strings.Builder
			for _, part := range filtered {
				token := idents.Convert(part, idents.PascalCase)
				if token != "" {
					b.WriteString(token)
				}
			}
			if b.Len() > 0 {
				return sw.unquote(sw.safeIdentifier(b.String()))
			}
		}
	}

	if filename := strings.TrimSpace(decl.Loc.Filename); filename != "" {
		filename = strings.TrimSuffix(filename, ".go")
		filename = strings.TrimSuffix(filename, ".ts")
		filename = strings.TrimSuffix(filename, ".js")
		filename = strings.TrimSuffix(filename, ".swift")
		token := idents.Convert(filename, idents.PascalCase)
		if token != "" {
			return sw.unquote(sw.safeIdentifier(token))
		}
	}

	return fmt.Sprintf("D%d", decl.Id)
}

func (sw *swift) swiftTypeParamsDecl(decl *schema.Decl) string {
	if len(decl.TypeParams) == 0 {
		return ""
	}
	parts := make([]string, 0, len(decl.TypeParams))
	for _, p := range decl.TypeParams {
		parts = append(parts, fmt.Sprintf("%s: Codable", sw.safeIdentifier(p.Name)))
	}
	return "<" + strings.Join(parts, ", ") + ">"
}

func (sw *swift) swiftType(ns string, typ *schema.Type) string {
	if typ == nil {
		return "Void"
	}
	return sw.renderType(ns, typ)
}

func (sw *swift) renderType(ns string, typ *schema.Type) string {
	switch tt := typ.Typ.(type) {
	case *schema.Type_Named:
		decl := sw.md.Decls[tt.Named.Id]
		if decl == nil {
			return "JSONValue"
		}
		name := sw.declTypeName(decl)
		if decl.Loc.PkgName != ns {
			name = sw.namespaceName(decl.Loc.PkgName) + "." + name
		}
		if len(tt.Named.TypeArguments) > 0 {
			args := make([]string, 0, len(tt.Named.TypeArguments))
			for _, arg := range tt.Named.TypeArguments {
				args = append(args, sw.renderType(ns, arg))
			}
			name += "<" + strings.Join(args, ", ") + ">"
		}
		return name
	case *schema.Type_List:
		return "[" + sw.renderType(ns, tt.List.Elem) + "]"
	case *schema.Type_Map:
		key := sw.renderType(ns, tt.Map.Key)
		if key != "String" {
			key = "String"
		}
		return "[" + key + ": " + sw.renderType(ns, tt.Map.Value) + "]"
	case *schema.Type_Builtin:
		return sw.builtinType(tt.Builtin)
	case *schema.Type_Literal:
		switch tt.Literal.Value.(type) {
		case *schema.Literal_Str:
			return "String"
		case *schema.Literal_Boolean:
			return "Bool"
		case *schema.Literal_Int:
			return "Int"
		case *schema.Literal_Float:
			return "Double"
		case *schema.Literal_Null:
			return "JSONValue?"
		default:
			return "JSONValue"
		}
	case *schema.Type_Pointer:
		return sw.renderType(ns, tt.Pointer.Base)
	case *schema.Type_Option:
		return sw.ensureOptional(sw.renderType(ns, tt.Option.Value))
	case *schema.Type_Union:
		return sw.unionType(ns, tt.Union.Types)
	case *schema.Type_Struct:
		return "JSONValue"
	case *schema.Type_TypeParameter:
		decl := sw.md.Decls[tt.TypeParameter.DeclId]
		if decl == nil || int(tt.TypeParameter.ParamIdx) >= len(decl.TypeParams) {
			return "JSONValue"
		}
		return sw.safeIdentifier(decl.TypeParams[tt.TypeParameter.ParamIdx].Name)
	case *schema.Type_Config:
		return sw.renderType(ns, tt.Config.Elem)
	default:
		return "JSONValue"
	}
}

func (sw *swift) unionType(ns string, types []*schema.Type) string {
	if len(types) == 0 {
		return "JSONValue"
	}

	nonNull := make([]*schema.Type, 0, len(types))
	hasNull := false
	allStringLiterals := true
	for _, t := range types {
		if lit := t.GetLiteral(); lit != nil {
			if _, ok := lit.Value.(*schema.Literal_Null); ok {
				hasNull = true
				continue
			}
			if _, ok := lit.Value.(*schema.Literal_Str); !ok {
				allStringLiterals = false
			}
		} else {
			allStringLiterals = false
		}
		nonNull = append(nonNull, t)
	}

	if len(nonNull) == 1 {
		base := sw.renderType(ns, nonNull[0])
		if hasNull {
			return sw.ensureOptional(base)
		}
		return base
	}

	if allStringLiterals {
		if hasNull {
			return "String?"
		}
		return "String"
	}

	return "JSONValue"
}

func (sw *swift) ensureOptional(typ string) string {
	if strings.HasSuffix(typ, "?") {
		return typ
	}
	return typ + "?"
}

func (sw *swift) builtinType(typ schema.Builtin) string {
	switch typ {
	case schema.Builtin_ANY, schema.Builtin_JSON:
		return "JSONValue"
	case schema.Builtin_BOOL:
		return "Bool"
	case schema.Builtin_INT8:
		return "Int8"
	case schema.Builtin_INT16:
		return "Int16"
	case schema.Builtin_INT32:
		return "Int32"
	case schema.Builtin_INT64:
		return "Int64"
	case schema.Builtin_INT:
		return "Int"
	case schema.Builtin_UINT8:
		return "UInt8"
	case schema.Builtin_UINT16:
		return "UInt16"
	case schema.Builtin_UINT32:
		return "UInt32"
	case schema.Builtin_UINT64:
		return "UInt64"
	case schema.Builtin_UINT:
		return "UInt"
	case schema.Builtin_FLOAT32:
		return "Float"
	case schema.Builtin_FLOAT64:
		return "Double"
	case schema.Builtin_STRING, schema.Builtin_BYTES, schema.Builtin_TIME, schema.Builtin_UUID, schema.Builtin_USER_ID, schema.Builtin_DECIMAL:
		return "String"
	default:
		return "JSONValue"
	}
}

func (sw *swift) resolveStructType(typ *schema.Type) *schema.Struct {
	if typ == nil {
		return nil
	}
	switch tt := typ.Typ.(type) {
	case *schema.Type_Struct:
		return tt.Struct
	case *schema.Type_Named:
		decl := sw.md.Decls[tt.Named.Id]
		if decl == nil {
			return nil
		}
		return sw.resolveStructType(decl.Type)
	default:
		return nil
	}
}

func (sw *swift) fieldNameInStruct(field *schema.Field) string {
	if field.JsonName != "" {
		return field.JsonName
	}
	return field.Name
}

func (sw *swift) isRecursive(typ *schema.Type) bool {
	if sw.currDecl == nil {
		return false
	}
	n := typ.GetNamed()
	if n == nil {
		return false
	}
	return sw.typs.IsRecursiveRef(sw.currDecl.Id, n.Id)
}

func (sw *swift) newIndentWriter(indent int) *indentWriter {
	return &indentWriter{
		w:                sw.Buffer,
		depth:            indent,
		indent:           "    ",
		firstWriteOnLine: true,
	}
}

func (sw *swift) isAuthCookieOnly() bool {
	if sw.md.AuthHandler == nil {
		return false
	}
	fields := sw.getFields(sw.md.AuthHandler.Params)
	if fields == nil {
		return false
	}
	for _, field := range fields {
		if field.Wire.GetCookie() == nil {
			return false
		}
	}
	return true
}

func (sw *swift) getFields(typ *schema.Type) []*schema.Field {
	if typ == nil {
		return nil
	}
	switch typ.Typ.(type) {
	case *schema.Type_Struct:
		return typ.GetStruct().Fields
	case *schema.Type_Named:
		decl := sw.md.Decls[typ.GetNamed().Id]
		if decl == nil {
			return nil
		}
		return sw.getFields(decl.Type)
	default:
		return nil
	}
}

func (sw *swift) stringUnionCases(typ *schema.Type) ([]string, bool) {
	u := typ.GetUnion()
	if u == nil {
		return nil, false
	}
	cases := make([]string, 0, len(u.Types))
	for _, t := range u.Types {
		lit := t.GetLiteral()
		if lit == nil {
			return nil, false
		}
		s, ok := lit.Value.(*schema.Literal_Str)
		if !ok {
			return nil, false
		}
		cases = append(cases, s.Str)
	}
	if len(cases) == 0 {
		return nil, false
	}
	return cases, true
}

func (sw *swift) enumCaseName(v string) string {
	if v == "" {
		return "empty"
	}
	v = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return '_'
	}, strings.ToLower(v))
	if v == "" {
		v = "empty"
	}
	if unicode.IsDigit([]rune(v)[0]) {
		v = "_" + v
	}
	return sw.safeIdentifier(v)
}
