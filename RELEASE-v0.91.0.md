# App SDK v0.91.0

Adds `Tool.Annotations` for standard MCP behavior hints such as `readOnlyHint`
and `destructiveHint`. The framework preserves explicitly false values and
publishes annotations at the top level of each `tools/list` entry, separate
from `_meta`. Unannotated tools and app-only exposure are unchanged.

This is an additive SDK change; annotations are not execution permissions.
Regression coverage verifies read/write hints, legacy response shapes,
manifest metadata, and private-tool visibility.
