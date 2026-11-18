# Endpoint Lighthouse

## Problem

When companies integrate with external APIs (like GitHub, Stripe, Twilio, or public government/financial feeds), upstream providers frequently make unannounced changes—renaming a JSON key, changing a field from an integer to a string, or altering HTTP headers. Endpoint Lighthouse continuously monitors public APIs and OpenAPI/Swagger specs, detects structural drift the moment it happens, and uses AI to generate a plain-English breaking-change report and client migration patch.