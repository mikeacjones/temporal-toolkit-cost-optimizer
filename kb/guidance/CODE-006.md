---
id: CODE-006
title: Java Local Activity arguments recorded in markers
why_it_costs: Each Local Activity result is a marker in history. If the arguments are recorded in the marker too, history and storage grow with every Local Activity call.
fix: Set setDoNotIncludeArgumentsIntoMarker(true) on LocalActivityOptions where the arguments don't need to be in history.
tradeoffs: The arguments no longer appear in history, so debugging a past run from its history alone gets harder.
docs:
  - kb: cost/evaluate-cloud-actions#activity
snippets:
  java: snippets/CODE-006.java.md
locator:
  sdk_calls:
    java: ["LocalActivityOptions.Builder", "setDoNotIncludeArgumentsIntoMarker"]
---

This check applies to Java only, and tcopt doesn't report it yet: whether Java records Local Activity arguments in markers by default isn't confirmed. It's here so the rule and its fix are ready when it is.

This is about the size of existing Local Activity markers. It doesn't recommend Local Activities; for work that doesn't fit their narrow use cases, a regular Activity is the better fix (STO-006).
