package main

import _ "embed"

// panelHTML is the browser UI for the ordering rule. It is served from the
// plugin's resource route, which CPA exposes without management auth, so the page
// itself must stay free of secrets: every read it performs goes through the
// management API with the key the operator already holds in the CPA panel.
//
//go:embed panel.html
var panelHTML string
