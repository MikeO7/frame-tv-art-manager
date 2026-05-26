🔍 **Gap:**
The `NewClient` function in `internal/samsung/client.go`, which is the primary public entry point for establishing connections to the TV, lacked explicit parameter documentation and real-world usage examples. This missing context could lead to confusion about how the returned client handles connection states and timeouts, causing contextual decay over time.

📝 **Update:**
Added explicit parameter descriptions (IP, options, logger), documented the return type, and provided a real-world code example in the JSDoc/GoDoc header of `NewClient`. This brings it into alignment with other core utilities like `LoadMapping` and `OptimizeFile`.

🎯 **Audience:**
Future human engineers and AI agents who need to understand how to instantiate, connect, and safely close the Samsung TV client.
