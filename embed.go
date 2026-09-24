package pod

import "embed"

// uiFS holds the HTML form interface templates shipped with pod. They are
// plain html/template documents using the native web platform only.
//
//go:embed ui/*.html
var uiFS embed.FS
