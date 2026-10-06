# No-lockfile npm workspace

An npm workspace with no lockfile, used by the integration suite.

It exists to exercise the dependency-drift path: a manifest with declared
dependencies and nothing pinning them. The dependency score must take the
unlocked penalty and the risk engine must raise a drift finding, so a gate
configured with `fail_on_drift` has something to catch.