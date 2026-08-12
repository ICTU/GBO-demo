package main

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maximumOrganizationLogoSize = 128 * 1024

type organizationLogo struct {
	MimeType  string `json:"mimeType"`
	ImageData string `json:"imageData"`
}

func loadOrganizationLogo(path string) (*organizationLogo, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read source logo: %w", err)
	}
	if len(content) == 0 {
		return nil, fmt.Errorf("source logo is empty")
	}
	if len(content) > maximumOrganizationLogoSize {
		return nil, fmt.Errorf("source logo exceeds %d bytes", maximumOrganizationLogoSize)
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".svg":
		var root struct {
			XMLName xml.Name
		}
		if err := xml.Unmarshal(content, &root); err != nil || root.XMLName.Local != "svg" {
			return nil, fmt.Errorf("source logo is not a valid SVG")
		}
		return &organizationLogo{MimeType: "image/svg+xml", ImageData: string(content)}, nil
	case ".png":
		if detected := http.DetectContentType(content); detected != "image/png" {
			return nil, fmt.Errorf("source logo extension is PNG but content is %s", detected)
		}
		return &organizationLogo{MimeType: "image/png", ImageData: base64.StdEncoding.EncodeToString(content)}, nil
	case ".jpg", ".jpeg":
		if detected := http.DetectContentType(content); detected != "image/jpeg" {
			return nil, fmt.Errorf("source logo extension is JPEG but content is %s", detected)
		}
		return &organizationLogo{MimeType: "image/jpeg", ImageData: base64.StdEncoding.EncodeToString(content)}, nil
	default:
		return nil, fmt.Errorf("source logo must be SVG, PNG or JPEG")
	}
}
