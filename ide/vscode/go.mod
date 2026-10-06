// A module boundary, not a Go module.
//
// This directory holds a TypeScript VS Code extension. It contains no Go, and
// this file exists so that the Go toolchain treats the whole subtree as outside
// the parent module.
//
// Without it, `go build ./...` and `go test ./...` descend into
// node_modules/, and at least one popular npm dependency ships Go source in its
// package. The Lensyxe module then compiles a transitive JavaScript dependency's
// code, so a change in an unrelated npm package — or a Go version bump — could
// break the whole project's build.
//
// The boundary makes that impossible: the parent module stops here.
//
// There is deliberately no `require` block and no dependency on the parent
// module. Nothing here is ever built by `go build` from the repository root.
module github.com/zelvior/lensyxe/ide/vscode

go 1.22