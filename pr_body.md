🎯 **Target**: Removed `gopkg.in/yaml.v3` dependency and the associated YAML parsing logic (`loadYamlSources`) from `internal/sources/loader_sync.go`.
📉 **Drop**: Deleted `gopkg.in/yaml.v3` module from `go.mod` reducing binary bloat. Removed 129 lines of code. Replaced `sources.yaml` functionality strictly with simpler natively-handled `.txt` line-by-line configuration logic for fetching sources.
🔬 **Proof**: `go test ./...` and compilation succeed completely on `loader_test.go` using text fallback logic. Deadcode and Linter passing.
