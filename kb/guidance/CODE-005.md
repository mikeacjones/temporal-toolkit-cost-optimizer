---
id: CODE-005
title: Schedule update that sets only cron expressions
why_it_costs: The server turns cron expressions into calendar entries. An update that sets cron expressions on the existing spec keeps the old calendars and adds new ones, so the spec and the Schedule's history grow with every update.
fix: Replace the whole spec in DoUpdate, or clear the calendars, instead of setting CronExpressions on the spec you got from Describe.
tradeoffs: None. Replacing the spec also replaces any intervals or skips set on it, so carry over the ones you want to keep.
docs:
  - kb: cost/best-practices-cost-optimization
snippets:
  go: snippets/CODE-005.go.md
locator:
  sdk_calls:
    go: ["client.ScheduleHandle.Update", "client.ScheduleUpdateOptions"]
---

The Schedule update edits the spec it read back from the server and sets only its cron expressions. The spec it read back already holds calendar entries made from the previous cron expressions, so each update appends duplicates.

Open Schedules are system Workflows, and their histories count as Active Storage. A spec that grows on every update grows that history too. Whether a Schedule update is itself billed as an Action isn't confirmed.
