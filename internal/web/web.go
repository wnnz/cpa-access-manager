package web

import _ "embed"

//go:embed apikey.html
var APIKeyHTML string

//go:embed shared-ui.css
var SharedUICSS string

//go:embed settings.html
var SettingsHTML string

//go:embed usage-statistics.html
var UsageStatisticsHTML string
