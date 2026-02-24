package clientgen

import "testing"

func TestDetectSwift(t *testing.T) {
	lang, ok := Detect("client.swift")
	if !ok {
		t.Fatalf("Detect(client.swift) did not detect language")
	}
	if lang != LangSwift {
		t.Fatalf("Detect(client.swift) = %q, want %q", lang, LangSwift)
	}
}

func TestGetLangSwift(t *testing.T) {
	lang, err := GetLang("swift")
	if err != nil {
		t.Fatalf("GetLang(swift) returned error: %v", err)
	}
	if lang != LangSwift {
		t.Fatalf("GetLang(swift) = %q, want %q", lang, LangSwift)
	}
}
