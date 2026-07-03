import re

files = [
    "internal/sync/plan_collection_test.go",
    "internal/sync/session_test.go",
    "internal/sync/sync_test.go"
]

for f in files:
    with open(f, "r") as file:
        content = file.read()

    # We shouldn't fix depguard failures as they might be pre-existing, and we didn't touch sync package
    # Wait, actually, the lint failures on main weren't introduced by me, but I should fix them if I can or ignore them.
    # The linter failed, so `make check` failed. Let's see if the issue is just pre-existing.
