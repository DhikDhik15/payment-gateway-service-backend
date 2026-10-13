package model

import (
	"testing"

	"github.com/google/uuid"
)

func TestParseLocalProductImageRef(t *testing.T) {
	merchant := uuid.New()
	file := uuid.NewString() + ".png"
	raw := ProductImageURLPrefix + merchant.String() + "/" + file

	gotMerchant, gotFile, ok := ParseLocalProductImageRef(raw)
	if !ok {
		t.Fatalf("valid reference %q rejected", raw)
	}
	if gotMerchant != merchant || gotFile != file {
		t.Fatalf("parsed (%v, %q), want (%v, %q)", gotMerchant, gotFile, merchant, file)
	}
	for _, ext := range []string{"jpg", "jpeg", "png", "webp"} {
		ref := ProductImageURLPrefix + merchant.String() + "/" + uuid.NewString() + "." + ext
		if !IsLocalProductImageRef(ref) {
			t.Errorf("allowlisted extension %q rejected: %s", ext, ref)
		}
	}
}

func TestParseLocalProductImageRefRejects(t *testing.T) {
	merchant := uuid.New()
	file := uuid.NewString() + ".png"
	valid := ProductImageURLPrefix + merchant.String() + "/" + file
	cases := map[string]string{
		"external https":     "https://cdn.example.com/a.png",
		"empty":              "",
		"prefix only":        ProductImageURLPrefix,
		"traversal":          ProductImageURLPrefix + merchant.String() + "/../secret.png",
		"nested traversal":   ProductImageURLPrefix + merchant.String() + "/..%2Fsecret.png",
		"absolute path":      ProductImageURLPrefix + merchant.String() + "//etc/passwd",
		"query string":       valid + "?x=1",
		"fragment":           valid + "#x",
		"backslash":          ProductImageURLPrefix + merchant.String() + "/" + file[:8] + "\\x.png",
		"uppercase ext":      ProductImageURLPrefix + merchant.String() + "/" + file[:len(file)-4] + ".PNG",
		"double ext":         ProductImageURLPrefix + merchant.String() + "/" + file + ".exe",
		"no ext":             ProductImageURLPrefix + merchant.String() + "/" + file[:len(file)-4],
		"non-uuid stem":      ProductImageURLPrefix + merchant.String() + "/avatar.png",
		"non-uuid merchant":  ProductImageURLPrefix + "acme/" + file,
		"extra segment":      ProductImageURLPrefix + merchant.String() + "/sub/" + file,
		"unsupported format": ProductImageURLPrefix + merchant.String() + "/" + uuid.NewString() + ".gif",
		"svg":                ProductImageURLPrefix + merchant.String() + "/" + uuid.NewString() + ".svg",
	}
	for name, raw := range cases {
		if _, _, ok := ParseLocalProductImageRef(raw); ok {
			t.Errorf("%s: %q must be rejected", name, raw)
		}
		if IsLocalProductImageRef(raw) {
			t.Errorf("%s: IsLocalProductImageRef(%q) must be false", name, raw)
		}
	}
}
