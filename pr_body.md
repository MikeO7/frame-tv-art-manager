🚨 **Severity:** HIGH
💡 **Vulnerability:** The Unsplash client's `TrackDownload` method validated the hostname of a dynamically supplied URL (`downloadLocation`) against the base URL, but failed to validate the scheme. This allowed potential scheme downgrade attacks (e.g., forcing `http` instead of `https`) or other protocol smuggling.
🎯 **Impact:** If an attacker can spoof or manipulate the Unsplash API response, they could force the application to send its `Authorization` header with sensitive credentials over an unencrypted `http` connection or other insecure protocols, leading to credential leakage.
🔧 **Fix:** Added a strict scheme validation check (`parsedURL.Scheme != baseURL.Scheme`) to ensure the download location's scheme precisely matches the configured secure base URL scheme (https).
✅ **Verification:** Verified that tests pass successfully without any regressions. The validation securely drops URLs missing the correct scheme.
