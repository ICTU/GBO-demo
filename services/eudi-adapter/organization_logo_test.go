package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrganizationLogo(t *testing.T) {
	tests := map[string]struct {
		filename string
		content  []byte
		mimeType string
		encoded  bool
	}{
		"SVG": {
			filename: "logo.svg",
			content:  []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`),
			mimeType: "image/svg+xml",
		},
		"PNG": {
			filename: "logo.png",
			content:  []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 504)),
			mimeType: "image/png",
			encoded:  true,
		},
		"JPEG": {
			filename: "logo.jpg",
			content:  append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), []byte(strings.Repeat("x", 501))...),
			mimeType: "image/jpeg",
			encoded:  true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), test.filename)
			if err := os.WriteFile(path, test.content, 0o600); err != nil {
				t.Fatal(err)
			}
			logo, err := loadOrganizationLogo(path)
			if err != nil {
				t.Fatalf("load logo: %v", err)
			}
			if logo.MimeType != test.mimeType {
				t.Fatalf("mimeType = %q, want %q", logo.MimeType, test.mimeType)
			}
			expected := string(test.content)
			if test.encoded {
				expected = base64.StdEncoding.EncodeToString(test.content)
			}
			if logo.ImageData != expected {
				t.Fatalf("imageData = %q, want %q", logo.ImageData, expected)
			}
		})
	}
}

func TestLoadOrganizationLogoRejectsInvalidInput(t *testing.T) {
	tests := map[string]struct {
		filename string
		content  []byte
	}{
		"empty":       {filename: "logo.svg"},
		"invalid SVG": {filename: "logo.svg", content: []byte(`<html/>`)},
		"wrong type":  {filename: "logo.png", content: []byte("not a PNG")},
		"unsupported": {filename: "logo.gif", content: []byte("GIF89a")},
		"too large":   {filename: "logo.svg", content: []byte(strings.Repeat("x", maximumOrganizationLogoSize+1))},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), test.filename)
			if err := os.WriteFile(path, test.content, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadOrganizationLogo(path); err == nil {
				t.Fatal("invalid logo was accepted")
			}
		})
	}
}
