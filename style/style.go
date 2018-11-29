package style

import "github.com/fatih/color"

var Identifier = color.New(color.FgHiWhite, color.Bold).SprintFunc()

var Tip = color.New(color.FgHiGreen, color.Bold).SprintfFunc()

var Error = color.New(color.FgRed, color.Bold).SprintfFunc()

var Separator = color.HiCyanString
