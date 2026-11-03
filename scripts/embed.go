package scripts

import _ "embed"

// ShidianExport is shared by the automatic browser and the manual console path.
// Embedding it keeps the released executable independent of the source checkout.
//
//go:embed shidian-export.js
var ShidianExport string
