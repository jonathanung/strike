package lsp

import (
	"path/filepath"
	"strings"
)

// languageID maps a file extension (with leading dot) to an LSP languageId.
// Unknown extensions fall back to the extension without the dot (or "plaintext").
func languageID(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if id, ok := extLanguage[ext]; ok {
		return id
	}
	if ext == "" {
		return "plaintext"
	}
	return strings.TrimPrefix(ext, ".")
}

// normalizeExt returns a lowercase extension with a leading dot, or "".
func normalizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}

var extLanguage = map[string]string{
	".go":     "go",
	".mod":    "go.mod",
	".sum":    "go.sum",
	".ts":     "typescript",
	".tsx":    "typescriptreact",
	".js":     "javascript",
	".jsx":    "javascriptreact",
	".mjs":    "javascript",
	".cjs":    "javascript",
	".py":     "python",
	".pyi":    "python",
	".rs":     "rust",
	".c":      "c",
	".h":      "c",
	".cc":     "cpp",
	".cpp":    "cpp",
	".cxx":    "cpp",
	".hpp":    "cpp",
	".hh":     "cpp",
	".java":   "java",
	".kt":     "kotlin",
	".kts":    "kotlin",
	".rb":     "ruby",
	".php":    "php",
	".cs":     "csharp",
	".fs":     "fsharp",
	".swift":  "swift",
	".m":      "objective-c",
	".mm":     "objective-cpp",
	".scala":  "scala",
	".lua":    "lua",
	".r":      "r",
	".jl":     "julia",
	".zig":    "zig",
	".sh":     "shellscript",
	".bash":   "shellscript",
	".zsh":    "shellscript",
	".json":   "json",
	".jsonc":  "jsonc",
	".yaml":   "yaml",
	".yml":    "yaml",
	".toml":   "toml",
	".md":     "markdown",
	".html":   "html",
	".htm":    "html",
	".css":    "css",
	".scss":   "scss",
	".less":   "less",
	".vue":    "vue",
	".svelte": "svelte",
	".sql":    "sql",
	".proto":  "protobuf",
	".xml":    "xml",
	".dart":   "dart",
	".ex":     "elixir",
	".exs":    "elixir",
	".erl":    "erlang",
	".hrl":    "erlang",
	".hs":     "haskell",
	".lhs":    "haskell",
	".ml":     "ocaml",
	".mli":    "ocaml",
	".clj":    "clojure",
	".cljs":   "clojure",
	".nim":    "nim",
	".v":      "v",
}
