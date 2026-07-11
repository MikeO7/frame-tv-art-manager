#!/bin/bash
set -euo pipefail

test_pipeline_failure_is_preserved() {
    if bash -o pipefail -c 'exit 23 | tee /dev/null'; then
        echo "pipeline unexpectedly succeeded" >&2
        return 1
    fi
}

test_agent_loop_enables_pipefail() {
    grep -Eq '^set -euo pipefail$' scripts/agent-loop.sh
}

test_pipeline_failure_is_preserved
test_agent_loop_enables_pipefail
