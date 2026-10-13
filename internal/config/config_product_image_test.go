package config

// config_product_image_test.go — tests for parseProductImageConfig (upload
// size/dimension defaults, explicit values, fail-closed validation).

import (
	"strings"
	"testing"
)

func clearProductImageEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"PRODUCT_IMAGE_DIR",
		"PRODUCT_IMAGE_MAX_BYTES",
		"PRODUCT_IMAGE_MAX_DIMENSION",
		"PRODUCT_IMAGE_MAX_PIXELS",
	} {
		t.Setenv(k, "")
	}
}

func TestParseProductImageConfig_Defaults(t *testing.T) {
	clearProductImageEnv(t)

	cfg, err := parseProductImageConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Dir != "./storage/product-images" {
		t.Errorf("Dir = %q, want ./storage/product-images", cfg.Dir)
	}
	if cfg.MaxBytes != 5242880 {
		t.Errorf("MaxBytes = %d, want 5242880 (5 MiB)", cfg.MaxBytes)
	}
	if cfg.MaxDimension != 4096 {
		t.Errorf("MaxDimension = %d, want 4096", cfg.MaxDimension)
	}
	if cfg.MaxPixels != 16777216 {
		t.Errorf("MaxPixels = %d, want 16777216", cfg.MaxPixels)
	}
}

func TestParseProductImageConfig_ExplicitValues(t *testing.T) {
	clearProductImageEnv(t)
	t.Setenv("PRODUCT_IMAGE_DIR", "/data/images")
	t.Setenv("PRODUCT_IMAGE_MAX_BYTES", "2097152")
	t.Setenv("PRODUCT_IMAGE_MAX_DIMENSION", "2048")
	t.Setenv("PRODUCT_IMAGE_MAX_PIXELS", "4194304")

	cfg, err := parseProductImageConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Dir != "/data/images" || cfg.MaxBytes != 2097152 ||
		cfg.MaxDimension != 2048 || cfg.MaxPixels != 4194304 {
		t.Errorf("cfg = %+v, want /data/images/2097152/2048/4194304", cfg)
	}
}

// A zero, negative, unparseable, or empty value must fail startup — a typo
// can never silently disable upload validation or redirect files.
func TestParseProductImageConfig_RejectsInvalidValues(t *testing.T) {
	cases := []struct{ name, value string }{
		{"PRODUCT_IMAGE_DIR", "   "},
		{"PRODUCT_IMAGE_MAX_BYTES", "0"},
		{"PRODUCT_IMAGE_MAX_BYTES", "-1"},
		{"PRODUCT_IMAGE_MAX_BYTES", "unlimited"},
		{"PRODUCT_IMAGE_MAX_DIMENSION", "0"},
		{"PRODUCT_IMAGE_MAX_DIMENSION", "-100"},
		{"PRODUCT_IMAGE_MAX_DIMENSION", "huge"},
		{"PRODUCT_IMAGE_MAX_PIXELS", "0"},
		{"PRODUCT_IMAGE_MAX_PIXELS", "-5"},
		{"PRODUCT_IMAGE_MAX_PIXELS", "lots"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			clearProductImageEnv(t)
			t.Setenv(tc.name, tc.value)

			_, err := parseProductImageConfig()
			if err == nil {
				t.Fatalf("expected error for %s=%s", tc.name, tc.value)
			}
			if !strings.Contains(err.Error(), tc.name) {
				t.Errorf("error must name %s, got: %v", tc.name, err)
			}
		})
	}
}
