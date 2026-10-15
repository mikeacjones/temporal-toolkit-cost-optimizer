---
id: CODE-008
title: SDK version with a known cost bug
why_it_costs: Some SDK releases sent requests that later releases don't, and each extra request that reaches the server can be an Action.
fix: Upgrade the SDK to the version the finding names.
tradeoffs: Read the SDK's release notes between your version and the fixed one before upgrading.
docs:
  - kb: cost/evaluate-cloud-actions#activity
locator:
  sdk_calls: {}
---

tcopt reads your lockfiles (go.mod, uv.lock, requirements.txt, package-lock.json, pom.xml, build.gradle, and .csproj files) and compares each Temporal SDK version with a list of releases known to cost extra Actions.

A release goes on the list only when its affected versions are confirmed. One issue is waiting on that today: SDKs built on sdk-core sent an extra Heartbeat when a heartbeating Activity completed successfully, fixed in sdk-core in September 2025. The SDK versions that include the fix aren't confirmed yet.
