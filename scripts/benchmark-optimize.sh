#!/bin/bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

go test ./internal/optimize -run '^$' -bench '^BenchmarkGenerateSaliencyMap_Prof$' -benchmem
