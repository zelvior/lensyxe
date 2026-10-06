// This blank import registers the -update flag in this test binary.
//
// go test ./... -update passes the flag to every test binary it builds,
// and a binary that does not define it fails with lag provided but not
// defined. One import per test package is what makes the documented
// command work everywhere rather than only in the package that owns the
// golden files.

package compare

import _ "github.com/zelvior/lensyxe/internal/golden"
