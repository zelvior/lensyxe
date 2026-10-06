# Healthy Go example

A small, deliberately well-shaped Go project used by the integration suite.

It exists so the tests have a repository whose expected output is known by
construction rather than by inspection: small files, tests present, a lockfile,
and a conventional layout. A CI gate that rejects this repository would be
wrong, so anything it does is a bug.

The file is not a Go module of its own so it can sit inside the Lensyxe
repository without interfering with `go build ./...`. Analysis targets it
directly by path.