🔍 **Gap**: The `.env.example` file lacked documentation for `VERIFY_TLS` and did not provide clear annotations that `LOCATION_LATITUDE` and `LOCATION_LONGITUDE` are required when `SOLAR_BRIGHTNESS_ENABLED` is enabled. The manual brightness setting also lacked explicit value range boundaries.

📝 **Update**: Added `VERIFY_TLS` to the "Advanced System Settings" section with a default of `false` matching Frame TV self-signed certificate constraints. Updated the "Brightness" section to explicitly denote the 0-50 range for manual brightness and annotated required dependencies for solar brightness.

🎯 **Audience**: Future human engineers and AI agents configuring local setups or deployment pipelines, preventing confusing initialization crashes.
